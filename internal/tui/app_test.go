package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// stubManager permite forzar resultados de Evaluate en tests del modelo.
type stubManager struct {
	eval func(process.EvalSpec) process.Status
}

func (s *stubManager) Start(spec process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}

func (s *stubManager) Stop(spec process.StopSpec) error { return nil }

func (s *stubManager) Evaluate(spec process.EvalSpec) process.Status {
	if s.eval != nil {
		return s.eval(spec)
	}
	return process.StatusStopped
}

var _ process.Manager = (*stubManager)(nil)

// newTestModel construye un árbol temporal con 3 proyectos:
// tienda-api (Go, group tienda, con repo git), tienda-web (JavaScript,
// group tienda) y suelto (Go, sin manifiesto). Devuelve el modelo y store.
func newTestModel(t *testing.T) (Model, *state.Store) {
	t.Helper()
	isolateConfig(t)
	store := state.NewStoreAt(t.TempDir())
	m := New(store, &stubManager{}, writeTestTree(t, false))
	m.width, m.height = 100, 30
	m.updateLayout()
	return m, store
}

// newJobsTestModel extiende el árbol base con comandos install/build en
// tienda-api y un mise.toml con tasks en tienda-web (0003 R26/R28).
func newJobsTestModel(t *testing.T) (Model, *state.Store) {
	t.Helper()
	isolateConfig(t)
	store := state.NewStoreAt(t.TempDir())
	m := New(store, &stubManager{}, writeTestTree(t, true))
	m.width, m.height = 100, 30
	m.updateLayout()
	return m, store
}

// isolateConfig apunta VROOM_CONFIG a un fichero inexistente para que
// los tests no lean la config real del usuario.
func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "absent-config.toml"))
}

// newTestModelWithConfig construye el modelo leyendo la config del
// contenido dado (para probar claves del config.toml, ej. el prefill
// de ask, spec 0005 R35).
func newTestModelWithConfig(t *testing.T, content string) (Model, *state.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VROOM_CONFIG", path)
	store := state.NewStoreAt(t.TempDir())
	m := New(store, &stubManager{}, writeTestTree(t, false))
	m.width, m.height = 100, 30
	m.updateLayout()
	return m, store
}

// fakeBin escribe un binario ejecutable falso en un directorio nuevo y
// devuelve el directorio (para usar como PATH).
func fakeBin(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// writeTestTree crea el árbol de proyectos de prueba; con jobs añade
// los campos one-shot del manifiesto y tasks de mise.
func writeTestTree(t *testing.T, jobs bool) string {
	t.Helper()
	root := t.TempDir()

	writeFile := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifestAPI := "name = \"tienda-api\"\ngroup = \"tienda\"\ncommand_start = \"go run main.go\"\nport = 8081\n"
	if jobs {
		manifestAPI += "command_install = \"echo installing\"\ncommand_build = \"echo building\"\n"
	}
	writeFile("tienda-api/go.mod", "module api\n")
	writeFile("tienda-api/.vroom.toml", manifestAPI)
	writeFile("tienda-api/.git/HEAD", "ref: refs/heads/main\n")
	writeFile("tienda-web/package.json", "{}\n")
	manifestWeb := "name = \"tienda-web\"\ngroup = \"tienda\"\ncommand_start = \"node server.js\"\nport = 5173\n"
	if jobs {
		manifestWeb += "command_install = \"echo installing web\"\ncommand_build = \"echo building web\"\n"
	}
	writeFile("tienda-web/.vroom.toml", manifestWeb)
	if jobs {
		writeFile("tienda-web/mise.toml", "[tasks.build]\ndescription = \"build the web\"\nrun = \"echo mise-build\"\n\n[tasks.test]\nrun = \"echo mise-test\"\n\n[tasks.hidden]\nhide = true\n")
	}
	writeFile("suelto/go.mod", "module suelto\n")
	return root
}

func press(m Model, key string) (Model, tea.Cmd) {
	var km tea.Msg
	switch key {
	case "enter":
		km = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		km = tea.KeyPressMsg{Code: tea.KeyTab}
	case "esc":
		km = tea.KeyPressMsg{Code: tea.KeyEsc}
	case "up":
		km = tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		km = tea.KeyPressMsg{Code: tea.KeyDown}
	case "pgup":
		km = tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		km = tea.KeyPressMsg{Code: tea.KeyPgDown}
	default:
		km = tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	}
	next, cmd := m.Update(km)
	return next.(Model), cmd
}

func findCursor(m Model, name string) int {
	for i, it := range m.tree {
		if it.kind == itemProject && it.project.Name == name {
			return i
		}
	}
	return -1
}

func findGroup(m Model, name string) int {
	for i, it := range m.tree {
		if it.kind == itemGroup && it.group == name {
			return i
		}
	}
	return -1
}

func moveCursorTo(t *testing.T, m Model, name string) Model {
	t.Helper()
	idx := findCursor(m, name)
	if idx < 0 {
		t.Fatalf("proyecto %s no encontrado", name)
	}
	m.cursor = idx
	return m
}

// pathOfSelected devuelve el path del proyecto bajo el cursor (falla si
// el cursor está sobre un grupo).
func pathOfSelected(t *testing.T, m Model) string {
	t.Helper()
	p := m.selected()
	if p == nil {
		t.Fatal("el cursor no está sobre un proyecto")
	}
	return p.Path
}

// S12.1: navegación cíclica sobre el árbol (grupos + proyectos, R24).
func TestNavigationCyclic(t *testing.T) {
	m, _ := newTestModel(t)
	if len(m.entries) != 3 {
		t.Fatalf("esperaba 3 proyectos, got %d", len(m.entries))
	}
	// árbol: grupo tienda + 2 miembros + suelto
	if len(m.tree) != 4 {
		t.Fatalf("esperaba 4 filas de árbol, got %d", len(m.tree))
	}

	m.cursor = len(m.tree) - 1
	m, _ = press(m, "j")
	if m.cursor != 0 {
		t.Errorf("al final, 'j' debe volver al inicio; cursor = %d", m.cursor)
	}
	m, _ = press(m, "k")
	if m.cursor != len(m.tree)-1 {
		t.Errorf("en el inicio, 'k' debe ir al final; cursor = %d", m.cursor)
	}
	m, _ = press(m, "down")
	m, _ = press(m, "up")
	if m.cursor != len(m.tree)-1 {
		t.Errorf("up/down deben comportarse como j/k; cursor = %d", m.cursor)
	}
}

// S11.2: toggle en proyecto sin manifiesto → mensaje, sin acción.
func TestToggleDisabledWithoutManifest(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "suelto")

	m2, cmd := press(m, "s")
	if cmd != nil {
		t.Error("no debe emitirse ningún comando sin manifiesto")
	}
	if !strings.Contains(m2.message, "No manifest") {
		t.Errorf("mensaje = %q, want mención de 'No manifest'", m2.message)
	}
}

// Toggle sobre running: SIEMPRE para (nunca doble start, S15.1).
func TestToggleRunningStops(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusRunning

	m2, cmd := press(m, "s")
	if cmd == nil {
		t.Fatal("toggle sobre running debe emitir stop")
	}
	if got := m2.services[path].Status; got != statusStopping {
		t.Errorf("estado = %s, want stopping", got)
	}
}

// Toggle sobre unknown: para el servicio dudoso.
func TestToggleUnknownStops(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusUnknown

	m2, cmd := press(m, "s")
	if cmd == nil {
		t.Fatal("toggle sobre unknown debe emitir stop")
	}
	if got := m2.services[path].Status; got != statusStopping {
		t.Errorf("estado = %s, want stopping", got)
	}
}

// S14.1: toggle sobre stopped → starting (transitorio) + comando de spawn.
func TestToggleStoppedStarts(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	m2, cmd := press(m, "s")
	if cmd == nil {
		t.Fatal("toggle debe emitir comando de start")
	}
	if got := m2.services[pathOfSelected(t, m2)].Status; got != statusStarting {
		t.Errorf("estado = %s, want starting", got)
	}
}

// Toggle ignorado mientras el servicio está en tránsito.
func TestToggleInTransitIgnored(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusStarting

	m2, cmd := press(m, "s")
	if cmd != nil {
		t.Error("toggle en tránsito no debe emitir comando")
	}
	if got := m2.services[path].Status; got != statusStarting {
		t.Errorf("estado = %s, want starting (sin cambio)", got)
	}
}

// S14.2: restart = stop → start encadenados.
func TestRestartChainsStopAndStart(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusRunning

	m2, cmd := press(m, "R")
	if cmd == nil {
		t.Fatal("restart debe emitir stop")
	}
	if got := m2.services[path].Status; got != statusStopping {
		t.Errorf("tras R: estado = %s, want stopping", got)
	}

	// Simular el stoppedMsg que devuelve stopCmd.
	next, cmd2 := m2.Update(stoppedMsg{path: path})
	m3 := next.(Model)
	if cmd2 == nil {
		t.Fatal("restart debe encadenar el start")
	}
	if got := m3.services[path].Status; got != statusStarting {
		t.Errorf("tras stop: estado = %s, want starting", got)
	}
}

func TestRestartRequiresRunning(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api") // stopped

	m2, cmd := press(m, "R")
	if cmd != nil {
		t.Error("restart de servicio detenido no debe emitir comando")
	}
	if !strings.Contains(m2.message, "running") {
		t.Errorf("mensaje = %q", m2.message)
	}
}

// R13/S13.1: el polling actualiza estados desde meta.json.
func TestRefreshUpdatesStatuses(t *testing.T) {
	m, store := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	if err := store.SaveMeta(path, state.Meta{
		Name: "tienda-api", ProjectPath: path, Command: "go run main.go",
		Pid: 4242, Pgid: 4242, CreationTimeMs: 1000, State: state.StateRunning,
	}); err != nil {
		t.Fatal(err)
	}

	next, _ := m.Update(refreshedMsg{results: map[string]refreshResult{
		path: {status: process.StatusRunning, meta: state.Meta{Pid: 4242}},
	}})
	m2 := next.(Model)
	if got := m2.services[path].Status; got != statusRunning {
		t.Errorf("estado = %s, want running tras refresh", got)
	}

	next, _ = m.Update(refreshedMsg{results: map[string]refreshResult{
		path: {status: process.StatusStopped},
	}})
	m2 = next.(Model)
	if got := m2.services[path].Status; got != statusStopped {
		t.Errorf("estado = %s, want stopped (servicio crasheado)", got)
	}
}

// S5.1: meta corrupto → stopped + warning visible, sin crashear.
func TestRefreshWithCorruptMeta(t *testing.T) {
	m, store := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	if err := os.MkdirAll(store.ServiceDir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.ServiceDir(path), "meta.json"), []byte("{bad"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := refreshCmd(store, &stubManager{}, m.projects)
	msg := cmd() // ejecutar el comando directamente
	rm, ok := msg.(refreshedMsg)
	if !ok {
		t.Fatalf("msg inesperado: %T", msg)
	}
	if got := rm.results[path].status; got != process.StatusStopped {
		t.Errorf("estado = %s, want stopped", got)
	}
	if rm.results[path].warn == "" {
		t.Error("debe incluir warning para meta corrupto")
	}
}

// S9.3: unknown se mapea a estado unknown con indicador en el dashboard.
func TestRefreshMapsUnknown(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	next, _ := m.Update(refreshedMsg{results: map[string]refreshResult{
		path: {status: process.StatusUnknown},
	}})
	m2 := next.(Model)
	if got := m2.services[path].Status; got != statusUnknown {
		t.Errorf("estado = %s, want unknown", got)
	}
	view := m2.renderDashboard()
	if !strings.Contains(view, "unknown") {
		t.Error("el dashboard debe mostrar unknown (badge del detalle)")
	}
}

// S7.2: al refrescar con meta en disco el modelo re-adjunta el estado.
func TestReattachFromDisk(t *testing.T) {
	m, store := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	if err := store.SaveMeta(path, state.Meta{
		Name: "tienda-api", ProjectPath: path, Command: "go run main.go",
		Pid: 4242, Pgid: 4242, CreationTimeMs: 1000, State: state.StateRunning,
	}); err != nil {
		t.Fatal(err)
	}

	cmd := refreshCmd(store, &stubManager{eval: func(process.EvalSpec) process.Status {
		return process.StatusRunning
	}}, m.projects)
	rm := cmd().(refreshedMsg)

	next, _ := m.Update(rm)
	m2 := next.(Model)
	if got := m2.services[path].Status; got != statusRunning {
		t.Errorf("re-adjunta: estado = %s, want running", got)
	}
}

func TestTickReschedules(t *testing.T) {
	m, _ := newTestModel(t)
	next, cmd := m.Update(tickMsg(time.Now()))
	m2 := next.(Model)
	if cmd == nil {
		t.Fatal("tick debe re-programar el siguiente ciclo")
	}
	_ = m2
}

// ---- Dashboard (0002 R18) ----

// 0004 R30: el panel de detalles es fijo (sin toggle); `d` no existe y
// esc sale directamente.
func TestDetailsAlwaysVisible(t *testing.T) {
	m, _ := newTestModel(t)
	if !m.detailsShown {
		t.Fatal("el panel de detalles debe estar siempre visible si cabe")
	}

	m2, cmd := press(m, "d")
	if cmd != nil || !m2.detailsShown {
		t.Error("d ya no existe: no debe hacer nada")
	}
	m3, cmd2 := press(m2, "esc")
	if cmd2 == nil {
		t.Error("esc debe salir de la TUI (no hay panel que cerrar)")
	}
	if !m3.detailsShown {
		t.Error("esc no debe ocultar el panel de detalles")
	}
}

// R24: enter sobre un servicio no hace nada.
func TestEnterOnServiceNoop(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, cmd := press(m, "enter")
	if cmd != nil {
		t.Error("enter sobre servicio no debe emitir comando")
	}
	if m2.cursor != m.cursor || len(m2.tree) != len(m.tree) {
		t.Error("enter sobre servicio no debe cambiar el árbol")
	}
}

// S18.3: en anchos mínimos el detalle se auto-oculta aunque esté abierto.
func TestDetailsAutoHideNarrow(t *testing.T) {
	m, _ := newTestModel(t)
	m.width = 50
	m.updateLayout()
	if m.detailsShown {
		t.Error("con ancho 50 el detalle no debe mostrarse")
	}
	out := m.renderDashboard()
	if strings.Contains(out, "language:") {
		t.Error("el panel de detalles no debe renderizarse en ancho mínimo")
	}
	if !strings.Contains(out, "tienda-api") {
		t.Error("el árbol debe seguir visible en ancho mínimo")
	}
}

// S18.4: la pestaña activa se renderiza con estilo distinto.
func TestActiveTabMarked(t *testing.T) {
	if tabLabel(tabConsole, true) == tabLabel(tabConsole, false) {
		t.Error("la pestaña activa debe renderizarse distinto a la inactiva")
	}
	if !strings.Contains(tabLabel(tabThreads, true), "[2] Threads") {
		t.Error("la etiqueta de la pestaña debe conservar su texto")
	}
}

// S18.5: el árbol hace scroll automático al navegar más allá de lo visible.
func TestTreeAutoScroll(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 12; i++ {
		dir := filepath.Join(root, "svc"+string(rune('a'+i)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 10 // bodyH = 7
	m.updateLayout()
	if m.bodyH != 7 {
		t.Fatalf("bodyH = %d, want 7", m.bodyH)
	}

	m.cursor = len(m.entries) - 1
	_, cl := m.treeLines()
	m.treeTop = cl - m.bodyH + 1 // simular posición tras navegar al final
	if m.treeTop < 0 {
		m.treeTop = 0
	}

	// k desde el índice 11 → 10: el cursor sigue dentro de la ventana.
	m2, _ := press(m, "k")
	tree, cl2 := m2.treeLines()
	if cl2 < m2.treeTop || cl2 >= m2.treeTop+m2.bodyH {
		t.Errorf("cursor fuera de la ventana: cl=%d top=%d bodyH=%d total=%d",
			cl2, m2.treeTop, m2.bodyH, len(tree))
	}
	// Volver al inicio: el cursor 0 fuerza treeTop a 0 (S18.5).
	m3 := m2
	for i := 0; i < 10; i++ {
		m3, _ = press(m3, "k")
	}
	if m3.cursor != 0 || m3.treeTop != 0 {
		t.Errorf("cursor=%d treeTop=%d, want 0/0 tras volver arriba", m3.cursor, m3.treeTop)
	}
}

// S18.1: el dashboard muestra árbol (dots/nombres), header de grupo y
// pestañas.
func TestRenderDashboard(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.services[pathOfSelected(t, m)].Status = statusRunning

	out := m.View().Content
	if !strings.Contains(out, "vroom — projects in") {
		t.Error("falta título")
	}
	for _, want := range []string{"tienda-api", "tienda-web", "suelto", "▾ tienda", "[1] Console", "[2] Threads"} {
		if !strings.Contains(out, want) {
			t.Errorf("el dashboard no contiene %q", want)
		}
	}
	// El panel de detalles (abierto por defecto) muestra los campos base.
	for _, want := range []string{"language:", "branch:", "main", "start:"} {
		if !strings.Contains(out, want) {
			t.Errorf("el panel de detalles no contiene %q", want)
		}
	}
}

// R21/S21.1: la rama git aparece en el panel de detalles.
func TestDetailsShowsBranch(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	lines := m.detailsLines(m.rightW)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "branch:") || !strings.Contains(joined, "main") {
		t.Errorf("detalle sin rama git: %q", joined)
	}
	// S21.4: proyecto sin repo no muestra la fila.
	m = moveCursorTo(t, m, "suelto")
	for _, l := range m.detailsLines(m.rightW) {
		if strings.Contains(l, "branch:") {
			t.Error("proyecto sin repo no debe mostrar branch")
		}
	}
}

// El panel de detalles muestra los 4 comandos del manifiesto en la
// columna derecha; los no configurados aparecen atenuados.
func TestDetailsShowsCommands(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-web")
	joined := strings.Join(m.detailsLines(m.rightW), "\n")
	for _, want := range []string{"start:", "node server.js", "install:", "echo installing web", "build:", "echo building web"} {
		if !strings.Contains(joined, want) {
			t.Errorf("detalles sin %q: %q", want, joined)
		}
	}
	if !strings.Contains(joined, "stop:") {
		t.Errorf("detalles sin la fila stop: %q", joined)
	}
	// La fila stop sin configurar no debe filtrar el valor de otro campo.
	if strings.Contains(joined, "docker") {
		t.Errorf("stop sin configurar no debe mostrar valor: %q", joined)
	}
}

// ---- Consola (0002 R19/R23) ----

// S19.1: append incremental sin resetear contenido previo.
func TestConsoleIncrementalAppend(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	next, _ := m.Update(consoleDeltaMsg{path: path, stdout: "line1\n", offS: 6})
	m1 := next.(Model)
	if got := m1.consoleStateFor(path).merged; got != "line1\n" {
		t.Errorf("merged = %q", got)
	}

	next, _ = m1.Update(consoleDeltaMsg{path: path, stdout: "line2\n", offS: 12})
	m2 := next.(Model)
	if got := m2.consoleStateFor(path).merged; got != "line1\nline2\n" {
		t.Errorf("merged tras segundo delta = %q, want append sin reset", got)
	}
	if !strings.Contains(m2.consoleView.View(), "line2") {
		t.Error("el viewport debe contener el contenido nuevo")
	}
}

// S23.1: merged intercala ambos deltas sin duplicar bytes.
func TestConsoleMergedNoDuplicates(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	next, _ := m.Update(consoleDeltaMsg{path: path, stdout: "a\n", stderr: "b\n", offS: 2, offE: 2})
	m1 := next.(Model)
	cs := m1.consoleStateFor(path)
	if cs.merged != "a\nb\n" {
		t.Errorf("merged = %q, want a\\nb\\n", cs.merged)
	}
	if cs.stdout != "a\n" || cs.stderr != "b\n" {
		t.Errorf("streams aislados: stdout=%q stderr=%q", cs.stdout, cs.stderr)
	}
}

// S19.2: navegar entre servicios conserva los buffers y offsets.
func TestConsoleBuffersSurviveNavigation(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	pathA := pathOfSelected(t, m)

	next, _ := m.Update(consoleDeltaMsg{path: pathA, stdout: "histórico\n", offS: 10})
	m1 := next.(Model)

	m2, _ := press(m1, "j") // → tienda-web
	pathB := pathOfSelected(t, m2)
	next, _ = m2.Update(consoleDeltaMsg{path: pathB, stdout: "web\n", offS: 4})
	m3 := next.(Model)

	m4, _ := press(m3, "k") // de vuelta a tienda-api
	if got := m4.consoleStateFor(pathA).merged; got != "histórico\n" {
		t.Errorf("buffer de A perdido: %q", got)
	}
	if !strings.Contains(m4.consoleView.View(), "histórico") {
		t.Error("al volver a A el viewport debe mostrar su buffer")
	}
	if got := m4.consoleStateFor(pathB).merged; got != "web\n" {
		t.Errorf("buffer de B perdido: %q", got)
	}
}

// S19.4: scroll pausa el follow; G lo reactiva.
func TestConsoleFollowPause(t *testing.T) {
	m, _ := newTestModel(t)
	if !m.consoleFollow {
		t.Fatal("follow debe iniciar activo")
	}
	m2, _ := press(m, "pgup")
	if m2.consoleFollow {
		t.Error("pgup debe pausar el follow")
	}
	m3, _ := press(m2, "G")
	if !m3.consoleFollow {
		t.Error("G debe reactivar el follow")
	}
}

// pgup y pgdown hacen scroll real de la consola (S19.4).
func TestConsolePageScroll(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.consoleView.SetHeight(5) // antes de setConsoleContent: GotoBottom respeta el alto
	m.setConsoleContent(strings.Repeat("line\n", 100))

	m2, _ := press(m, "pgup")
	if m2.consoleView.YOffset() == 0 {
		t.Error("pgup debe desplazar la vista")
	}
	if m2.consoleFollow {
		t.Error("pgup debe pausar el follow")
	}
	m3, _ := press(m2, "pgdown")
	if !m3.consoleView.AtBottom() {
		t.Errorf("pgdown debe desplazar la vista hacia abajo: yOffset=%d", m3.consoleView.YOffset())
	}
}

// La rueda del mouse hace scroll de la consola: arriba pausa el follow,
// abajo hasta el final lo reactiva (S19.4).
func TestMouseWheelScroll(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.consoleView.SetHeight(5) // antes de setConsoleContent: GotoBottom respeta el alto
	m.setConsoleContent(strings.Repeat("line\n", 100))

	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m2 := next.(Model)
	if m2.consoleFollow {
		t.Error("rueda arriba debe pausar el follow")
	}
	if m2.consoleView.YOffset() == 0 {
		t.Error("rueda arriba debe desplazar la vista")
	}

	// Vuelta al final: la vista debe quedar pegada abajo y con follow.
	for i := 0; i < 100; i++ {
		next, _ = m2.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m2 = next.(Model)
	}
	if !m2.consoleView.AtBottom() || m2.consoleView.YOffset() == 0 {
		t.Errorf("rueda abajo debe llevar al final: yOffset=%d", m2.consoleView.YOffset())
	}
	if !m2.consoleFollow {
		t.Error("rueda abajo hasta el final debe reactivar el follow")
	}

	// Rueda fuera de la consola: ignorada (nada cambia).
	before := m2.consoleView.YOffset()
	m4 := m2
	m4.activeTab = tabThreads
	next, _ = m4.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m5 := next.(Model)
	if !m5.consoleFollow || m5.consoleView.YOffset() != before {
		t.Error("la rueda en Threads no debe tocar la consola")
	}
}

// S19.3 + 0003 R29: c cicla merged → stdout → stderr → merged (antes t,
// ahora reservada para el picker de tasks).
func TestToggleStreamMode(t *testing.T) {
	m, _ := newTestModel(t)
	m2, _ := press(m, "c")
	if m2.stream != streamStdout {
		t.Errorf("tras 1er c: %v, want stdout", m2.stream)
	}
	m3, _ := press(m2, "c")
	if m3.stream != streamStderr {
		t.Errorf("tras 2do c: %v, want stderr", m3.stream)
	}
	m4, _ := press(m3, "c")
	if m4.stream != streamMerged {
		t.Errorf("tras 3er c: %v, want merged", m4.stream)
	}
}

// Buffer con cap (S19.6) a nivel de modelo: los buffers nunca crecen
// infinito aunque el delta sea grande.
func TestConsoleBufferCapped(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	big := strings.Repeat("x", 300*1024) + "\nend\n"
	next, _ := m.Update(consoleDeltaMsg{path: path, stdout: big, offS: int64(len(big))})
	m1 := next.(Model)
	if got := len(m1.consoleStateFor(path).stdout); got > maxConsoleBytes {
		t.Errorf("buffer = %d bytes, cap = %d", got, maxConsoleBytes)
	}
	if !strings.Contains(m1.consoleStateFor(path).stdout, "end\n") {
		t.Error("el cap debe conservar el contenido reciente")
	}
}

// S16.2 heredado: buffer vacío muestra placeholder en el viewport.
func TestEmptyConsoleShowsPlaceholder(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.setConsoleContent("")
	if !strings.Contains(m.consoleView.View(), "No logs available") {
		t.Errorf("viewport = %q", m.consoleView.View())
	}
}

// offBy reporta si got se desvía de want más allá de la tolerancia (el
// delta de tiempo real entre muestras introduce una desviación mínima).
func offBy(got, want float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d > 0.5
}

// Integración end-to-end del pipeline de consola (S19.1): tail real
// sobre ficheros en disco, dos ticks, solo bytes nuevos.
func TestConsoleTailPipelineRealFiles(t *testing.T) {
	m, store := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}
	stdoutLog := store.StdoutLog(path)

	// Primer tick de consola: fichero con contenido inicial.
	if err := os.WriteFile(stdoutLog, []byte("boot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg1 := m.tailCmd()().(consoleDeltaMsg)
	next, _ := m.Update(msg1)
	m1 := next.(Model)
	if got := m1.consoleStateFor(path).merged; got != "boot\n" {
		t.Errorf("merged = %q, want boot\\n", got)
	}

	// El servicio escribe más y llega el segundo tick: solo el delta.
	f, err := os.OpenFile(stdoutLog, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("ready \x1b[32mOK\x1b[0m\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	msg2 := m1.tailCmd()().(consoleDeltaMsg)
	next, _ = m1.Update(msg2)
	m2 := next.(Model)
	if got := m2.consoleStateFor(path).merged; got != "boot\nready OK\n" {
		t.Errorf("merged = %q, want boot\\nready OK\\n (delta + strip ANSI)", got)
	}
}

// ---- Pestañas (S22: 1/2/tab) ----

func TestTabSwitching(t *testing.T) {
	m, _ := newTestModel(t)
	if m.activeTab != tabConsole {
		t.Fatal("pestaña inicial = Console")
	}
	m2, _ := press(m, "2")
	if m2.activeTab != tabThreads {
		t.Error("tecla 2 debe activar Threads")
	}
	m3, _ := press(m2, "tab")
	if m3.activeTab != tabConsole {
		t.Error("tab debe ciclar a Console")
	}
	m4, _ := press(m3, "1")
	if m4.activeTab != tabConsole {
		t.Error("tecla 1 debe activar Console")
	}
}

// ---- Threads (0002 R20) ----

// S20.4: servicio no running → placeholder.
func TestThreadsPlaceholderNotRunning(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	lines := m.threadsLines(m.rightW, m.contentH)
	if len(lines) != 1 || !strings.Contains(lines[0], "service not running") {
		t.Errorf("lines = %v", lines)
	}
	m = moveCursorTo(t, m, "suelto")
	lines = m.threadsLines(m.rightW, m.contentH)
	if len(lines) != 1 || !strings.Contains(lines[0], "No manifest") {
		t.Errorf("lines = %v", lines)
	}
}

// S20.1/S20.2/S20.3: tabla ordenada por CPU desc, con delta entre
// muestras (100 ticks en 2s = 50%).
func TestThreadsTableAndCPU(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusRunning
	m.services[path].Meta.Pid = 4242

	// Primera muestra: CPU 0.
	next, _ := m.Update(threadsMsg{path: path, threads: []process.ThreadInfo{
		{TID: 1, Name: "main", State: "R", Ticks: 100},
		{TID: 2, Name: "gc", State: "S", Ticks: 0},
	}})
	m1 := next.(Model)
	rows := m1.threads[path]
	if len(rows) != 2 || rows[0].CPU != 0 || rows[1].CPU != 0 {
		t.Fatalf("primera muestra: %+v", rows)
	}

	// Segunda muestra: main +100 ticks en 2s → 50%.
	prev := m1.threadPrev[path]
	prev.at = time.Now().Add(-2 * time.Second)
	next, _ = m1.Update(threadsMsg{path: path, threads: []process.ThreadInfo{
		{TID: 1, Name: "main", State: "R", Ticks: 200},
		{TID: 2, Name: "gc", State: "S", Ticks: 10},
	}})
	m2 := next.(Model)
	rows = m2.threads[path]
	if rows[0].TID != 1 || offBy(rows[0].CPU, 50) {
		t.Errorf("orden/CPU: %+v", rows)
	}
	if rows[1].TID != 2 || offBy(rows[1].CPU, 5) {
		t.Errorf("segundo hilo: %+v", rows)
	}

	out := strings.Join(m2.threadsLines(m2.rightW, m2.contentH), "\n")
	for _, want := range []string{"main", "gc", "TID", "CPU%"} {
		if !strings.Contains(out, want) {
			t.Errorf("tabla sin %q: %q", want, out)
		}
	}
}

// S20.5: error de muestreo (proceso muerto) → sin filas ni crash.
func TestThreadsSamplingError(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusRunning
	m.services[path].Meta.Pid = 4242

	next, _ := m.Update(threadsMsg{path: path, err: os.ErrNotExist})
	m1 := next.(Model)
	if got := m1.threads[path]; got != nil {
		t.Errorf("rows = %v, want nil", got)
	}
}

// ---- Help / misceláneos ----

// Help responsive: completa en ancho amplio, compacta y truncada en estrecho.
func TestHelpResponsive(t *testing.T) {
	full := dashboardHelp(200)
	if !strings.Contains(full, "start/stop") || !strings.Contains(full, "refresh") {
		t.Errorf("help completa inesperada: %q", full)
	}
	narrow := dashboardHelp(40)
	if strings.Contains(narrow, "refresh") {
		t.Errorf("en estrecho no debe caber refresh: %q", narrow)
	}
	if utf8.RuneCountInString(narrow) > 40 {
		t.Errorf("help no truncada al ancho: %d runes", utf8.RuneCountInString(narrow))
	}
}

// El badge muestra el puerto junto a running/unknown.
func TestBadgeShowsPort(t *testing.T) {
	p := scanner.Project{
		Path: "/tmp/x", Name: "x", Language: "Go",
		Configured: true,
		Manifest:   &manifest.Manifest{Name: "x", Command: "run", Port: 8081},
	}
	sv := &ServiceState{Status: statusRunning}
	if badge := statusBadge(p, sv); !strings.Contains(badge, ":8081") {
		t.Errorf("badge running sin puerto: %q", badge)
	}
	sv.Status = statusUnknown
	if badge := statusBadge(p, sv); !strings.Contains(badge, ":8081") {
		t.Errorf("badge unknown sin puerto: %q", badge)
	}
	sv.Status = statusStopped
	if badge := statusBadge(p, sv); strings.Contains(badge, ":8081") {
		t.Errorf("badge stopped no debe mostrar puerto: %q", badge)
	}
	// Sin manifiesto: nunca puerto
	p2 := scanner.Project{Path: "/tmp/y", Name: "y", Language: "Go"}
	if badge := statusBadge(p2, &ServiceState{Status: statusUnconfigured}); strings.Contains(badge, ":") {
		t.Errorf("badge unconfigured con puerto: %q", badge)
	}
}

// R17 heredado: `l`/`o` compone el editor correctamente (nvim -O, foco
// según el stream activo, editores no-vim sin -O).
func TestBuildEditorCmd(t *testing.T) {
	stdoutLog := "/state/services/abc/stdout.log"
	stderrLog := "/state/services/abc/stderr.log"

	// nvim default con -O, stdout primero
	cmd := buildEditorCmd("nvim", stdoutLog, stderrLog, false)
	want := []string{"nvim", "-O", stdoutLog, stderrLog}
	if !equalArgs(cmd.Args, want) {
		t.Errorf("args = %v, want %v", cmd.Args, want)
	}

	// foco en stderr: stderr primero
	cmd = buildEditorCmd("nvim", stdoutLog, stderrLog, true)
	want = []string{"nvim", "-O", stderrLog, stdoutLog}
	if !equalArgs(cmd.Args, want) {
		t.Errorf("args = %v, want %v", cmd.Args, want)
	}

	// vim también recibe -O
	cmd = buildEditorCmd("vim", stdoutLog, stderrLog, false)
	if !equalArgs(cmd.Args, []string{"vim", "-O", stdoutLog, stderrLog}) {
		t.Errorf("args = %v", cmd.Args)
	}

	// editor no-vim: sin -O, con argumentos propios respetados
	cmd = buildEditorCmd("code -w", stdoutLog, stderrLog, false)
	if !equalArgs(cmd.Args, []string{"code", "-w", stdoutLog, stderrLog}) {
		t.Errorf("args = %v", cmd.Args)
	}
}

func equalArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestResolveEditor(t *testing.T) {
	t.Setenv("VISUAL", "code -w")
	t.Setenv("EDITOR", "nano")
	if got := resolveEditor(); got != "code -w" {
		t.Errorf("VISUAL debe tener prioridad, got %q", got)
	}
	t.Setenv("VISUAL", "")
	if got := resolveEditor(); got != "nano" {
		t.Errorf("EDITOR secundario, got %q", got)
	}
	t.Setenv("EDITOR", "")
	if got := resolveEditor(); got != "nvim" {
		t.Errorf("default nvim, got %q", got)
	}
}

// La ayuda menciona `l logfile`, las pestañas, el colapso de grupos y
// las acciones de 0003/0004; `d` (toggle) desapareció.
func TestHelpWording(t *testing.T) {
	full := dashboardHelp(200)
	for _, want := range []string{"l logfile", "1/2 tabs", "enter collapse", "b build", "i install", "t tasks", "a ask", "C clear"} {
		if !strings.Contains(full, want) {
			t.Errorf("help sin %q: %q", want, full)
		}
	}
	if strings.Contains(full, "d info") {
		t.Error("help no debe mencionar d info (toggle eliminado)")
	}
}

// ---- Grupos seleccionables y colapsables (0002 R24) ----

// Seleccionar un grupo y colapsarlo con enter: los miembros se ocultan
// y el header muestra running/total; enter de nuevo los expande.
func TestGroupCollapse(t *testing.T) {
	m, _ := newTestModel(t)
	gi := findGroup(m, "tienda")
	if gi < 0 {
		t.Fatal("grupo tienda no encontrado en el árbol")
	}
	m.cursor = gi
	// tienda-api running → header colapsado "tienda (1/2)".
	m.services[pathOfSelectedNamed(t, m, "tienda-api")].Status = statusRunning

	m2, cmd := press(m, "enter")
	if cmd != nil {
		t.Error("colapsar no debe emitir comandos")
	}
	if !m2.collapsed["tienda"] {
		t.Fatal("enter sobre grupo debe colapsarlo")
	}
	if len(m2.tree) != 2 { // header tienda + suelto
		t.Errorf("árbol colapsado: %d filas, want 2", len(m2.tree))
	}
	if m2.cursor != gi {
		t.Errorf("cursor = %d, want %d (el header conserva su índice)", m2.cursor, gi)
	}
	tree, _ := m2.treeLines()
	treeText := strings.Join(tree, "\n")
	if !strings.Contains(treeText, "tienda (1/2)") {
		t.Errorf("header colapsado sin conteo: %q", treeText)
	}
	if strings.Contains(treeText, "tienda-web") || strings.Contains(treeText, "tienda-api") {
		t.Errorf("los miembros del grupo colapsado no deben verse en el árbol: %q", treeText)
	}
	// El panel de detalles sí lista los miembros (peek del grupo).
	if !strings.Contains(m2.renderDashboard(), "tienda-web") {
		t.Error("el panel de info debe listar los miembros del grupo colapsado")
	}

	m3, _ := press(m2, "enter")
	if m3.collapsed["tienda"] {
		t.Error("segundo enter debe expandir el grupo")
	}
	treeExp, _ := m3.treeLines()
	treeExpText := strings.Join(treeExp, "\n")
	if len(m3.tree) != 4 || !strings.Contains(treeExpText, "▾ tienda") || strings.Contains(treeExpText, "(1/2)") {
		t.Errorf("árbol expandido debe mostrar miembros sin conteo: %q", treeExpText)
	}
}

// s sobre un grupo: si hay parados arranca los parados; si no, para los
// running (toggle de grupo, R24).
func TestGroupToggleAll(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = findGroup(m, "tienda")

	// Ambos stopped → start de ambos.
	m2, cmd := press(m, "s")
	if cmd == nil {
		t.Fatal("s sobre grupo con parados debe emitir starts")
	}
	for _, name := range []string{"tienda-api", "tienda-web"} {
		sv := m2.services[pathOfSelectedNamed(t, m2, name)]
		if sv.Status != statusStarting {
			t.Errorf("%s: estado = %s, want starting", name, sv.Status)
		}
	}

	// Ambos running → stop de ambos.
	for _, name := range []string{"tienda-api", "tienda-web"} {
		m2.services[pathOfSelectedNamed(t, m2, name)].Status = statusRunning
	}
	m3, cmd2 := press(m2, "s")
	if cmd2 == nil {
		t.Fatal("s sobre grupo corriendo debe emitir stops")
	}
	for _, name := range []string{"tienda-api", "tienda-web"} {
		sv := m3.services[pathOfSelectedNamed(t, m3, name)]
		if sv.Status != statusStopping {
			t.Errorf("%s: estado = %s, want stopping", name, sv.Status)
		}
	}
}

// Panel de info sobre grupo: resumen con conteo y miembros.
func TestGroupDetails(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = findGroup(m, "tienda")
	m.services[pathOfSelectedNamed(t, m, "tienda-api")].Status = statusRunning

	lines := m.detailsLines(m.rightW)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"tienda (1/2)", "services:", "running:", "tienda-api", "tienda-web"} {
		if !strings.Contains(joined, want) {
			t.Errorf("detalle de grupo sin %q: %q", want, joined)
		}
	}
}

// Consola con grupo seleccionado: placeholder, no logs.
func TestGroupConsolePlaceholder(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = findGroup(m, "tienda")
	next, _ := m.onSelect()
	m2 := next.(Model)
	if !strings.Contains(m2.consoleView.View(), "group selected") {
		t.Errorf("viewport = %q, want placeholder de grupo", m2.consoleView.View())
	}
	if m2.tailCmd() != nil {
		t.Error("con grupo seleccionado no debe tailear logs")
	}
}

func pathOfSelectedNamed(t *testing.T, m Model, name string) string {
	t.Helper()
	idx := findCursor(m, name)
	if idx < 0 {
		t.Fatalf("proyecto %s no encontrado", name)
	}
	return m.tree[idx].project.Path
}

// ---- Jobs one-shot (0003 R26/R27) ----

// b lanza el comando build del manifiesto: marca el job, ejecuta,
// libera el bloqueo y notifica el resultado; la salida (con banner)
// queda en stdout.log visible en la consola.
func TestBuildKey(t *testing.T) {
	m, store := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	m2, cmd := press(m, "b")
	if cmd == nil {
		t.Fatal("b debe lanzar el job de build")
	}
	if got := m2.jobs[path]; got != "build" {
		t.Errorf("jobs[%s] = %q, want build", path, got)
	}

	msg := cmd()
	jm, ok := msg.(jobMsg)
	if !ok {
		t.Fatalf("msg = %T, want jobMsg", msg)
	}
	if jm.exitCode != 0 || jm.err != nil {
		t.Errorf("jobMsg = %+v, want exit 0", jm)
	}

	next, _ := m2.Update(msg)
	m3 := next.(Model)
	if m3.jobs[path] != "" {
		t.Error("jobMsg debe liberar el bloqueo del proyecto")
	}
	if !strings.Contains(m3.message, "build ok") {
		t.Errorf("message = %q, want build ok", m3.message)
	}

	data, err := os.ReadFile(store.StdoutLog(path))
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	for _, want := range []string{"── vroom ▶ build: echo building", "── vroom ✓ build ok"} {
		if !strings.Contains(log, want) {
			t.Errorf("stdout.log sin %q: %q", want, log)
		}
	}
}

// Sin campo build → notifica, no lanza nada.
func TestBuildKeyWithoutCommand(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, cmd := press(m, "b")
	if cmd != nil {
		t.Error("sin build no debe lanzar job")
	}
	if !strings.Contains(m2.message, "no build command") {
		t.Errorf("message = %q, want no build command", m2.message)
	}
}

// i lanza el comando install del manifiesto; sin install → notify.
func TestInstallKey(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, cmd := press(m, "i")
	if cmd == nil {
		t.Fatal("i debe lanzar el job de install")
	}
	if got := m2.jobs[pathOfSelected(t, m2)]; got != "install" {
		t.Errorf("jobs = %q, want install", got)
	}
	msg := cmd()
	if jm := msg.(jobMsg); jm.exitCode != 0 {
		t.Errorf("jobMsg = %+v, want exit 0", jm)
	}

	// Sin install configurado → notify.
	m4, _ := newTestModel(t)
	m4 = moveCursorTo(t, m4, "tienda-web")
	m5, cmd2 := press(m4, "i")
	if cmd2 != nil {
		t.Error("sin install no debe lanzar job")
	}
	if !strings.Contains(m5.message, "no install command") {
		t.Errorf("message = %q, want no install command", m5.message)
	}
}

// Con un job en curso sobre el proyecto, otra acción one-shot se
// bloquea con notificación (R27); otros proyectos siguen libres.
func TestJobBusyBlock(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "b") // build en curso

	m3, cmd2 := press(m2, "i")
	if cmd2 != nil {
		t.Error("con job en curso no debe lanzar otro")
	}
	if !strings.Contains(m3.message, "already running") {
		t.Errorf("message = %q, want already running", m3.message)
	}
	if m3.jobs[pathOfSelected(t, m3)] != "build" {
		t.Error("el job original debe seguir marcado")
	}

	// Otro proyecto puede lanzar el suyo en paralelo.
	m4 := moveCursorTo(t, m3, "tienda-web")
	m5, cmd3 := press(m4, "b")
	if cmd3 == nil {
		t.Error("otro proyecto debe poder lanzar build en paralelo")
	}
	_ = m5
}

// b/i/t sobre grupo o unconfigured notifican en vez de fallar.
func TestJobsGuards(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m.cursor = findGroup(m, "tienda")
	m2, cmd := press(m, "b")
	if cmd != nil || !strings.Contains(m2.message, "select a service") {
		t.Errorf("b sobre grupo: cmd=%v msg=%q", cmd, m2.message)
	}

	m3, _ := newJobsTestModel(t)
	m3 = moveCursorTo(t, m3, "suelto")
	m4, cmd2 := press(m3, "i")
	if cmd2 != nil || !strings.Contains(m4.message, "No manifest") {
		t.Errorf("i sobre unconfigured: cmd=%v msg=%q", cmd2, m4.message)
	}
}

// jobCmd registra el exit code y los banners de inicio/fin en el log.
func TestJobCmdFailure(t *testing.T) {
	dir := t.TempDir()
	stdout := filepath.Join(dir, "stdout.log")
	stderr := filepath.Join(dir, "stderr.log")

	msg := jobCmd("p", "build", "echo boom && exit 3", dir, stdout, stderr)()
	jm := msg.(jobMsg)
	if jm.exitCode != 3 {
		t.Errorf("exitCode = %d, want 3", jm.exitCode)
	}
	if jm.err != nil {
		t.Errorf("err = %v, want nil (exit code conocido)", jm.err)
	}

	data, err := os.ReadFile(stdout)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	for _, want := range []string{"── vroom ▶ build: echo boom && exit 3", "✗ build failed (exit 3"} {
		if !strings.Contains(log, want) {
			t.Errorf("stdout.log sin %q: %q", want, log)
		}
	}
}

// ---- Picker de tasks de mise (0003 R28) ----

// t abre el modal con los tasks ordenados (sin los hide); j/k navegan;
// enter cierra y lanza `mise run <task>` como job; el render muestra
// título, cursor y hint de teclas.
func TestPickerOpenAndRun(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-web")

	m2, _ := press(m, "t")
	if !m2.pickerOpen {
		t.Fatalf("t debe abrir el picker (msg=%q)", m2.message)
	}
	if len(m2.pickerItems) != 2 {
		t.Errorf("items = %d, want 2 (hidden excluido): %+v", len(m2.pickerItems), m2.pickerItems)
	}
	if m2.pickerItems[0].Name != "build" || m2.pickerItems[1].Name != "test" {
		t.Errorf("orden alfabético roto: %+v", m2.pickerItems)
	}
	out := m2.View().Content
	for _, want := range []string{"tasks — tienda-web", "▶ build", "j/k select · enter run · esc close"} {
		if !strings.Contains(out, want) {
			t.Errorf("render del picker sin %q", want)
		}
	}

	m3, _ := press(m2, "j")
	if m3.pickerCursor != 1 {
		t.Errorf("cursor = %d, want 1", m3.pickerCursor)
	}
	m4, cmd := press(m3, "enter")
	if m4.pickerOpen {
		t.Error("enter debe cerrar el picker")
	}
	if cmd == nil {
		t.Fatal("enter debe lanzar el job task")
	}
	if got := m4.jobs[pathOfSelected(t, m4)]; got != "task" {
		t.Errorf("jobs = %q, want task", got)
	}
}

// esc cierra el modal sin salir de la TUI; otras teclas se ignoran
// (modal).
func TestPickerModal(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-web")
	m2, _ := press(m, "t")

	m3, cmd := press(m2, "s") // el modal debe tragar teclas ajenas
	if cmd != nil || m3.services[pathOfSelected(t, m3)].Status != statusStopped {
		t.Error("el modal debe ignorar s (sin toggle de servicio)")
	}
	if !m3.pickerOpen {
		t.Error("el modal debe seguir abierto")
	}

	m4, cmd2 := press(m3, "esc")
	if m4.pickerOpen {
		t.Error("esc debe cerrar el picker")
	}
	if cmd2 != nil {
		t.Error("esc con picker NO debe salir de la TUI")
	}
}

// Sin mise.toml → notifica; unconfigured → notify de manifiesto.
func TestPickerGuards(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "t")
	if m2.pickerOpen || !strings.Contains(m2.message, "no mise.toml") {
		t.Errorf("sin mise.toml: open=%v msg=%q", m2.pickerOpen, m2.message)
	}

	m3, _ := newJobsTestModel(t)
	m3 = moveCursorTo(t, m3, "suelto")
	m4, _ := press(m3, "t")
	if m4.pickerOpen || !strings.Contains(m4.message, "No manifest") {
		t.Errorf("unconfigured: open=%v msg=%q", m4.pickerOpen, m4.message)
	}
}

// mise.toml con sección [tasks] vacía → notifica en vez de abrir.
func TestPickerNoTasks(t *testing.T) {
	root := writeTestTree(t, true)
	if err := os.WriteFile(filepath.Join(root, "tienda-web", "mise.toml"), []byte("[tools]\nnode = \"22\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := state.NewStoreAt(t.TempDir())
	m := New(store, &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()
	m = moveCursorTo(t, m, "tienda-web")
	m2, _ := press(m, "t")
	if m2.pickerOpen || !strings.Contains(m2.message, "no tasks defined") {
		t.Errorf("sin tasks: open=%v msg=%q", m2.pickerOpen, m2.message)
	}
}

// ---- Limpiar consola (0004 R31) ----

// C vacía los buffers y salta los offsets al EOF: el viewport queda
// limpio, el siguiente tail no reintroduce nada y los ficheros no
// cambian.
func TestClearConsole(t *testing.T) {
	m, store := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	// Simular contenido de consola + delta pendiente en el log.
	cs := m.consoleStateFor(path)
	cs.merged = "output viejo\n"
	m.setConsoleContent(cs.view(m.stream))
	stdout := store.StdoutLog(path)
	if err := os.MkdirAll(filepath.Dir(stdout), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stdout, []byte("output viejo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m2, cmd := press(m, "C")
	if cmd != nil {
		t.Error("C no debe emitir comandos")
	}
	if !strings.Contains(m2.message, "console cleared") {
		t.Errorf("message = %q", m2.message)
	}
	cs2 := m2.consoleStateFor(path)
	if cs2.merged != "" || cs2.stdout != "" || cs2.stderr != "" {
		t.Error("los buffers deben quedar vacíos")
	}
	if cs2.off[0] != int64(len("output viejo\n")) {
		t.Errorf("off[0] = %d, want EOF del log", cs2.off[0])
	}
	// El siguiente tail no reintroduce el contenido.
	dm := consoleTailCmd(path, cs2.off[0], cs2.off[1], stdout, store.StderrLog(path))().(consoleDeltaMsg)
	if dm.stdout != "" && dm.stderr != "" {
		t.Errorf("delta tras clear = %q/%q, want vacío", dm.stdout, dm.stderr)
	}
	// El fichero conserva su histórico.
	data, _ := os.ReadFile(stdout)
	if !strings.Contains(string(data), "output viejo") {
		t.Error("C no debe truncar los ficheros de log")
	}
}

func TestClearConsoleGuards(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = findGroup(m, "tienda")
	m2, _ := press(m, "C")
	if !strings.Contains(m2.message, "select a service") {
		t.Errorf("C sobre grupo: msg=%q", m2.message)
	}
	m3, _ := newTestModel(t)
	m3 = moveCursorTo(t, m3, "suelto")
	m4, _ := press(m3, "C")
	if !strings.Contains(m4.message, "No manifest") {
		t.Errorf("C sobre unconfigured: msg=%q", m4.message)
	}
}

// ---- Ask AI (0004 R32) ----

// Sin agentes instalados → notify, sin abrir nada.
func TestAskNoAgents(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // sin opencode/pi/hermes
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	if m2.askPromptOpen || m2.pickerOpen {
		t.Error("sin agentes no debe abrir nada")
	}
	if !strings.Contains(m2.message, "no AI agent found") {
		t.Errorf("message = %q", m2.message)
	}
}

// Un único agente instalado → directo al prompt (sin picker).
func TestAskSingleAgent(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	if m2.pickerOpen {
		t.Error("con un solo agente no debe abrir el picker")
	}
	if !m2.askPromptOpen || m2.askAgent.Name != "pi" {
		t.Errorf("askPromptOpen=%v agent=%q", m2.askPromptOpen, m2.askAgent.Name)
	}
	if !strings.Contains(m2.View().Content, "ask pi — tienda-api") {
		t.Error("el modal del prompt debe renderizar el título")
	}
}

// Varios agentes instalados → picker de agentes; enter lleva al prompt.
func TestAskPickerFlow(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "opencode", "pi", "hermes"))
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	if !m2.pickerOpen || m2.pickerKind != pickerAgents {
		t.Fatalf("a debe abrir el picker de agentes (open=%v kind=%v)", m2.pickerOpen, m2.pickerKind)
	}
	if len(m2.pickerItems) != 3 {
		t.Errorf("items = %d, want 3", len(m2.pickerItems))
	}
	m3, _ := press(m2, "j")
	m4, _ := press(m3, "enter")
	if m4.pickerOpen || !m4.askPromptOpen || m4.askAgent.Name != "pi" {
		t.Errorf("enter del picker: open=%v prompt=%v agent=%q", m4.pickerOpen, m4.askPromptOpen, m4.askAgent.Name)
	}
}

// El prompt vacío no lanza nada; el esc cancela antes de salir.
func TestAskPromptGuards(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	m2.promptInput.SetValue("") // el prefill default (R35) deja el input no vacío

	m3, _ := press(m2, "enter")
	if !m3.askPromptOpen || !strings.Contains(m3.message, "empty prompt") {
		t.Errorf("enter vacío: open=%v msg=%q", m3.askPromptOpen, m3.message)
	}

	m4, cmd := press(m3, "esc")
	if m4.askPromptOpen {
		t.Error("esc debe cerrar el prompt")
	}
	if cmd != nil {
		t.Error("esc con prompt abierto NO debe salir de la TUI")
	}
}

// Dispatch por herdr (fake): enter con prompt lanza split+run y notifica.
func TestAskDispatchHerdr(t *testing.T) {
	bin := fakeBin(t, "pi")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + filepath.Join(t.TempDir(), "calls.log") + "\"\n" +
		"case \"$1 $2\" in \"pane split\") echo '{\"result\":{\"pane\":{\"pane_id\":\"w1:p7\"}}}' ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HERDR_ENV", "1")

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	m3 := m2
	m3.promptInput.SetValue("fix the login bug")
	next, cmd := m3.dispatchAsk()
	m4 := next.(Model)
	if cmd == nil {
		t.Fatal("dispatch debe emitir el cmd del launcher")
	}
	if m4.askPromptOpen {
		t.Error("dispatch debe cerrar el modal")
	}
	msg := cmd().(statusMsg)
	if !strings.Contains(msg.message, "pi → herdr pane w1:p7") {
		t.Errorf("status = %q (el launcher herdr debe despachar)", msg.message)
	}
}

// a sobre un grupo → notify.
func TestAskOnGroup(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, _ := newTestModel(t)
	m.cursor = findGroup(m, "tienda")
	m2, _ := press(m, "a")
	if !strings.Contains(m2.message, "select a service") {
		t.Errorf("a sobre grupo: msg=%q", m2.message)
	}
}

// El prefill default (R35) rellena el input con el template builtin
// expandido: {name} → nombre del proyecto, {logs} → dir del servicio.
// Teclear añade al final (comportamiento editable real).
func TestAskPromptPrefillDefault(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, store := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	if !m2.askPromptOpen {
		t.Fatal("a debe abrir el prompt")
	}
	got := m2.promptInput.Value()
	if !strings.Contains(got, "Given the app tienda-api") {
		t.Errorf("prefill sin nombre del proyecto: %q", got)
	}
	if !strings.Contains(got, store.ServiceDir(pathOfSelected(t, m2))) {
		t.Errorf("prefill sin dir de logs: %q", got)
	}
	m3, _ := press(m2, "X")
	if !strings.HasSuffix(m3.promptInput.Value(), "X") {
		t.Errorf("teclear debe añadir al final del prefill: %q", m3.promptInput.Value())
	}
}

// Un template custom del config se expande con name/dir/logs.
func TestAskPromptPrefillCustom(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, _ := newTestModelWithConfig(t, `[ask]
prompt = "About {name} in {dir}, logs at {logs}: "
`)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	got := m2.promptInput.Value()
	wantName := "About tienda-api in "
	if !strings.HasPrefix(got, wantName) {
		t.Errorf("prefill = %q, want prefix %q", got, wantName)
	}
	if !strings.HasSuffix(got, ": ") {
		t.Errorf("prefill debe terminar con el sufijo del template: %q", got)
	}
}

// prompt = "" en config → sin prefill (comportamiento pre-R35).
func TestAskPromptPrefillEmpty(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, _ := newTestModelWithConfig(t, "[ask]\nprompt = \"\"\n")
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	if !m2.askPromptOpen || m2.promptInput.Value() != "" {
		t.Errorf("prompt vacío debe dejar el input limpio: open=%v value=%q", m2.askPromptOpen, m2.promptInput.Value())
	}
}

// R35: el textarea del prompt crece con el contenido hasta el cap de
// pantalla (16) y ahí se queda (scroll interno), nunca lo excede.
func TestAskPromptDynamicHeight(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, _ := newTestModel(t)
	m.width, m.height = 100, 40
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	if !m2.askPromptOpen {
		t.Fatal("a debe abrir el prompt")
	}
	if m2.promptInput.Height() != 6 {
		t.Errorf("alto inicial = %d, want 6 (grande de inicio)", m2.promptInput.Height())
	}
	// Contenido que envuelve en ~11 filas visuales: crece.
	m3 := m2
	m3.promptInput.SetValue(strings.Repeat("ab ", 300) + "TAIL1")
	if m3.promptInput.Height() < 10 {
		t.Errorf("alto tras prefill largo = %d, want >10 (crece con el contenido)", m3.promptInput.Height())
	}
	// Contenido mayor que el cap: el alto se clava en 16, no crece más.
	m4 := m3
	m4.promptInput.SetValue(strings.Repeat("ab ", 600) + "TAIL2")
	if m4.promptInput.Height() != 16 {
		t.Errorf("alto tras contenido enorme = %d, want 16 (cap)", m4.promptInput.Height())
	}
}

// R33: las líneas largas de la consola se envuelven (soft wrap) en vez
// de truncarse al borde derecho.
func TestConsoleSoftWrap(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	if !m.consoleView.SoftWrap {
		t.Fatal("el viewport de consola debe tener SoftWrap activado")
	}
	tail := "END-MARKER-XYZ"
	long := strings.Repeat("abcdefghij", 30) + " " + tail // 310 runes > rightW
	m.setConsoleContent(long)
	rendered := m.consoleView.View()
	if !strings.Contains(rendered, tail) {
		t.Error("el final de la línea larga debe ser visible tras el wrap")
	}
	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		if w := lipglossWidth(line); w > m.rightW {
			t.Errorf("línea de %d celdas excede rightW=%d: %q", w, m.rightW, line)
		}
	}
}

// R33: el contenido pre-estilizado conserva el color en la continuación
// (ansi.Cut re-emite las secuencias previas al corte).
func TestConsoleSoftWrapKeepsStyle(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	styled := styleLineError.Render(strings.Repeat("error text ", 40))
	m.setConsoleContent(styled)
	rendered := m.consoleView.View()
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatal("la línea estilizada larga debe envolver en ≥2 líneas")
	}
	if !strings.Contains(lines[1], "\x1b[") {
		t.Errorf("la línea de continuación perdió el estilo ANSI: %q", lines[1])
	}
}
