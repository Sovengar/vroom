package tui

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/orchestrate"
	"vroom/internal/process"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// Los caminos que dependen de un fallo del entorno real.
//
// Nada de esto se puede probar con un doble: los fallos que se provocan son de
// verdad —un puerto que no se puede atribuir, un `command_start` que no existe,
// un servicio con el mismo nombre en dos directorios— y los Motores que hacen
// falta son los de verdad, porque su contrato de error es justo lo que se está
// comprobando.
// ---------------------------------------------------------------------------

// TestStopCmdEscribeEnElLogLosAvisosDelParado: el `Warn` que no se traga.
//
// `Stop` puede terminar el proceso y no conseguir todo lo que le pedía, y eso no
// es un error: es un aviso. El aviso tiene que acabar en el log de stderr del
// servicio, porque es lo único que el usuario puede leer después del hecho —la
// TUI ya ha vuelto a la lista—.
//
// El aviso se provoca de verdad: el servicio declara un puerto que NO se puede
// atribuir a nadie. MEDIDO: un mismo puerto escuchando a la vez en IPv4 y en IPv6
// desde el mismo proceso da DOS dueños en `/proc/net/tcp`, y con dos dueños el
// fallo de `killPortHolderWith` es cerrado a propósito —no se mata nada y se
// avisa—, que es exactamente el aviso que se quiere ver en el log.
func TestStopCmdEscribeEnElLogLosAvisosDelParado(t *testing.T) {
	m, store := newTestModel(t)
	p := primerProyectoConfigurado(t, m)

	// El mismo puerto en las dos familias: `/proc/net/tcp` lo lista dos veces.
	port := puertoConDueñoAmbiguo(t)

	if err := store.SaveMeta(p.Path, state.Meta{Pid: 0, Port: port}); err != nil {
		t.Fatal(err)
	}

	cmd := stopCmd(store, process.NewManager(), p.Path, "")
	if cmd == nil {
		t.Fatal("stopCmd tiene que devolver un comando")
	}
	if _, ok := cmd().(stoppedMsg); !ok {
		t.Fatalf("el comando devolvió %T, want stoppedMsg", cmd())
	}

	log, err := os.ReadFile(store.StderrLog(p.Path))
	if err != nil {
		t.Fatalf("no hay log de stderr del servicio: %v", err)
	}
	aviso := string(log)
	if !strings.Contains(aviso, "vroom ▶ stop") {
		t.Errorf("el log de stderr = %q, want una línea de aviso de stop: un aviso que no se "+
			"escribe en ningún sitio no es un aviso", aviso)
	}
	if !strings.Contains(aviso, "no se mata nada") {
		t.Errorf("el aviso = %q, want que explique que no se mata nada: el usuario tiene que "+
			"entender por qué su puerto sigue ocupado", aviso)
	}
}

// TestStartCmdPropagaElFalloDePersistirElMeta: el arranque que no termina.
//
// Es el camino que ya tuvo un bug: cuando el proceso SÍ arrancó pero guardar el
// meta falla, se devuelve error sin ningún PID, y el caller no tiene forma de
// parar al hijo. Lo que se puede comprobar desde la TUI es la mitad pública del
// contrato: el mensaje trae el error y lo trae con el nombre del servicio.
//
// El fallo se provoca de verdad, sin tocar la persistencia: se convierte el
// `meta.json` del servicio en un directorio no vacío, así que escribirlo falla con
// EISDIR. Es lo que pasa cuando el directorio de estado quedó a medias por una
// copia o por un `sudo` de otro sitio.
func TestStartCmdPropagaElFalloDePersistirElMeta(t *testing.T) {
	m, store := newTestModel(t)
	p := primerProyectoConfigurado(t, m)

	// El servicio arranca de verdad, para que el fallo sea el de persistencia y no
	// el de un comando imposible: `sh -c` sale con 127 y el spawn sí tiene éxito.
	p.Manifest = &manifest.Manifest{Name: "tienda-api", Command: "sleep 30", Port: 8081}

	dir, err := store.EnsureServiceDir(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	// MEDIDO: `SaveMeta` escribe `meta.json.tmp` y luego renombra. Con `meta.json`
	// ya siendo un directorio no vacío el rename falla con EISDIR, que es el
	// fallo que se quiere; el tmp sí llega a escribirse antes.
	if err := os.MkdirAll(filepath.Join(dir, "meta.json", "bloqueo"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Logf("meta de %s en el directorio de servicio %s", p.Path, dir)

	cmd := startCmd(store, &stubManager{}, p)
	if cmd == nil {
		t.Fatal("startCmd tiene que devolver un comando")
	}
	msg, ok := cmd().(startedMsg)
	if !ok {
		t.Fatalf("el comando devolvió %T, want startedMsg", cmd())
	}
	if msg.err == nil {
		t.Error("startedMsg.err = nil con un meta que no se puede guardar: el servicio se quedaría " +
			"parado en la TUI sin explicación de por qué")
	}
	if msg.path != p.Path {
		t.Errorf("path = %q, want %q: el mensaje tiene que decir a qué servicio se refiere", msg.path, p.Path)
	}
	if msg.res.Pid != 0 {
		t.Errorf("startedMsg.res.Pid = %d con un meta que no se pudo guardar, want 0: el proceso se "+
			"para antes de propagar el error, así que el mensaje no puede llevar un pid que el "+
			"usuario no pueda parar", msg.res.Pid)
	}
}

// TestToggleDeComposersAvisaDeUnStackQueNoResuelve: el `stackStats` con error.
//
// El caso real es renombrar un servicio en el manifiesto y no en el compose file,
// o borrar el directorio. El stack sigue ahí, con un nombre que ya no existe, y
// `toggleComposers` tiene que decirlo en vez de arrancar medio grupo.
//
// El aviso importa por el orden: `stackStats` recorre TODOS los stacks antes de
// lanzar ninguno, así que un nombre roto en el segundo stack no deja el primero
// arrancado a medias.
func TestToggleDeGrupoDeStacksAvisaDeUnStackQueNoResuelve(t *testing.T) {
	isolateConfig(t)
	root := writeTestTree(t, false)
	writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), `primary_group = "tienda"

[[stack]]
name = "front"
primary_group = "tienda"
  [[stack.stage]]
  name = "front"
  services = ["tienda-web", "tienda-api"]

[[stack]]
name = "roto"
primary_group = "tienda"
  [[stack.stage]]
  name = "roto"
  services = ["servicio-que-no-existe"]
`)
	store := state.NewStoreAt(t.TempDir())
	m := New(store, &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()
	if m.engine == nil {
		t.Fatalf("New no construyó el engine")
	}
	if len(m.stacksForPrimary("tienda")) != 2 {
		t.Fatalf("hay %d stacks, want 2", len(m.stacksForPrimary("tienda")))
	}

	// Todos los servicios del grupo están parados: la condición de arranque.
	for _, p := range m.projects {
		if sv := m.services[p.Path]; sv != nil {
			sv.Status = statusStopped
		}
	}

	nuevo, cmd := m.toggleComposers("tienda")
	got, ok := nuevo.(Model)
	if !ok {
		t.Fatalf("toggleComposers devolvió %T, want Model", nuevo)
	}
	if cmd != nil {
		t.Error("un stack que no resuelve no puede lanzar nada: ni siquiera el otro")
	}
	if !strings.Contains(got.message, "stack conflict") {
		t.Errorf("message = %q, want que diga que hay un conflicto de stack", got.message)
	}
	// Y el nombre del culpable tiene que estar en el mensaje: sin él, el usuario
	// tiene que abrir el compose file a adivinar cuál de los dos stacks está mal.
	if !strings.Contains(got.message, "servicio-que-no-existe") {
		t.Errorf("message = %q, want que nombre el servicio que no resuelve", got.message)
	}
	// Ningún servicio del grupo pasó a arrancarse.
	for _, p := range m.projects {
		if sv := got.services[p.Path]; sv != nil && sv.Status != statusStopped {
			t.Errorf("%s quedó en %q tras un conflicto de stack: el fallo se detectó ANTES de "+
				"lanzar nada, y así tiene que quedar", p.Name, sv.Status)
		}
	}
}

// TestToggleSobreUnStackDeVerdadVaPorLaRamaDelStack: el `case` de `toggleStack`.
//
// El árbol de `newStackModel` sí trae filas `itemStack` con su stack puesto, pero
// ningún test pulsaba la tecla sobre una de ellas: se llamaba a `toggleStack`
// directamente. La diferencia no es de código sino de contrato —que la fila del
// árbol, que es lo que el cursor puede señalar, llegue al toggle—.
func TestToggleSobreUnStackDeVerdadVaPorLaRamaDelStack(t *testing.T) {
	m := newStackModel(t)

	idx := -1
	for i, e := range m.tree {
		if e.kind == itemStack {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("el árbol con compose file no trajo ninguna fila de stack: este test no está probando nada")
	}
	m.cursor = idx

	nuevo, cmd := m.toggleSelected()
	got, ok := nuevo.(Model)
	if !ok {
		t.Fatalf("toggleSelected devolvió %T, want Model", nuevo)
	}
	if cmd == nil {
		t.Error("pulsar la tecla sobre un stack parado tiene que lanzar la orquestación")
	}
	if !strings.Contains(got.message, "launching stack") {
		t.Errorf("message = %q, want que diga que está lanzando el stack: es la confirmación "+
			"de que la tecla fue a la fila que el cursor señalaba", got.message)
	}
}

// TestToggleDeGrupoSaltaAlMiembroSinEstadoConocidoEnElGrupoElegido: el `continue`
// bien dirigido.
//
// El caso real es un miembro que el modelo no conoce porque se coló en el grupo
// entre el escaneo y la pulsación, o que su manifiesto dejó de parsear. Sin el
// `continue`, ese miembro caería en el `switch` con `sv` a nil y el panic
// reventaría en mitad del arranque del grupo, con el resto ya lanzándose.
//
// Lo que se comprueba es que los miembros CON estado sí avanzan y el sin estado
// se salta, que es la diferencia entre "se ignoró un miembro" y "no arrancó nada".
func TestToggleDeGrupoSaltaAlMiembroSinEstadoConocidoEnElGrupoElegido(t *testing.T) {
	m, _ := newTestModel(t)

	prim := ""
	idx := -1
	for i, e := range m.tree {
		if e.kind == itemPrimary {
			prim = e.primary
			idx = i
			break
		}
	}
	if prim == "" {
		t.Skip("el árbol de test no trae grupos")
	}

	miembros := m.nodeMembers(prim, "")
	if len(miembros) < 2 {
		t.Skipf("el grupo tiene %d miembros, want al menos 2", len(miembros))
	}

	for _, p := range m.projects {
		if sv := m.services[p.Path]; sv != nil {
			sv.Status = statusStopped
		}
	}
	// El primero del grupo es el que se queda sin estado.
	perdido := miembros[0].Path
	delete(m.services, perdido)

	m.cursor = idx
	nuevo, _ := m.toggleSelected()
	got, ok := nuevo.(Model)
	if !ok {
		t.Fatalf("toggleSelected devolvió %T, want Model", nuevo)
	}
	if sv := got.services[perdido]; sv != nil {
		t.Errorf("el miembro sin estado %s volvió a aparecer con estado %v", perdido, sv.Status)
	}
	for _, p := range miembros[1:] {
		sv := got.services[p.Path]
		if sv == nil {
			t.Fatalf("el miembro %s sin estado desapareció del mapa en el camino", p.Path)
		}
		if sv.Status != statusStarting {
			t.Errorf("%s quedó en %q tras arrancar el grupo, want starting: saltarse un miembro "+
				"sin estado no puede ser una excusa para no arrancar los demás", p.Name, sv.Status)
		}
	}
}

// TestPickerInnerWTieneSueloEnUnTerminalEstrecho: el `min` de 28.
//
// El modal de picker se abre con la pantalla ya calculada, así que con un terminal
// de 20 columnas el ancho interior salía en 6 y cada fila del picker se recortaba a
// seis celdas: el usuario veía tragamanchas sin poder leer qué elegía.
func TestPickerInnerWTieneSueloEnUnTerminalEstrecho(t *testing.T) {
	m, _ := newTestModel(t)
	m.width = 20
	m.updateLayout()
	m.pickerItems = []pickerItem{{Name: "un servicio con nombre larguísimo", Description: "y su descripción"}}

	if w := m.pickerInnerW(); w != 28 {
		t.Errorf("pickerInnerW() = %d con un terminal de 20 columnas, want 28: por debajo el modal "+
			"no es usable y el usuario no puede leer lo que elige", w)
	}

	// Y que no reviente al componer la caja con ese ancho.
	if s := m.pickerBox(); s == "" {
		t.Error("pickerBox() devolvió vacío")
	}
}

// TestElArbolOcultaLosStacksDeLosGruposPlegados: el `continue` de `buildTree`.
//
// Hay tres formas de que un stack no llegue a verse, y las tres son rutas que el
// usuario toma a propósito:
//
//   - plegar el primario entero, que también se lleva sus stacks —si no, los
//     stacks se quedarían flotando sin el grupo al que pertenecen—;
//   - plegar sólo la cabecera de Composers, que deja los stacks escondidos pero
//     deja ver al grupo;
//   - un primario que no tiene stacks, que no debe abrir una cabecera de Composers
//     vacía.
//
// La tercera es la que más se nota: un primario de un solo proyecto sin stacks
// ganaba una fila de cabecera que no llevaba a ninguna parte.
func TestElArbolOcultaLosStacksDeLosGruposPlegados(t *testing.T) {
	m := modeloConDosPrimariosYStacks(t)

	// Un primario con stacks y otro sin ellos: el segundo no puede abrir cabecera.
	todos := m.tree
	conStacks := -1
	for i, e := range todos {
		if e.kind == itemSecondary && e.secondary == composersGroup {
			conStacks = i
			break
		}
	}
	if conStacks < 0 {
		t.Fatalf("el árbol no trajo ninguna cabecera de Composers: hay %d filas", len(todos))
	}

	primConStacks := todos[conStacks].primary
	p := projectPath(t, m, "tienda-web")
	primSinStacks := manifestPrimary(t, p)

	// Sin plegar: hay cabecera de Composers para el primario que tiene stacks y
	// NO para el que no tiene ninguno.
	if !m.treeTieneFila(func(e treeItem) bool {
		return e.kind == itemSecondary && e.primary == primSinStacks && e.secondary == composersGroup
	}) {
		t.Errorf("el primario %s no tiene stacks pero abrió cabecera de Composers: una fila que "+
			"no lleva a nada es ruido", primSinStacks)
	}

	// Con el primario plegado, sus stacks desaparecen con él.
	colapsado := m
	colapsado.collapsed = map[string]bool{primConStacks: true}
	arbol := colapsado.buildTree()
	for _, e := range arbol {
		if e.primary == primConStacks && (e.kind == itemStack || e.secondary == composersGroup) {
			t.Errorf("con el primario %s plegado apareció la fila %v/%v: plegar un grupo tiene que "+
				"esconder también sus stacks, o se quedan flotando sin dueño", primConStacks, e.kind, e.secondary)
		}
	}

	// Y con la cabecera de Composers plegada, los stacks tampoco salen, pero el
	// resto del grupo sigue visible.
	porComposers := m
	porComposers.collapsed = map[string]bool{porComposers.secondaryKey(primConStacks, composersGroup): true}
	arbol = porComposers.buildTree()
	vioStacks := false
	for _, e := range arbol {
		if e.primary == primConStacks && e.kind == itemStack {
			vioStacks = true
		}
	}
	if vioStacks {
		t.Errorf("con la cabecera de Composers de %s plegada sus stacks siguen en el árbol",
			primConStacks)
	}
	if !porComposers.treeTieneFila(func(e treeItem) bool {
		return e.kind == itemProject && e.project.Path == p
	}) {
		t.Errorf("plegar la cabecera de Composers de %s se llevó también los proyectos del grupo: "+
			"una cosa es plegar los stacks y otra el grupo entero", primConStacks)
	}
}

// modeloConDosPrimariosYStacks construye un modelo con dos grupos, stacks sólo en
// uno de ellos. El grupo sin stacks es el caso que abre una cabecera de Composers
// vacía, y para eso hace falta que exista de verdad un segundo primario.
func modeloConDosPrimariosYStacks(t *testing.T) Model {
	t.Helper()
	isolateConfig(t)
	root := writeTestTree(t, false)

	// Un segundo primario, con su servicio, que no aparece en el compose file.
	writeStr(t, filepath.Join(root, "blog", ".vroom.toml"),
		"name = \"blog\"\ncommand_start = \"true\"\nprimary_group = \"otro\"\n")

	writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), `primary_group = "tienda"

[[stack]]
name = "front"
primary_group = "tienda"
  [[stack.stage]]
  name = "front"
  services = ["tienda-web", "tienda-api"]
`)

	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()
	return m
}

// treeTieneFila dice si alguna fila del árbol cumple el predicado.
func (m Model) treeTieneFila(pred func(treeItem) bool) bool {
	for _, e := range m.buildTree() {
		if pred(e) {
			return true
		}
	}
	return false
}

// manifestPrimary devuelve el primary_group del manifiesto de un proyecto.
func manifestPrimary(t *testing.T, path string) string {
	t.Helper()
	mf, err := manifest.Parse(filepath.Join(path, manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return mf.PrimaryGroup
}

// ---------------------------------------------------------------------------
// Utilidades
// ---------------------------------------------------------------------------

// puertoConDueñoAmbiguo abre un puerto a la vez en IPv4 e IPv6 desde este
// proceso y devuelve el número.
//
// MEDIDO: `/proc/net/tcp` lista una entrada por socket, así que el mismo pid
// aparece DOS veces como dueño del mismo puerto. `distinctOwners` los ve, son dos,
// y la política de `killPortHolderWith` es no matar nada cuando no puede probar de
// quién es. Es el caso real de un puerto publicado en las dos familias sin que
// vroom pueda decidir cuál es el suyo.
func puertoConDueñoAmbiguo(t *testing.T) int {
	t.Helper()
	ln4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// El cierre va con t.Cleanup y NO con defer a propósito: si los listeners
	// murieran al devolver la función, el puerto quedaría libre justo cuando el
	// test lo necesita ocupado, y la prueba pasaría sin provocar nada.
	t.Cleanup(func() { _ = ln4.Close() })

	puerto := ln4.Addr().(*net.TCPAddr).Port
	ln6, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", puerto))
	if err != nil {
		// El host no tiene IPv6: se salta en vez de dar un falso "probado".
		t.Skipf("no se pudo abrir el mismo puerto en IPv6: %v", err)
	}
	t.Cleanup(func() { _ = ln6.Close() })

	// MEDIDO: sin esto la advertencia no se dispara, porque `PortOwnerPIDs` lee
	// `/proc/net/tcp` y necesita las entradas SYN_RECV/ESTABLISHED de verdad.
	for _, ln := range []net.Listener{ln4, ln6} {
		go func(ln net.Listener) {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				_ = c.Close()
			}
		}(ln)
	}
	esperaDueño(t, puerto)
	if !process.PortOpen(puerto) {
		t.Fatalf("el puerto %d no está abierto, así que el camino que se quiere probar —el "+
			"puerto ocupado que no se puede atribuir— no se va a recorrer", puerto)
	}

	return puerto
}

// esperaDueño espera a que el puerto aparezca con al menos dos dueños en
// `/proc/net/tcp`.
func esperaDueño(t *testing.T, puerto int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(process.PortOwnerPIDs(puerto)) >= 2 {
			return
		}
		// Una conexión que se cierra al instante deja el socket en el kernel el
		// tiempo justo para que se pueda leer; se fuerza una conexión viva.
		go func() {
			for _, addr := range []string{
				fmt.Sprintf("127.0.0.1:%d", puerto),
				fmt.Sprintf("[::1]:%d", puerto),
			} {
				c, err := net.Dial("tcp", addr)
				if err == nil {
					_ = c.Close()
				}
			}
		}()
		time.Sleep(50 * time.Millisecond)
	}
	t.Skipf("el puerto %d no llegó a tener dos dueños en /proc/net/tcp en 3s: este test no "+
		"puede provocar el aviso en este host", puerto)
}

// TestElArbolMuestraLosStacksDeUnPrimarioSinProyectos: el primario fantasma.
//
// Un stack declara a qué grupo pertenece, y ese grupo se busca entre los proyectos
// escaneados. Si el grupo no tiene ningún proyecto —porque se borró el directorio
// entero, o porque el stack se copió de otro workspace—, el primario no aparece en
// ninguna parte del recorrido y sus stacks desaparecían con él.
//
// Es el caso inverso al que ya se probaba, y el que de verdad duele: el usuario
// tiene su `vroom.compose.toml` con un stack bien escrito, pulsa start/stop y no ve
// ninguna fila que pulsar. Sin este camino, `buildTree` sólo junta los grupos que ya
// están en `visible`.
func TestElArbolMuestraLosStacksDeUnPrimarioSinProyectos(t *testing.T) {
	isolateConfig(t)
	root := writeTestTree(t, false)
	writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), `primary_group = "tienda"

[[stack]]
name = "front"
primary_group = "tienda"
  [[stack.stage]]
  name = "front"
  services = ["tienda-web", "tienda-api"]

[[stack]]
name = "fantasma"
primary_group = "grupo-que-no-tiene-proyectos"
  [[stack.stage]]
  name = "fantasma"
  services = ["tienda-web"]
`)

	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()

	stacks := m.stacksForPrimary("grupo-que-no-tiene-proyectos")
	if len(stacks) != 1 {
		t.Fatalf("hay %d stacks para el grupo fantasma, want 1", len(stacks))
	}

	visto := false
	for _, e := range m.tree {
		if e.kind == itemStack && e.primary == "grupo-que-no-tiene-proyectos" {
			visto = true
			if e.stack == nil {
				t.Error("la fila del stack del grupo fantasma salió con stack nil: el cursor puede " +
					"señalarla y `toggleSelected` no sabría qué lanzar")
			}
		}
	}
	if !visto {
		var filas []string
		for _, e := range m.tree {
			filas = append(filas, string(rune('0'+int(e.kind)))+":"+e.primary+"/"+e.secondary)
		}
		t.Errorf("el stack del grupo fantasma no aparece en el árbol (%v): el usuario tiene un "+
			"compose con ese stack y no tiene ninguna fila que pulsar", filas)
	}
}
