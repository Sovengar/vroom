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

func TestStopCmdEscribeEnElLogLosAvisosDelParado(t *testing.T) {
	m, store := newTestModel(t)
	p := primerProyectoConfigurado(t, m)

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

// Regression anchor for the "spawned but meta failed" bug: the propagated error carries no PID because the child is already killed by then.
func TestStartCmdPropagaElFalloDePersistirElMeta(t *testing.T) {
	m, store := newTestModel(t)
	p := primerProyectoConfigurado(t, m)

	// A command that really spawns, so the failure under test is the persistence one and not an impossible spawn.
	p.Manifest = &manifest.Manifest{Name: "tienda-api", Command: "sleep 30", Port: 8081}

	dir, err := store.EnsureServiceDir(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	// MEDIDO: SaveMeta writes meta.json.tmp and then renames, so with meta.json already a non-empty directory the rename fails with EISDIR.
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

// stackStats resolves every stack before launching any, so an unresolvable name in the second must leave the first untouched.
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
	if !strings.Contains(got.message, "servicio-que-no-existe") {
		t.Errorf("message = %q, want que nombre el servicio que no resuelve", got.message)
	}
	for _, p := range m.projects {
		if sv := got.services[p.Path]; sv != nil && sv.Status != statusStopped {
			t.Errorf("%s quedó en %q tras un conflicto de stack: el fallo se detectó ANTES de "+
				"lanzar nada, y así tiene que quedar", p.Name, sv.Status)
		}
	}
}

// Regression anchor: before this, no test pressed the key on an itemStack row, only called toggleStack directly.
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

// Without the nil-sv continue an unknown member panics mid-launch while the rest of the group is already starting.
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

func TestPickerInnerWTieneSueloEnUnTerminalEstrecho(t *testing.T) {
	m, _ := newTestModel(t)
	m.width = 20
	m.updateLayout()
	m.pickerItems = []pickerItem{{Name: "un servicio con nombre larguísimo", Description: "y su descripción"}}

	if w := m.pickerInnerW(); w != 28 {
		t.Errorf("pickerInnerW() = %d con un terminal de 20 columnas, want 28: por debajo el modal "+
			"no es usable y el usuario no puede leer lo que elige", w)
	}

	if s := m.pickerBox(); s == "" {
		t.Error("pickerBox() devolvió vacío")
	}
}

func TestElArbolOcultaLosStacksDeLosGruposPlegados(t *testing.T) {
	m := modeloConDosPrimariosYStacks(t)

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

	if !m.treeTieneFila(func(e treeItem) bool {
		return e.kind == itemSecondary && e.primary == primSinStacks && e.secondary == composersGroup
	}) {
		t.Errorf("el primario %s no tiene stacks pero abrió cabecera de Composers: una fila que "+
			"no lleva a nada es ruido", primSinStacks)
	}

	colapsado := m
	colapsado.collapsed = map[string]bool{primConStacks: true}
	arbol := colapsado.buildTree()
	for _, e := range arbol {
		if e.primary == primConStacks && (e.kind == itemStack || e.secondary == composersGroup) {
			t.Errorf("con el primario %s plegado apareció la fila %v/%v: plegar un grupo tiene que "+
				"esconder también sus stacks, o se quedan flotando sin dueño", primConStacks, e.kind, e.secondary)
		}
	}

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

// A real second primary is required because a group with no stacks must not open an empty Composers header.
func modeloConDosPrimariosYStacks(t *testing.T) Model {
	t.Helper()
	isolateConfig(t)
	root := writeTestTree(t, false)

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

func (m Model) treeTieneFila(pred func(treeItem) bool) bool {
	for _, e := range m.buildTree() {
		if pred(e) {
			return true
		}
	}
	return false
}

func manifestPrimary(t *testing.T, path string) string {
	t.Helper()
	mf, err := manifest.Parse(filepath.Join(path, manifest.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return mf.PrimaryGroup
}

// MEDIDO: /proc/net/tcp lists one entry per socket, so the same pid shows up as two owners of one port and killPortHolderWith refuses to kill what it cannot attribute.
func puertoConDueñoAmbiguo(t *testing.T) int {
	t.Helper()
	ln4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// t.Cleanup, not defer: a defer would free the port right when the test needs it held and the assertions would pass without provoking anything.
	t.Cleanup(func() { _ = ln4.Close() })

	puerto := ln4.Addr().(*net.TCPAddr).Port
	ln6, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", puerto))
	if err != nil {
		// Skip instead of fail when the host has no IPv6, so a missing family is not reported as a pass.
		t.Skipf("no se pudo abrir el mismo puerto en IPv6: %v", err)
	}
	t.Cleanup(func() { _ = ln6.Close() })

	// MEDIDO: without these accept loops PortOwnerPIDs finds nothing, since it reads real SYN_RECV/ESTABLISHED entries from /proc/net/tcp.
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

func esperaDueño(t *testing.T, puerto int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(process.PortOwnerPIDs(puerto)) >= 2 {
			return
		}
		// A connection closed instantly can vanish from the kernel before it is read, so one is kept alive.
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

// A stack whose primary has no scanned projects (deleted directory, stack copied from another workspace) used to vanish with it, leaving a valid compose stack with no row to press.
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
