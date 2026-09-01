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
// tienda-api (Go, group tienda), tienda-web (JavaScript, group tienda) y
// suelto (Go, sin manifiesto). Devuelve el modelo y el store.
func newTestModel(t *testing.T) (Model, *state.Store) {
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
	writeFile("tienda-api/go.mod", "module api\n")
	writeFile("tienda-api/.vroom.toml", "name = \"tienda-api\"\ngroup = \"tienda\"\ncommand = \"go run main.go\"\nport = 8081\n")
	writeFile("tienda-web/package.json", "{}\n")
	writeFile("tienda-web/.vroom.toml", "name = \"tienda-web\"\ngroup = \"tienda\"\ncommand = \"node server.js\"\nport = 5173\n")
	writeFile("suelto/go.mod", "module suelto\n")

	store := state.NewStoreAt(t.TempDir())
	m := New(store, &stubManager{}, root)
	m.width, m.height = 100, 30
	return m, store
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
	default:
		km = tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	}
	next, cmd := m.Update(km)
	return next.(Model), cmd
}

func findCursor(m Model, name string) int {
	for i, e := range m.entries {
		if e.Project.Name == name {
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

// S12.1: navegación cíclica.
func TestNavigationCyclic(t *testing.T) {
	m, _ := newTestModel(t)
	if len(m.entries) != 3 {
		t.Fatalf("esperaba 3 proyectos, got %d", len(m.entries))
	}

	m.cursor = len(m.entries) - 1
	m, _ = press(m, "j")
	if m.cursor != 0 {
		t.Errorf("al final, 'j' debe volver al inicio; cursor = %d", m.cursor)
	}
	m, _ = press(m, "k")
	if m.cursor != len(m.entries)-1 {
		t.Errorf("en el inicio, 'k' debe ir al final; cursor = %d", m.cursor)
	}
	m, _ = press(m, "down")
	m, _ = press(m, "up")
	if m.cursor != len(m.entries)-1 {
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
	if m2.view != viewList {
		t.Error("no debe cambiar de vista")
	}
}

// Toggle sobre running: SIEMPRE para (nunca doble start, S15.1).
func TestToggleRunningStops(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := m.entries[m.cursor].Project.Path
	m.services[path].Status = statusRunning

	m2, cmd := press(m, "s")
	if cmd == nil {
		t.Fatal("toggle sobre running debe emitir stop")
	}
	if got := m2.services[path].Status; got != statusStopping {
		t.Errorf("estado = %s, want stopping", got)
	}
}

// Toggle sobre unknown: para el servicio dudosos.
func TestToggleUnknownStops(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := m.entries[m.cursor].Project.Path
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
	if got := m2.services[m2.entries[m2.cursor].Project.Path].Status; got != statusStarting {
		t.Errorf("estado = %s, want starting", got)
	}
}

// Toggle ignorado mientras el servicio está en tránsito.
func TestToggleInTransitIgnored(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := m.entries[m.cursor].Project.Path
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
	path := m.entries[m.cursor].Project.Path
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
	path := m.entries[m.cursor].Project.Path

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
	path := m.entries[m.cursor].Project.Path

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

// S9.3: unknown se mapea a estado unknown con indicador.
func TestRefreshMapsUnknown(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := m.entries[m.cursor].Project.Path

	next, _ := m.Update(refreshedMsg{results: map[string]refreshResult{
		path: {status: process.StatusUnknown},
	}})
	m2 := next.(Model)
	if got := m2.services[path].Status; got != statusUnknown {
		t.Errorf("estado = %s, want unknown", got)
	}
	view := m2.renderList()
	if !strings.Contains(view, "unknown") {
		t.Error("la lista debe mostrar unknown")
	}
}

// S16.1: Tab alterna stdout/stderr.
func TestTabTogglesLogStream(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.view = viewLogs
	m.resizeViewport()

	m2, cmd := press(m, "tab")
	if !m2.logStderr {
		t.Error("primer tab debe activar stderr")
	}
	if cmd == nil {
		t.Error("cambiar de stream debe re-tail el log")
	}
	m3, _ := press(m2, "tab")
	if m3.logStderr {
		t.Error("segundo tab debe volver a stdout")
	}
}

// S16.2: log vacío muestra "Sin logs disponibles" vía logTailMsg.
func TestEmptyLogShowsPlaceholder(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.view = viewLogs
	m.resizeViewport()

	next, _ := m.Update(logTailMsg{path: m.entries[m.cursor].Project.Path, isStderr: false, content: "No logs available"})
	m2 := next.(Model)
	if !strings.Contains(m2.logViewport.View(), "No logs available") {
		t.Errorf("viewport = %q", m2.logViewport.View())
	}
}

// R12: navegación de vistas.
func TestViewNavigation(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	m2, _ := press(m, "enter")
	if m2.view != viewService {
		t.Errorf("enter debe abrir detalle, got %d", m2.view)
	}
	m3, _ := press(m2, "l")
	if m3.view != viewLogs {
		t.Errorf("l debe abrir logs, got %d", m3.view)
	}
	m4, _ := press(m3, "esc")
	if m4.view != viewService {
		t.Errorf("esc desde logs debe volver al detalle, got %d", m4.view)
	}
	m5, _ := press(m4, "esc")
	if m5.view != viewList {
		t.Errorf("esc desde detalle debe volver a la lista, got %d", m5.view)
	}
}

// R11: la lista muestra nombre, lenguaje, grupo y estado.
func TestRenderList(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.services[m.entries[m.cursor].Project.Path].Status = statusRunning

	out := m.View().Content
	if !strings.Contains(out, "vroom — projects in") {
		t.Error("falta título")
	}
	for _, want := range []string{"tienda-api", "Go", "tienda-web", "JavaScript", "suelto", "unconfigured", "running"} {
		if !strings.Contains(out, want) {
			t.Errorf("la lista no contiene %q", want)
		}
	}
	// S10.1: header de grupo antes de los miembros.
	if !strings.Contains(out, "── tienda") {
		t.Error("falta separador de grupo")
	}
}

// S7.2: al refrescar con meta en disco el modelo re-adjunta el estado.
func TestReattachFromDisk(t *testing.T) {
	m, store := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := m.entries[m.cursor].Project.Path

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

// Help responsive: completa en ancho amplio, compacta y truncada en estrecho.
func TestHelpResponsive(t *testing.T) {
	full := helpLine(viewList, 200)
	if !strings.Contains(full, "start/stop") || !strings.Contains(full, "refresh") {
		t.Errorf("help completa inesperada: %q", full)
	}
	narrow := helpLine(viewList, 40)
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

// La lista se degrada en anchos estrechos (sin columna de lenguaje).
func TestRenderListNarrow(t *testing.T) {
	m, _ := newTestModel(t)
	m.width = 50
	out := m.View().Content
	if !strings.Contains(out, "tienda-api") || !strings.Contains(out, "stopped") {
		t.Errorf("lista estrecha debe mantener nombre y estado: %q", out)
	}
	if strings.Contains(out, "JavaScript") {
		t.Error("lista estrecha no debe incluir columna de lenguaje")
	}
}

// R17: `o` compone el editor correctamente (nvim -O, foco por stream,
// editores no-vim sin -O, override por $VISUAL/$EDITOR).
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

// La ayuda completa distingue l (ver integrado) de o (abrir en editor).
func TestHelpWording(t *testing.T) {
	full := helpLine(viewList, 200)
	for _, want := range []string{"l log view", "o open logs"} {
		if !strings.Contains(full, want) {
			t.Errorf("help sin %q: %q", want, full)
		}
	}
}
