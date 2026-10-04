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

type msgDesconocido struct{ algo int }

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

// The editor is never launched here: tea.ExecProcess returns its ExecMsg without starting anything.
func TestLaAccionLogsAbreElEditorConUnProyectoSeleccionado(t *testing.T) {
	// Remapped to a free key because the default collides with another binding.
	m, store := newTestModelWithConfig(t, "[keybindings]\nlogs = \"y\"\n")

	p := primerProyectoConfigurado(t, m)
	m = seleccionar(t, m, p.Path)

	_, cmd := m.Update(tecla("y"))
	if cmd == nil {
		t.Fatal("la tecla de logs tiene que devolver el comando del editor")
	}
	// ExecMsg is private to bubbletea, so the only thing assertable here is that a message came back; nil would skip suspending the TUI and the editor would open on top of it.
	msg := cmd()
	if msg == nil {
		t.Fatal("logs devolvió un comando que no produce mensaje: la TUI no se suspendería y el " +
			"editor se abriría encima de la interfaz")
	}
	// Editor choice and arguments are asserted in TestBuildEditorCmd, where the *exec.Cmd is inspectable.
	_ = store
}

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

// The tab guard exists so a resize outside the console leaves no stale buffer for the console to show later.
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

// Unreachable through the UI (tree items always carry a stack), but toggleStack(nil) would nil-deref on the start/stop path.
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

// MEDIDO: a directory with no .vroom.toml never enters the scan, so a broken manifest is the only way to get an unconfigured project; the message must name the manifest, not say "you can't".
func TestRestartSobreUnProyectoConManifiestoRotoLoDice(t *testing.T) {
	m, _ := newTestModel(t)

	m, roto := proyectoRoto(t, m)
	m = seleccionar(t, m, roto.Path)

	// The default restart key is capital "R" because it is the only stop-then-start action, so it stays apart from "r".
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

// Kept out of writeTestTree because the rest of the suite assumes every project starts and its row counts depend on that.
func proyectoRoto(t *testing.T, m Model) (Model, scanner.Project) {
	t.Helper()
	path := filepath.Join(m.projects[0].Path, "roto")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	// Invalid TOML on purpose: the trailing comma after the value.
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

	// The whole model is rebuilt because what changes here is the project list, not a status.
	m2 := New(m.store, &stubManager{}, root)
	m2.width, m2.height = m.width, m.height
	m2.updateLayout()
	return m2, roto
}

// A group can carry members the model does not know, so without the continue a nil sv would nil-deref mid group start.
func TestToggleDeGrupoIgnoraLosMiembrosQueNoTienenEstadoConocido(t *testing.T) {
	m, _ := newTestModel(t)

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

// Only the selected member's console is cleared: emptying a service nobody is looking at would discard its history for nothing.
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

	for _, p := range m.projects {
		if sv := m.services[p.Path]; sv != nil {
			sv.Status = statusStopped
		}
	}

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
	// The sibling's in-memory buffer is cleared too: every member restarts tailing from the current offset, so stale text would mix two runs; the viewport of an unselected service is not rewritten, because painting an empty console where the user is not looking looks like a regression.
	if cs := m.consoleStateFor(otro); cs.stdout != "" {
		t.Errorf("el buffer en memoria de %s = %q tras arrancar el grupo, want vacío: el offset "+
			"se reinició, así que el texto viejo pertenece a una ejecución anterior", otro, cs.stdout)
	}
	if v := tail.StripANSI(m.consoleView.View()); strings.Contains(v, "salida del hermano anterior") {
		t.Errorf("el viewport se reescribió con la salida de un servicio que no está seleccionado:\n%s", v)
	}
}

// MEDIDO: these heights are subtractions that can go negative and strings.Repeat panics on a negative count; updateLayout is pure, so any size can be fed without a real tiny terminal.
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
			if s := tail.StripANSI(m.View().Content); strings.TrimSpace(s) == "" {
				t.Error("View() no dibujó nada")
			}
		})
	}
}

// The command must not launch at all, and the mkdir error must reach the caller unwrapped because whoever reads it needs the errno.
func TestRunLoggedPropagaElFalloDeCrearElDirectorioDeLogs(t *testing.T) {
	// A regular file where the log directory goes, so mkdir fails with ENOTDIR.
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
	info, serr := os.Stat(bloqueo)
	if serr != nil {
		t.Fatal(serr)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("el bloqueo dejó de ser un fichero (mode %s): el comando escribió donde no podía", info.Mode())
	}
}

// stdout is created earlier by appendLine, so stderr's open is the first that can really fail, and it must fail before launch or the child writes to the inherited fd, which is the TUI.
func TestRunLoggedFallaSiElStderrNoSePuedeAbrir(t *testing.T) {
	dir := t.TempDir()
	stderrPath := filepath.Join(dir, "no-existe", "err.log")

	_, code, err := runLogged("build", "echo hola", dir, filepath.Join(dir, "out.log"), stderrPath)
	if err == nil {
		t.Fatal("con el stderr imposible de abrir el comando no se puede lanzar")
	}
	if code != 0 {
		t.Errorf("exit code = %d con un fallo de lanzamiento, want 0", code)
	}
	// The stdout banner is written before stderr is opened, so the log names the job even when stderr fails.
	out, rerr := os.ReadFile(filepath.Join(dir, "out.log"))
	if rerr != nil {
		t.Fatalf("el banner del stdout tiene que estar escrito aunque el stderr falle: %v", rerr)
	}
	if !strings.Contains(string(out), "build") {
		t.Errorf("el banner del stdout = %q, want que nombre el job", string(out))
	}
}

// MEDIDO (bug): with 40 projects and treeTop at the end, growing the window from 100x30 to 200x400 left the scroll in place and rendered one row of tree over hundreds of blank lines.
func TestResizeConElArbolMasAltoQueLaVentanaLoDejaEnSuSitio(t *testing.T) {
	m := modeloConArbolDe(t, 40)
	m.width, m.height = 100, 30
	m.updateLayout()
	if m.treeVis() >= len(m.tree) {
		t.Skipf("el árbol de test cabe entero en %d filas: no hay nada que desplazar", m.treeVis())
	}

	m.cursor = len(m.tree) - 1
	m.treeTop = m.cursor + 3

	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	got, ok := m2.(Model)
	if !ok {
		t.Fatalf("Update devolvió %T, want Model", m2)
	}
	// The clamp floors at len(tree)-visible, not at the cursor, so the cursor row ends up last visible instead of first.
	wantTop := len(got.tree) - got.treeVis()
	if got.treeTop != wantTop {
		t.Errorf("treeTop = %d tras el resize, want %d (las filas del árbol menos las visibles): "+
			"con el desplazamiento más alto, la fila del cursor queda la primera de la ventana "+
			"y las %d de arriba desaparecen", got.treeTop, wantTop, got.treeTop)
	}
	if got.cursor != m.cursor {
		t.Errorf("el cursor pasó de %d a %d al redimensionar", m.cursor, got.cursor)
	}

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

	columna := grande.treeColumnLines()
	if len(columna) != len(grande.tree) {
		t.Errorf("la columna del árbol dibujó %d filas de un árbol de %d: al crecer la ventana "+
			"hasta que cabe entero, tienen que salir todas", len(columna), len(grande.tree))
	}
}

// A project whose manifest fails to parse must be shown unconfigured, not stopped, or the UI offers a start button that does nothing.
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
	seleccionar(t, m, roto.Path)
	if sv := m.services[roto.Path]; sv == nil || sv.Status != statusUnconfigured {
		t.Errorf("el servicio de %s = %v, want sin configurar: sin esta fila el aviso del manifiesto "+
			"roto no se ve nunca", roto.Path, sv)
	}
}

// The default test tree is 6 rows and fits in a 30-row window, so without a taller tree the scroll clamp has nothing to correct.
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

func tecla(name string) tea.KeyMsg {
	return tea.KeyPressMsg{Code: rune(name[0]), Text: name}
}
