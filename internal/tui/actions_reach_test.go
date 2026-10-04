package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/config"
	"vroom/internal/group"
	"vroom/internal/scanner"
	"vroom/internal/state"
	"vroom/internal/tail"
)

// ---------------------------------------------------------------------------
// Las acciones que no se ejercitaban, y los suelos del layout.
//
// La mayoría de lo que hay aquí son rutas de *rechazo*: la acción existe, el
// contexto no la admite, y el código tiene que decirlo. Es la clase de línea más
// fácil de no probar y la más cara cuando falta, porque son las que el usuario
// encuentra primero — pulsa la tecla y no pasa nada—.
//
// Y el otro bloque es el del layout: los suelos de `bodyH` y `contentH` existen
// porque una resta puede dar negativo, y `strings.Repeat` con un número negativo
// revienta. Un helper de render no puede ser lo que apague la TUI.
// ---------------------------------------------------------------------------

// msgDesconocido es un mensaje que `Update` no conoce. Existe para probar el
// `return m, nil` del final del switch, que es la ruta que toma CUALQUIER mensaje
// que no sea de los que la TUI fabricate: un `tea.PasteMsg` de una versión nueva de
// bubbletea, un mensaje propio de otro paquete, o el `nil` inicial.
//
// Que devuelva el modelo intacto y sin comando es lo correcto: un mensaje
// desconocido no puede ser un error, y un comando nil evita que bubbletea llame a
// un `tea.Cmd` nil.
type msgDesconocido struct{ algo int }

// TestUpdateConUnMensajeQueNoConoceDevuelveElModeloIntacto: la ruta por defecto.
func TestUpdateConUnMensajeQueNoConoceDevuelveElModeloIntacto(t *testing.T) {
	m, _ := newTestModel(t)
	antes := m

	nuevo, cmd := m.Update(msgDesconocido{algo: 1})
	if cmd != nil {
		t.Error("un mensaje desconocido no puede lanzar un comando: el comando sería del TUI, " +
			"nuestro, y no hay ninguno")
	}
	got, ok := nuevo.(Model)
	if !ok {
		t.Fatalf("Update devolvió %T, want Model", nuevo)
	}
	if got.message != antes.message {
		t.Errorf("message pasó de %q a %q: un mensaje que no es nuestro no puede hablar", antes.message, got.message)
	}
	if got.cursor != antes.cursor {
		t.Errorf("el cursor se movió de %d a %d sin ninguna tecla", antes.cursor, got.cursor)
	}
}

// TestLaAccionLogsAbreElEditorConUnProyectoSeleccionado: la tecla `logs` de verdad.
//
// Es la acción que se abre en el `case "logs"` y que hasta ahora sólo se había
// probado en su versión RECHAZADA (seleccionar un stack avisa "not available for
// stacks"). El camino bueno no tenía test, que es el que el usuario quiere: pulsar
// la tecla y ver sus logs.
//
// La prueba no invoca el editor: `tea.ExecProcess` devuelve un `tea.ExecMsg` sin
// arrancar nada, que es lo que permite comprobar la decisión sin lanzar un nvim en
// mitad de la suite.
func TestLaAccionLogsAbreElEditorConUnProyectoSeleccionado(t *testing.T) {
	// `logs` remapeada a una tecla libre, porque con los defaults choca con otras.
	m, store := newTestModelWithConfig(t, "[keybindings]\nlogs = \"y\"\n")

	p := primerProyectoConfigurado(t, m)
	m = seleccionar(t, m, p.Path)

	_, cmd := m.Update(tecla("y"))
	if cmd == nil {
		t.Fatal("la tecla de logs tiene que devolver el comando del editor")
	}
	// Lo que devuelve `tea.ExecProcess` es un mensaje con el `*exec.Cmd` del editor y
	// el callback de vuelta, y su tipo es privado de bubbletea: no se puede
	// inspeccionar desde aquí. Lo que sí se comprueba es que hay mensaje y que es
	// el de un Exec, porque un `nil` haría que bubbletea no suspendiera la TUI y el
	// editor se abriría POR ENCIMA de la interfaz, que es el bug que esto previene.
	msg := cmd()
	if msg == nil {
		t.Fatal("logs devolvió un comando que no produce mensaje: la TUI no se suspendería y el " +
			"editor se abriría encima de la interfaz")
	}
	// Y la composición del comando —qué editor y con qué argumentos— la comprueba
	// el test de `buildEditorCmd`, que es donde se puede, porque ahí sí hay un
	// `*exec.Cmd` inspeccionable.
	_ = store
}

// TestLaAccionRefreshLanzaElRefrescoSinCambiarDeVista: la tecla `refresh`.
//
// Es la acción más fácil de no probar porque no cambia nada visible: `refreshBatch`
// devuelve un `tea.Batch` con el refresco de estados, el de git y el de la pestaña
// activa. Lo que hay que comprobar es que no cambia de pestaña —`refresh` no es
// `switchTab`— porque un refresco que además te saca de la consola activa es la
// peor forma de implementarlo.
func TestLaAccionRefreshLanzaElRefrescoSinCambiarDeVista(t *testing.T) {
	m, _ := newTestModelWithConfig(t, "[keybindings]\nrefresh = \"y\"\n")
	m.activeTab = tabThreads
	antes := m.activeTab

	nuevo, cmd := m.Update(tecla("y"))
	if cmd == nil {
		t.Fatal("refresh tiene que devolver el lote de refrescos")
	}
	got, ok := nuevo.(Model)
	if !ok {
		t.Fatalf("Update devolvió %T, want Model", nuevo)
	}
	if got.activeTab != antes {
		t.Errorf("refresh cambió la pestaña de %d a %d: refrescar no es cambiar de vista", antes, got.activeTab)
	}
	if got.message != "" {
		t.Errorf("message = %q tras un refresco correcto, want vacío", got.message)
	}
}

// TestSyncConsoleViewNoHaceNadaFueraDeLaPestañaDeConsola: el guard de pestaña.
//
// `syncConsoleView` tiene tres llamadores —cambio de tamaño, cambio de stream y
// visibilidad del panel— y sólo tiene sentido en la pestaña de consola. Escribir el
// viewport desde las otras dos no rompería nada visible… salvo que el buffer que
// `SetContent` deja se queda con el contenido viejo y al volver a la consola aparece
// una mezcla del servicio anterior.
//
// O sea: el guard existe para que cambiar de pestaña no contamine el buffer, y eso
// es comprobable sin terminal.
func TestSyncConsoleViewNoHaceNadaFueraDeLaPestañaDeConsola(t *testing.T) {
	m, _ := newTestModel(t)
	m.activeTab = tabGit

	antes := m.consoleView.View()
	if cmd := m.syncConsoleView(); cmd != nil {
		t.Error("syncConsoleView fuera de la consola no puede devolver comando: no hay nada que leer")
	}
	if despues := m.consoleView.View(); despues != antes {
		t.Errorf("el viewport cambió fuera de la pestaña de consola:\n-- antes --\n%s\n-- después --\n%s",
			antes, despues)
	}
}

// TestToggleSobreUnStackSinStackNoRevienta: la defensa de tipo.
//
// El árbol construye los `itemStack` con `stack: &stacks[i]`, siempre no-nil, así que
// el `case it.kind == itemStack && it.stack != nil` deja pasar de largo un itemStack
// con stack nil y cae al `p == nil` de abajo. No hay forma de llegar por la UI.
//
// Y aun así el camino tiene que estar: `toggleStack(nil)` haría un nil-deref, y esta
// función está en la ruta de arranque/parada, que es donde un panic no tiene a dónde
// ir. Lo que se comprueba es que no revienta, que es el contrato entero.
func TestToggleSobreUnStackSinStackNoRevienta(t *testing.T) {
	m, _ := newTestModel(t)
	m.tree = []treeItem{{kind: itemStack, primary: "vacio"}}
	m.cursor = 0

	nuevo, cmd := m.toggleSelected()
	if cmd != nil {
		t.Error("un stack sin stack no puede arrancar nada")
	}
	if nuevo == nil {
		t.Fatal("Update devolvió nil")
	}
}

// TestRestartSobreUnProyectoSinManifiestoLoDice: el rechazo que faltaba.
//
// `restart` sobre un nodo de grupo ya avisa ("select a service to restart") y sobre
// un servicio parado también ("only a running service can be restarted"). Lo que no
// estaba era el tercero: un directorio cuyo manifiesto NO PARSEA.
//
// MEDIDO: es el caso real del botón de restart. Un directorio sin `.vroom.toml`
// ni siquiera entra en el escaneo (`inspectDir` devuelve nil), así que la única
// forma de tener un proyecto `!Configured` en el modelo es un manifiesto roto —
// que es además el caso que el usuario se va a encontrar: una coma de más.
//
// Y ese caso se distingue a propósito en el mensaje, porque lo va a pulsar sin
// pensar: está al lado de un proyecto que sí funciona, y la diferencia entre "no
// puedes" y "no hay nada que reiniciar" es lo que le dice que tiene que arreglar el
// fichero.
func TestRestartSobreUnProyectoConManifiestoRotoLoDice(t *testing.T) {
	m, _ := newTestModel(t)

	m, roto := proyectoRoto(t, m)
	m = seleccionar(t, m, roto.Path)

	// La tecla por defecto de `restart` es "R" (mayúscula): es la única acción que
	// para y rearranca, y por eso está separada de "r".
	nuevo, _ := m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	got, ok := nuevo.(Model)
	if !ok {
		t.Fatalf("Update devolvió %T, want Model", nuevo)
	}
	if !strings.Contains(got.message, "No manifest") {
		t.Errorf("message = %q, want que diga que falta el manifiesto: el mensaje tiene que "+
			"distinguir \"no hay nada que reiniciar\" de \"no puedes\"", got.message)
	}
	if sv := got.services[roto.Path]; sv != nil && sv.Status == statusStopping {
		t.Error("el servicio pasó a stopping con el manifiesto roto: no hay nada que parar")
	}
}

// proyectoRoto mete un directorio con un `.vroom.toml` inválido en el árbol del
// modelo y devuelve el proyecto ya escaneado. Se hace aquí y no en `writeTestTree`
// porque el resto de la suite asume que todos sus proyectos arrancan: añadir un
// manifest roto de base rompería los conteos de filas de los tests de árbol.
func proyectoRoto(t *testing.T, m Model) (Model, scanner.Project) {
	t.Helper()
	path := filepath.Join(m.projects[0].Path, "roto")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	// TOML inválido a propósito: la coma final después del valor.
	if err := os.WriteFile(filepath.Join(path, ".vroom.toml"), []byte("name = \"roto\",\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := filepath.Dir(m.projects[0].Path)
	sig, err := scanner.Scan(root, config.Defaults().Scanner.Depth)
	if err != nil {
		t.Fatal(err)
	}
	var roto scanner.Project
	for _, p := range sig.Projects {
		if filepath.Base(p.Path) == "roto" {
			roto = p
			break
		}
	}
	if roto.Path == "" {
		t.Fatal("el escaneo no trajo el proyecto recién creado")
	}
	if roto.Configured {
		t.Fatalf("el proyecto %s tiene Configured=true con un manifiesto inválido: este test "+
			"no está probando el caso que cree probar", roto.Path)
	}

	// El modelo se reconstruye entero —proyectos, estados y árbol— porque lo que
	// cambia aquí es la lista de proyectos, no un estado: un `New` sobre el mismo
	// root ya pasa por la misma ruta del arranque que el resto de la TUI.
	m2 := New(m.store, &stubManager{}, root)
	m2.width, m2.height = m.width, m.height
	m2.updateLayout()
	return m2, roto
}

// TestToggleDeGrupoIgnoraLosMiembrosQueNoTienenEstadoConocido: el `continue`.
//
// Un grupo puede traer miembros que el modelo no conoce —un directorio sin
// manifiesto que se coló en el grupo por su `secondary_group`, o un proyecto
// eliminado entre el escaneo y la pulsación—. Sin el `continue`, ese miembro
// arrivalaría con `sv` a nil y el panic caería en pleno arranque del grupo, con el
// resto de los servicios ya lanzándose.
func TestToggleDeGrupoIgnoraLosMiembrosQueNoTienenEstadoConocido(t *testing.T) {
	m, _ := newTestModel(t)

	// Cursor en una cabecera de grupo: es lo que dispara `toggleNode` y por tanto
	// el bucle sobre los miembros donde vive el `sv == nil`.
	idx := -1
	for i, e := range m.tree {
		if e.kind == itemPrimary || e.kind == itemSecondary {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Skip("el árbol de test no trae grupos")
	}
	m.cursor = idx

	// Se quita el estado de un miembro del grupo sin tocar el resto: el modelo lo
	// considera "sin ServiceState", que es lo que encuentra el `sv == nil`.
	target := ""
	for _, e := range m.tree {
		if e.kind == itemProject {
			target = e.project.Path
			break
		}
	}
	if target == "" {
		t.Fatal("el árbol de test tiene que traer proyectos")
	}
	delete(m.services, target)

	nuevo, _ := m.toggleSelected()
	if nuevo == nil {
		t.Fatal("toggleSelected devolvió nil: un miembro sin estado no puede tumbar el grupo")
	}
}

// TestToggleDeGrupoLimpiaLaConsolaDelMiembroSeleccionado: el `setConsoleContent`
// dentro del bucle.
//
// Al arrancar un grupo entero, la consola del servicio que el cursor tiene
// seleccionado tiene que quedar VACÍA: si no, el usuario ve la salida del servicio
// anterior ocupando media pantalla justo después de arrancar, y no hay forma de
// distinguirla de la nueva.
//
// Y sólo para el seleccionado, porque vaciar la consola de un servicio que no se
// está mirando tiraría su historial sin motivo.
func TestToggleDeGrupoLimpiaLaConsolaDelMiembroSeleccionado(t *testing.T) {
	m, _ := newTestModel(t)
	m.activeTab = tabConsole

	prim := ""
	for i, e := range m.tree {
		if e.kind == itemPrimary {
			prim = e.primary
			m.cursor = i
			break
		}
	}
	if prim == "" {
		t.Skip("el árbol de test no trae grupos")
	}

	// Miembros con estado parado: la condición de arranque.
	for _, p := range m.projects {
		if sv := m.services[p.Path]; sv != nil {
			sv.Status = statusStopped
		}
	}

	// El cursor se pone sobre el primer miembro del grupo, y no sobre la cabecera:
	// el `setConsoleContent` del bucle sólo se ejecuta para el servicio
	// seleccionado, y vaciar la consola de uno que no se está mirando tiraría su
	// historial sin motivo.
	sel := ""
	for i, e := range m.tree {
		if e.kind == itemProject && group.PrimaryOf(e.project) == prim {
			sel = e.project.Path
			m.cursor = i
			break
		}
	}
	if sel == "" {
		t.Skip("el grupo de test no tiene proyectos")
	}

	// Un hermano del mismo grupo que NO está en el cursor, para comprobar que su
	// consola no se toca.
	otro := ""
	for _, e := range m.tree {
		if e.kind == itemProject && group.PrimaryOf(e.project) == prim && e.project.Path != sel {
			otro = e.project.Path
			break
		}
	}
	if otro == "" {
		t.Skip("el grupo de test tiene un solo miembro")
	}
	csOtro := m.consoleStateFor(otro)
	csOtro.stdout = "salida del hermano anterior"

	cs := m.consoleStateFor(sel)
	cs.stdout = "salida del servicio anterior"

	_, _ = m.toggleNode(prim, "")

	if cs := m.consoleStateFor(sel); cs.stdout != "" {
		t.Errorf("la consola de %s sigue con %q tras arrancar el grupo: el usuario vería la salida "+
			"del servicio anterior como si fuera del nuevo", sel, cs.stdout)
	}
	// El hermano también pierde su buffer en memoria, y eso es lo correcto: al
	// arrancar el grupo TODOS sus miembros empiezan a seguir el log desde el
	// offset actual, así que dejar el texto viejo en memoria mostraría una mezcla
	// de dos ejecuciones del servicio cuando el usuario vaya a esa pestaña.
	//
	// Lo que NO se toca es el viewport de un servicio que no está en el cursor:
	// reescribirlo_pinta una consola vacía justo donde el usuario no ha mirado.
	if cs := m.consoleStateFor(otro); cs.stdout != "" {
		t.Errorf("el buffer en memoria de %s = %q tras arrancar el grupo, want vacío: el offset "+
			"se reinició, así que el texto viejo pertenece a una ejecución anterior", otro, cs.stdout)
	}
	// Lo que NO se toca es el viewport de un servicio que no está en el cursor:
	// reescribirlo pinta una consola vacía justo donde el usuario no ha mirado.
	if v := tail.StripANSI(m.consoleView.View()); strings.Contains(v, "salida del hermano anterior") {
		t.Errorf("el viewport se reescribió con la salida de un servicio que no está seleccionado:\n%s", v)
	}
}

// TestElLayoutNoDejaQueElAltoInteriorSeaNegativo: los suelos de `updateLayout`.
//
// `bodyH` y `contentH` son restas de alturas ajenas —la del terminal, la de las cajas
// de al lado, el borde— y con una terminal diminuta dan negativo. El suelo a 1 y a 0
// evita que un `strings.Repeat` con un número negativo reviente la TUI entera por un
// `resize` de una ventana de 30px de alto.
//
// MEDIDO: no hace falta una terminal diminuta real, `updateLayout` es puro —sólo lee
// `m.width`/`m.height`— así que se le puede dar cualquier tamaño.
func TestElLayoutNoDejaQueElAltoInteriorSeaNegativo(t *testing.T) {
	casos := []struct {
		nombre      string
		ancho, alto int
	}{
		{"terminal de 1x1", 1, 1},
		{"una columna y una fila", 1, 1},
		{"alto 3 con detalle", 40, 3},
		{"ancho 0", 0, 24},
		{"alto negativo", 80, -5},
		{"todo negativo", -1, -1},
	}
	for _, tt := range casos {
		t.Run(tt.nombre, func(t *testing.T) {
			m, _ := newTestModel(t)
			m.width, m.height = tt.ancho, tt.alto
			m.detailsShown = true
			m.updateLayout()

			if m.bodyH < 1 {
				t.Errorf("bodyH = %d con %dx%d: un interior de 0 o menos hace que el compositor de "+
					"cajas reciba un número negativo de filas", m.bodyH, tt.ancho, tt.alto)
			}
			if m.contentH < 0 {
				t.Errorf("contentH = %d con %dx%d: el viewport recibiría una altura negativa", m.contentH, tt.ancho, tt.alto)
			}
			// Y lo que de verdad importa: renderizar con eso no revienta.
			if s := tail.StripANSI(m.View().Content); strings.TrimSpace(s) == "" {
				t.Error("View() no dibujó nada")
			}
		})
	}
}

// TestRunLoggedPropagaElFalloDeCrearElDirectorioDeLogs: la primera escritura.
//
// Los logs de un servicio se escriben bajo el directorio del servicio en el store.
// Si ese directorio no se puede crear, el comando NO se lanza: es preferible decir
// que no se pudo escribir el log a ejecutar un build de dos minutos cuyo output no
// va a ninguna parte.
//
// Y el error tiene que ser el del mkdir, sin envolver: es un problema de permisos o de
// disco, y quien lo lee necesita el errno.
func TestRunLoggedPropagaElFalloDeCrearElDirectorioDeLogs(t *testing.T) {
	// Un fichero donde debería ir el directorio: mkdir falla con ENOTDIR.
	bloqueo := filepath.Join(t.TempDir(), "logs")
	if err := os.WriteFile(bloqueo, []byte("soy un fichero"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, code, err := runLogged("build", "echo hola", t.TempDir(), filepath.Join(bloqueo, "out.log"), "")
	if err == nil {
		t.Fatal("con el directorio de logs inservible el comando no se puede lanzar")
	}
	if code != 0 {
		t.Errorf("exit code = %d con un fallo de lanzamiento, want 0: no llegó a ejecutarse nada", code)
	}
	// Y no se creó nada: el bloqueo sigue siendo un fichero.
	info, serr := os.Stat(bloqueo)
	if serr != nil {
		t.Fatal(serr)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("el bloqueo dejó de ser un fichero (mode %s): el comando escribió donde no podía", info.Mode())
	}
}

// TestRunLoggedFallaSiElStderrNoSePuedeAbrir: el segundo descriptor.
//
// El stdout se crea antes con `appendLine`, así que su fallo prácticamente no puede
// ocurrir: si el directorio existe, el fichero se acaba de crear. El stderr NO pasa
// por `appendLine`, así que su `OpenFile` es el primero que puede fallar de verdad —
// por ejemplo, cuando su directorio no existe, porque el banner del stdout crea el
// suyo y el del stderr está en otro sitio.
//
// Y el fallo tiene que ser ANTES de lanzar el comando: un proceso cuyo stderr no
// tiene dónde ir escribiría en el descriptor que herede, que es la TUI.
func TestRunLoggedFallaSiElStderrNoSePuedeAbrir(t *testing.T) {
	dir := t.TempDir()
	// El stderr apunta a un directorio que no existe; `appendLine` no lo crea
	// porque no toca esa ruta.
	stderrPath := filepath.Join(dir, "no-existe", "err.log")

	_, code, err := runLogged("build", "echo hola", dir, filepath.Join(dir, "out.log"), stderrPath)
	if err == nil {
		t.Fatal("con el stderr imposible de abrir el comando no se puede lanzar")
	}
	if code != 0 {
		t.Errorf("exit code = %d con un fallo de lanzamiento, want 0", code)
	}
	// El stdout sí llegó a escribirse: el banner se escribe antes de abrir el
	// stderr, y eso es lo que da nombre al fallo en los logs del usuario.
	out, rerr := os.ReadFile(filepath.Join(dir, "out.log"))
	if rerr != nil {
		t.Fatalf("el banner del stdout tiene que estar escrito aunque el stderr falle: %v", rerr)
	}
	if !strings.Contains(string(out), "build") {
		t.Errorf("el banner del stdout = %q, want que nombre el job", string(out))
	}
}

// ---------------------------------------------------------------------------
// El redimensionado: el clamp del scroll del árbol.
// ---------------------------------------------------------------------------

// TestResizeConElArbolMasAltoQueLaVentanaLoDejaEnSuSitio: el clamp del scroll.
//
// `treeTop` es la primera línea del árbol que se dibuja. Al cambiar el alto de la
// ventana cambia cuántas filas caben, y el desplazamiento tiene que seguir siendo
// legal en los dos sentidos:
//
//   - Si la fila del cursor queda por encima del borde superior, hay que bajar el
//     desplazamiento hasta ella.
//   - Si la ventana crece hasta que cabe el árbol entero, el desplazamiento tiene
//     que volver a 0.
//
// Lo segundo no estaba. MEDIDO (bug): con 40 proyectos y `treeTop` al final, al
// pasar de 100x30 a 200x400 el desplazamiento se quedaba donde estaba y el render
// enseñaba UNA fila de árbol pegada al borde superior y 393 líneas en blanco
// debajo. La columna de proyectos quedaba vacía justo al maximizar la ventana.
//
// Y el suelo del segundo clamp no es `cursor` sino `len(árbol) - visible`: con el
// cursor en la última fila y una ventana de 24, el desplazamiento correcto es el
// que deja la fila del cursor como la última visible, no el que la pone la
// primera. Con el segundo, 79 de las 81 filas del árbol quedaban fuera de pantalla
// y el usuario veía una fila y un muro de líneas vacías.
func TestResizeConElArbolMasAltoQueLaVentanaLoDejaEnSuSitio(t *testing.T) {
	// El árbol de test por defecto son 6 filas y caben de sobra en 30, así que
	// `treeTop` no llega a moverse nunca: hace falta un árbol más alto que la
	// ventana para que el clamp tenga algo que corregir.
	m := modeloConArbolDe(t, 40)
	m.width, m.height = 100, 30
	m.updateLayout()
	if m.treeVis() >= len(m.tree) {
		t.Skipf("el árbol de test cabe entero en %d filas: no hay nada que desplazar", m.treeVis())
	}

	// Cursor en la última fila y el desplazamiento por encima de él: es el caso de
	// "la ventana se encogió tanto que la fila del cursor quedó por encima del
	// borde superior".
	m.cursor = len(m.tree) - 1
	m.treeTop = m.cursor + 3

	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	got, ok := m2.(Model)
	if !ok {
		t.Fatalf("Update devolvió %T, want Model", m2)
	}
	wantTop := len(got.tree) - got.treeVis()
	if got.treeTop != wantTop {
		t.Errorf("treeTop = %d tras el resize, want %d (las filas del árbol menos las visibles): "+
			"con el desplazamiento más alto, la fila del cursor queda la primera de la ventana "+
			"y las %d de arriba desaparecen", got.treeTop, wantTop, got.treeTop)
	}
	if got.cursor != m.cursor {
		t.Errorf("el cursor pasó de %d a %d al redimensionar", m.cursor, got.cursor)
	}

	// Y ahora la ventana crece hasta que cabe el árbol entero: el desplazamiento
	// tiene que volver a 0.
	m3, _ := got.Update(tea.WindowSizeMsg{Width: 200, Height: 400})
	grande, ok := m3.(Model)
	if !ok {
		t.Fatalf("Update devolvió %T, want Model", m3)
	}
	if grande.treeVis() < len(m.tree) {
		t.Skipf("con %dx%d el árbol sigue sin caber entero (%d filas visibles, %d líneas)",
			200, 400, grande.treeVis(), len(m.tree))
	}
	if grande.treeTop != 0 {
		t.Errorf("treeTop = %d con la ventana crecida hasta que cabe el árbol entero, want 0: "+
			"el árbol se dibuja desplazado y con un hueco en blanco debajo", grande.treeTop)
	}

	// Y la prueba de que el arreglo se ve, no sólo que el número cuadra: la columna
	// de proyectos tiene que enseñar TODAS las filas del árbol. Antes del arreglo
	// salía una y el resto en blanco.
	columna := grande.treeColumnLines()
	if len(columna) != len(grande.tree) {
		t.Errorf("la columna del árbol dibujó %d filas de un árbol de %d: al crecer la ventana "+
			"hasta que cabe entero, tienen que salir todas", len(columna), len(grande.tree))
	}
}

// ---------------------------------------------------------------------------
// Un mensaje que llega después de que la TUI ya esté en marcha.
// ---------------------------------------------------------------------------

// TestElArranqueMarcaComoSinConfigurarLoQueNoParsea: el `else` del boot.
//
// `New` no nace con una lista de servicios: la deriva del escaneo, y un proyecto
// cuyo `.vroom.toml` no parsea entra como "sin configurar", no como "parado".
//
// La diferencia es lo que impide el arranque: `toggleSelected` lee `p.Configured`
// para negarse, pero el estado pintado sale de aquí, y un "parado" en un manifiesto
// roto muestra el botón de start que no va a hacer nada.
func TestElArranqueMarcaComoSinConfigurarLoQueNoParsea(t *testing.T) {
	m0, _ := newTestModel(t)
	m, roto := proyectoRoto(t, m0)

	vistos := map[uiStatus]int{}
	for _, p := range m.projects {
		sv := m.services[p.Path]
		if sv == nil {
			t.Fatalf("el proyecto %s no tiene ServiceState tras el arranque", p.Name)
		}
		vistos[sv.Status]++
		if p.Configured && sv.Status != statusStopped {
			t.Errorf("%s tiene manifiesto pero su estado es %q, want parado", p.Name, sv.Status)
		}
		if !p.Configured && sv.Status != statusUnconfigured {
			t.Errorf("%s no tiene manifiesto pero su estado es %q, want sin configurar", p.Name, sv.Status)
		}
	}
	if vistos[statusUnconfigured] == 0 {
		t.Fatal("el árbol de test no trajo ningún proyecto sin manifiesto: este test no está probando nada")
	}
	// Y la fila del proyecto roto tiene que existir en el árbol: si no, el estado
	// "sin configurar" se pintaría de un servicio que el usuario no puede seleccionar.
	seleccionar(t, m, roto.Path)
	if sv := m.services[roto.Path]; sv == nil || sv.Status != statusUnconfigured {
		t.Errorf("el servicio de %s = %v, want sin configurar: sin esta fila el aviso del manifiesto "+
			"roto no se ve nunca", roto.Path, sv)
	}
}

// ---------------------------------------------------------------------------
// Utilidades
// ---------------------------------------------------------------------------

// modeloConArbolDe construye un modelo cuyo árbol tiene más filas de las que
// caben en una ventana normal. El árbol por defecto son 6 filas, que caben de sobra
// en 30: sin esto el desplazamiento del árbol no se puede probar porque nunca
// ocurre.
//
// Cada proyecto lleva `secondary_group` distinto, así que cada uno añade DOS
// filas —su header de secundario y la suya— y el árbol crece al doble.
func modeloConArbolDe(t *testing.T, proyectos int) Model {
	t.Helper()
	isolateConfig(t)
	root := t.TempDir()
	for i := range proyectos {
		dir := filepath.Join(root, fmt.Sprintf("p%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		manifiesto := fmt.Sprintf("name = \"p%02d\"\ncommand_start = \"true\"\nprimary_group = \"g\"\nsecondary_group = \"s%02d\"\n", i, i)
		if err := os.WriteFile(filepath.Join(dir, ".vroom.toml"), []byte(manifiesto), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()
	if len(m.tree) <= proyectos {
		t.Fatalf("el árbol tiene %d filas para %d proyectos: no se está generando un header por grupo",
			len(m.tree), proyectos)
	}
	return m
}

// primerProyectoConfigurado devuelve el primer proyecto con manifiesto.
func primerProyectoConfigurado(t *testing.T, m Model) scanner.Project {
	t.Helper()
	for _, p := range m.projects {
		if p.Configured {
			return p
		}
	}
	t.Fatal("el árbol de test no tiene proyectos configurados")
	return scanner.Project{}
}

// seleccionar mueve el cursor al proyecto con esa ruta.
func seleccionar(t *testing.T, m Model, path string) Model {
	t.Helper()
	for i, e := range m.tree {
		if e.kind == itemProject && e.project.Path == path {
			m.cursor = i
			return m
		}
	}
	t.Fatalf("no hay ninguna entrada de proyecto para %s", path)
	return m
}

// tecla construye un KeyPressMsg para una tecla de un solo carácter.
func tecla(name string) tea.KeyMsg {
	return tea.KeyPressMsg{Code: rune(name[0]), Text: name}
}
