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

// newTestModel builds a temp tree with 3 projects: tienda-api (Go, group tienda, git repo), tienda-web (JS, group tienda), suelto (Go, no manifest).
func newTestModel(t *testing.T) (Model, *state.Store) {
	t.Helper()
	isolateConfig(t)
	store := state.NewStoreAt(t.TempDir())
	m := New(store, &stubManager{}, writeTestTree(t, false))
	m.width, m.height = 100, 30
	m.updateLayout()
	return m, store
}

// newJobsTestModel is newTestModel plus install/build commands on tienda-api and a mise.toml with tasks on tienda-web.
func newJobsTestModel(t *testing.T) (Model, *state.Store) {
	t.Helper()
	isolateConfig(t)
	store := state.NewStoreAt(t.TempDir())
	m := New(store, &stubManager{}, writeTestTree(t, true))
	m.width, m.height = 100, 30
	m.updateLayout()
	return m, store
}

// isolateConfig points VROOM_CONFIG at a missing file so no test ever reads the real user config.
func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "absent-config.toml"))
}

// newTestModelWithConfig is newTestModel reading the given config.toml body, for keybinding and ask prefill tests.
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

// fakeBin writes stub executables and returns the directory to install as the whole PATH.
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

// writeTestTree builds the shared project tree; jobs=true adds the one-shot manifest fields and the mise tasks.
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
	manifestAPI := "name = \"tienda-api\"\nprimary_group = \"tienda\"\ncommand_start = \"go run main.go\"\nport = 8081\n"
	if jobs {
		manifestAPI += "command_install = \"echo installing\"\ncommand_build = \"echo building\"\n"
	}
	writeFile("tienda-api/go.mod", "module api\n")
	writeFile("tienda-api/.vroom.toml", manifestAPI)
	writeFile("tienda-api/.git/HEAD", "ref: refs/heads/main\n")
	writeFile("tienda-web/package.json", "{}\n")
	manifestWeb := "name = \"tienda-web\"\nprimary_group = \"tienda\"\ncommand_start = \"node server.js\"\nport = 5173\n"
	if jobs {
		manifestWeb += "command_install = \"echo installing web\"\ncommand_build = \"echo building web\"\n"
	}
	writeFile("tienda-web/.vroom.toml", manifestWeb)
	if jobs {
		writeFile("tienda-web/mise.toml", "[tasks.build]\ndescription = \"build the web\"\nrun = \"echo mise-build\"\n\n[tasks.test]\nrun = \"echo mise-test\"\n\n[tasks.hidden]\nhide = true\n")
	}
	writeFile("suelto/go.mod", "module suelto\n")
	writeFile("suelto/.vroom.toml", "name = \"suelto\"\ncommand_start = \"go run suelto\"\n")
	return root
}

func press(m Model, key string) (Model, tea.Cmd) {
	var km tea.Msg
	switch key {
	case "enter":
		km = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		km = tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		km = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
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

func findPrimary(m Model, name string) int {
	for i, it := range m.tree {
		if it.kind == itemPrimary && it.primary == name {
			return i
		}
	}
	return -1
}

func findSecondary(m Model, primary, secondary string) int {
	for i, it := range m.tree {
		if it.kind == itemSecondary && it.primary == primary && it.secondary == secondary {
			return i
		}
	}
	return -1
}

func moveCursorTo(t *testing.T, m Model, name string) Model {
	t.Helper()
	idx := findCursor(m, name)
	if idx < 0 {
		t.Fatalf("project %s not found", name)
	}
	m.cursor = idx
	return m
}

func pathOfSelected(t *testing.T, m Model) string {
	t.Helper()
	p := m.selected()
	if p == nil {
		t.Fatal("cursor is not on a project")
	}
	return p.Path
}

func TestNavigationCyclic(t *testing.T) {
	m, _ := newTestModel(t)
	if len(m.entries) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(m.entries))
	}
	if len(m.tree) != 4 {
		t.Fatalf("expected 4 tree rows, got %d", len(m.tree))
	}

	m.cursor = len(m.tree) - 1
	m, _ = press(m, "j")
	if m.cursor != 0 {
		t.Errorf("at the end, 'j' should wrap to the start; cursor = %d", m.cursor)
	}
	m, _ = press(m, "k")
	if m.cursor != len(m.tree)-1 {
		t.Errorf("at the start, 'k' should go to the end; cursor = %d", m.cursor)
	}
	m, _ = press(m, "down")
	m, _ = press(m, "up")
	if m.cursor != len(m.tree)-1 {
		t.Errorf("up/down should behave like j/k; cursor = %d", m.cursor)
	}
}

func TestToggleRunningStops(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusRunning

	m2, cmd := press(m, "s")
	if cmd == nil {
		t.Fatal("toggle on running should emit stop")
	}
	if got := m2.services[path].Status; got != statusStopping {
		t.Errorf("status = %s, want stopping", got)
	}
}

func TestToggleUnknownStops(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusUnknown

	m2, cmd := press(m, "s")
	if cmd == nil {
		t.Fatal("toggle on unknown should emit stop")
	}
	if got := m2.services[path].Status; got != statusStopping {
		t.Errorf("status = %s, want stopping", got)
	}
}

func TestToggleStoppedStarts(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	m2, cmd := press(m, "s")
	if cmd == nil {
		t.Fatal("toggle should emit start command")
	}
	if got := m2.services[pathOfSelected(t, m2)].Status; got != statusStarting {
		t.Errorf("status = %s, want starting", got)
	}
}

func TestToggleInTransitIgnored(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusStarting

	m2, cmd := press(m, "s")
	if cmd != nil {
		t.Error("toggle in transit should not emit command")
	}
	if got := m2.services[path].Status; got != statusStarting {
		t.Errorf("status = %s, want starting (no change)", got)
	}
}

func TestRestartChainsStopAndStart(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusRunning

	m2, cmd := press(m, "R")
	if cmd == nil {
		t.Fatal("restart should emit stop")
	}
	if got := m2.services[path].Status; got != statusStopping {
		t.Errorf("after R: status = %s, want stopping", got)
	}

	next, cmd2 := m2.Update(stoppedMsg{path: path})
	m3 := next.(Model)
	if cmd2 == nil {
		t.Fatal("restart should chain the start")
	}
	if got := m3.services[path].Status; got != statusStarting {
		t.Errorf("after stop: status = %s, want starting", got)
	}
}

func TestRestartRequiresRunning(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	m2, cmd := press(m, "R")
	if cmd != nil {
		t.Error("restart of stopped service should not emit command")
	}
	if !strings.Contains(m2.message, "running") {
		t.Errorf("message = %q", m2.message)
	}
}

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
		t.Errorf("status = %s, want running after refresh", got)
	}

	next, _ = m.Update(refreshedMsg{results: map[string]refreshResult{
		path: {status: process.StatusStopped},
	}})
	m2 = next.(Model)
	if got := m2.services[path].Status; got != statusStopped {
		t.Errorf("status = %s, want stopped (service crashed)", got)
	}
}

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
	msg := cmd()
	rm, ok := msg.(refreshedMsg)
	if !ok {
		t.Fatalf("unexpected msg: %T", msg)
	}
	if got := rm.results[path].status; got != process.StatusStopped {
		t.Errorf("status = %s, want stopped", got)
	}
	if rm.results[path].warn == "" {
		t.Error("should include warning for corrupt meta")
	}
}

func TestRefreshMapsUnknown(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	next, _ := m.Update(refreshedMsg{results: map[string]refreshResult{
		path: {status: process.StatusUnknown},
	}})
	m2 := next.(Model)
	if got := m2.services[path].Status; got != statusUnknown {
		t.Errorf("status = %s, want unknown", got)
	}
	view := m2.renderDashboard()
	if !strings.Contains(view, "unknown") {
		t.Error("dashboard should show unknown (detail badge)")
	}
}

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
		t.Errorf("reattached: status = %s, want running", got)
	}
}

func TestTickReschedules(t *testing.T) {
	m, _ := newTestModel(t)
	next, cmd := m.Update(tickMsg(time.Now()))
	m2 := next.(Model)
	if cmd == nil {
		t.Fatal("tick should reschedule the next cycle")
	}
	_ = m2
}

// The details panel has no toggle key: 'd' is gone for good, so esc quits straight out.
func TestDetailsAlwaysVisible(t *testing.T) {
	m, _ := newTestModel(t)
	if !m.detailsShown {
		t.Fatal("details panel should always be visible if it fits")
	}

	m2, cmd := press(m, "d")
	if cmd != nil || !m2.detailsShown {
		t.Error("d no longer exists: should do nothing")
	}
	m3, cmd2 := press(m2, "esc")
	if cmd2 == nil {
		t.Error("esc should quit the TUI (no panel to close)")
	}
	if !m3.detailsShown {
		t.Error("esc should not hide the details panel")
	}
}

func TestEnterOnProjectTogglesInnermost(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, cmd := press(m, "enter")
	if cmd != nil {
		t.Error("collapse should not emit commands")
	}
	if !m2.collapsed["tienda"] {
		t.Fatal("enter on project (no secondary) should collapse its primary tienda")
	}
	if len(m2.tree) != 2 { // 2 rows left: the collapsed tienda header plus the ungrouped suelto.
		t.Errorf("collapsed tree: %d rows, want 2", len(m2.tree))
	}
}

func TestDetailsAutoHideNarrow(t *testing.T) {
	m, _ := newTestModel(t)
	m.width = 50
	m.updateLayout()
	if m.detailsShown {
		t.Error("with width 50 the detail should not be shown")
	}
	out := m.renderDashboard()
	if strings.Contains(out, "language:") {
		t.Error("details panel should not render at minimum width")
	}
	if !strings.Contains(out, "tienda-api") {
		t.Error("tree should remain visible at minimum width")
	}
}

func TestActiveTabMarked(t *testing.T) {
	if tabLabel(tabConsole, true) == tabLabel(tabConsole, false) {
		t.Error("active tab should render differently from inactive")
	}
	if !strings.Contains(tabLabel(tabThreads, true), "2 Threads") {
		t.Error("tab label should preserve its text")
	}
}

func TestTreeAutoScroll(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	for i := 0; i < 12; i++ {
		dir := filepath.Join(root, "svc"+string(rune('a'+i)))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".vroom.toml"), []byte("name = \"svc"+string(rune('a'+i))+"\"\ncommand_start = \"echo\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 10 // bodyOuter=6, bodyH=4
	m.updateLayout()
	if m.bodyH != 4 {
		t.Fatalf("bodyH = %d, want 4", m.bodyH)
	}

	m.cursor = len(m.entries) - 1
	_, cl := m.treeLines()
	m.treeTop = cl - m.bodyH + 1
	if m.treeTop < 0 {
		m.treeTop = 0
	}

	m2, _ := press(m, "k")
	tree, cl2 := m2.treeLines()
	if cl2 < m2.treeTop || cl2 >= m2.treeTop+m2.bodyH {
		t.Errorf("cursor outside the window: cl=%d top=%d bodyH=%d total=%d",
			cl2, m2.treeTop, m2.bodyH, len(tree))
	}
	m3 := m2
	for i := 0; i < 10; i++ {
		m3, _ = press(m3, "k")
	}
	if m3.cursor != 0 || m3.treeTop != 0 {
		t.Errorf("cursor=%d treeTop=%d, want 0/0 after wrapping up", m3.cursor, m3.treeTop)
	}
}

// Exact width and height per line is the composer's contract: one extra line pushes the bottom border off screen.
func TestDashboardBoxWidthInvariant(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {120, 40}, {80, 24}, {60, 20}, {44, 20}, {40, 20}, {200, 50}} {
		m, _ := newTestModel(t)
		m.width, m.height = size[0], size[1]
		m.updateLayout()
		lines := strings.Split(m.renderDashboard(), "\n")
		if len(lines) != m.height {
			t.Errorf("%dx%d: %d lines, want %d", size[0], size[1], len(lines), m.height)
		}
		for i, l := range lines {
			if w := lipglossWidth(l); w != m.width {
				t.Errorf("%dx%d line %d: width %d, want %d (%q)", size[0], size[1], i, w, m.width, l)
				break
			}
		}
	}
}

func TestRenderDashboard(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.services[pathOfSelected(t, m)].Status = statusRunning

	out := m.View().Content
	for _, want := range []string{"Projects", "Details", "Output", "Keybinds"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing section title %q", want)
		}
	}
	for _, want := range []string{"tienda-api", "tienda-web", "suelto", "▾ tienda", "1 Console", "2 Threads"} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard does not contain %q", want)
		}
	}
	for _, want := range []string{"branch:", "main", "start:"} {
		if !strings.Contains(out, want) {
			t.Errorf("details panel does not contain %q", want)
		}
	}
}

func TestDetailsShowsBranch(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	lines := m.detailsLines(m.rightW)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "branch:") || !strings.Contains(joined, "main") {
		t.Errorf("detail without git branch: %q", joined)
	}
	m = moveCursorTo(t, m, "suelto")
	for _, l := range m.detailsLines(m.rightW) {
		if strings.Contains(l, "branch:") {
			t.Error("project without repo should not show branch")
		}
	}
}

func TestDetailsShowsCommands(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-web")
	joined := strings.Join(m.detailsLines(m.rightW), "\n")
	for _, want := range []string{"start:", "node server.js", "install:", "echo installing web", "build:", "echo building web"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details without %q: %q", want, joined)
		}
	}
	if !strings.Contains(joined, "stop:") {
		t.Errorf("details without stop row: %q", joined)
	}
	// An unconfigured stop must not inherit another field's value.
	if strings.Contains(joined, "docker") {
		t.Errorf("unconfigured stop should not show value: %q", joined)
	}
}

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
		t.Errorf("merged after second delta = %q, want append without reset", got)
	}
	if !strings.Contains(m2.consoleView.View(), "line2") {
		t.Error("viewport should contain the new content")
	}
}

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
		t.Errorf("isolated streams: stdout=%q stderr=%q", cs.stdout, cs.stderr)
	}
}

func TestConsoleBuffersSurviveNavigation(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	pathA := pathOfSelected(t, m)

	next, _ := m.Update(consoleDeltaMsg{path: pathA, stdout: "historical\n", offS: 10})
	m1 := next.(Model)

	m2, _ := press(m1, "j")
	pathB := pathOfSelected(t, m2)
	next, _ = m2.Update(consoleDeltaMsg{path: pathB, stdout: "web\n", offS: 4})
	m3 := next.(Model)

	m4, _ := press(m3, "k")
	if got := m4.consoleStateFor(pathA).merged; got != "historical\n" {
		t.Errorf("buffer of A lost: %q", got)
	}
	if !strings.Contains(m4.consoleView.View(), "historical") {
		t.Error("after returning to A the viewport should show its buffer")
	}
	if got := m4.consoleStateFor(pathB).merged; got != "web\n" {
		t.Errorf("buffer of B lost: %q", got)
	}
}

func TestConsoleFollowPause(t *testing.T) {
	m, _ := newTestModel(t)
	if !m.consoleFollow {
		t.Fatal("follow should start active")
	}
	m2, _ := press(m, "pgup")
	if m2.consoleFollow {
		t.Error("pgup should pause follow")
	}
	m3, _ := press(m2, "G")
	if !m3.consoleFollow {
		t.Error("G should reactivate follow")
	}
}

func TestConsolePageScroll(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.consoleView.SetHeight(5) // Set the height before setConsoleContent: GotoBottom reads it.
	m.setConsoleContent(strings.Repeat("line\n", 100))

	m2, _ := press(m, "pgup")
	if m2.consoleView.YOffset() == 0 {
		t.Error("pgup should scroll the view")
	}
	if m2.consoleFollow {
		t.Error("pgup should pause follow")
	}
	m3, _ := press(m2, "pgdown")
	if !m3.consoleView.AtBottom() {
		t.Errorf("pgdown should scroll the view down: yOffset=%d", m3.consoleView.YOffset())
	}
}

func TestMouseWheelScroll(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.consoleView.SetHeight(5)
	m.setConsoleContent(strings.Repeat("line\n", 100))

	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m2 := next.(Model)
	if m2.consoleFollow {
		t.Error("wheel up should pause follow")
	}
	if m2.consoleView.YOffset() == 0 {
		t.Error("wheel up should scroll the view")
	}

	for i := 0; i < 100; i++ {
		next, _ = m2.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m2 = next.(Model)
	}
	if !m2.consoleView.AtBottom() || m2.consoleView.YOffset() == 0 {
		t.Errorf("wheel down should go to the end: yOffset=%d", m2.consoleView.YOffset())
	}
	if !m2.consoleFollow {
		t.Error("wheel down to the end should reactivate follow")
	}

	before := m2.consoleView.YOffset()
	m4 := m2
	m4.activeTab = tabThreads
	next, _ = m4.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m5 := next.(Model)
	if !m5.consoleFollow || m5.consoleView.YOffset() != before {
		t.Error("wheel on Threads should not touch the console")
	}
}

func TestToggleStreamMode(t *testing.T) {
	m, _ := newTestModel(t)
	m2, _ := press(m, "c")
	if m2.stream != streamStdout {
		t.Errorf("after 1st c: %v, want stdout", m2.stream)
	}
	m3, _ := press(m2, "c")
	if m3.stream != streamStderr {
		t.Errorf("after 2nd c: %v, want stderr", m3.stream)
	}
	m4, _ := press(m3, "c")
	if m4.stream != streamMerged {
		t.Errorf("after 3rd c: %v, want merged", m4.stream)
	}
}

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
		t.Error("cap should preserve recent content")
	}
}

func TestEmptyConsoleShowsPlaceholder(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.setConsoleContent("")
	if !strings.Contains(m.consoleView.View(), "No logs available") {
		t.Errorf("viewport = %q", m.consoleView.View())
	}
}

// offBy allows 0.5 of drift because real wall-clock deltas skew the sampled CPU percentage.
func offBy(got, want float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d > 0.5
}

func TestConsoleTailPipelineRealFiles(t *testing.T) {
	m, store := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}
	stdoutLog := store.StdoutLog(path)

	if err := os.WriteFile(stdoutLog, []byte("boot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg1 := m.tailCmd()().(consoleDeltaMsg)
	next, _ := m.Update(msg1)
	m1 := next.(Model)
	if got := m1.consoleStateFor(path).merged; got != "boot\n" {
		t.Errorf("merged = %q, want boot\\n", got)
	}

	f, err := os.OpenFile(stdoutLog, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("ready \x1b[32mOK\x1b[0m\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	msg2 := m1.tailCmd()().(consoleDeltaMsg)
	next, _ = m1.Update(msg2)
	m2 := next.(Model)
	if got := m2.consoleStateFor(path).merged; got != "boot\nready OK\n" {
		t.Errorf("merged = %q, want boot\\nready OK\\n (delta + strip ANSI)", got)
	}
}

func TestTabSwitching(t *testing.T) {
	m, _ := newTestModel(t)
	if m.activeTab != tabConsole {
		t.Fatal("initial tab = Console")
	}
	m2, _ := press(m, "2")
	if m2.activeTab != tabThreads {
		t.Error("key 2 should activate Threads")
	}
	m3, _ := press(m2, "3")
	if m3.activeTab != tabMetrics {
		t.Error("key 3 should activate Metrics")
	}
	if m4, _ := press(m3, "7"); m4.activeTab != tabHealth {
		t.Error("key 7 should activate Health")
	}
	m5, _ := press(m3, "tab")
	if m5.activeTab != tabGit {
		t.Errorf("tab from Metrics should go to Git, got %v", m5.activeTab)
	}
	m6, _ := press(m5, "shift+tab")
	if m6.activeTab != tabMetrics {
		t.Errorf("shift+tab should return to Metrics, got %v", m6.activeTab)
	}
	m7, _ := press(m, "tab")
	if m7.activeTab != tabThreads {
		t.Errorf("tab from Console should go to Threads, got %v", m7.activeTab)
	}
	m8, _ := press(m7, "1")
	if m8.activeTab != tabConsole {
		t.Error("key 1 should activate Console")
	}
}

func TestThreadsPlaceholderNotRunning(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	lines := m.threadsLines(m.rightW, m.contentH)
	if len(lines) != 1 || !strings.Contains(lines[0], "service not running") {
		t.Errorf("lines = %v", lines)
	}
	m = moveCursorTo(t, m, "suelto")
	lines = m.threadsLines(m.rightW, m.contentH)
	if len(lines) != 1 || !strings.Contains(lines[0], "service not running") {
		t.Errorf("lines = %v", lines)
	}
}

func TestThreadsTableAndCPU(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusRunning
	m.services[path].Meta.Pid = 4242

	next, _ := m.Update(threadsMsg{path: path, threads: []process.ThreadInfo{
		{TID: 1, Name: "main", State: "R", Ticks: 100},
		{TID: 2, Name: "gc", State: "S", Ticks: 0},
	}})
	m1 := next.(Model)
	rows := m1.threads[path]
	if len(rows) != 2 || rows[0].CPU != 0 || rows[1].CPU != 0 {
		t.Fatalf("first sample: %+v", rows)
	}

	prev := m1.threadPrev[path]
	prev.at = time.Now().Add(-2 * time.Second)
	next, _ = m1.Update(threadsMsg{path: path, threads: []process.ThreadInfo{
		{TID: 1, Name: "main", State: "R", Ticks: 200},
		{TID: 2, Name: "gc", State: "S", Ticks: 10},
	}})
	m2 := next.(Model)
	rows = m2.threads[path]
	if rows[0].TID != 1 || offBy(rows[0].CPU, 50) {
		t.Errorf("order/CPU: %+v", rows)
	}
	if rows[1].TID != 2 || offBy(rows[1].CPU, 5) {
		t.Errorf("second thread: %+v", rows)
	}

	out := strings.Join(m2.threadsLines(m2.rightW, m2.contentH), "\n")
	for _, want := range []string{"main", "gc", "TID", "CPU%"} {
		if !strings.Contains(out, want) {
			t.Errorf("table without %q: %q", want, out)
		}
	}
}

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

func TestHelpResponsive(t *testing.T) {
	full1 := dashboardHelp1(200, nil)
	if !strings.Contains(full1, "start/stop") {
		t.Errorf("full help1 without start/stop: %q", full1)
	}
	full2 := dashboardHelp2(200, nil) // nil = defaults
	if !strings.Contains(full2, "refresh") {
		t.Errorf("full help2 without refresh: %q", full2)
	}
	narrow2 := dashboardHelp2(40, nil)
	if strings.Contains(narrow2, "refresh") {
		t.Errorf("in narrow mode refresh should not fit: %q", narrow2)
	}
	if utf8.RuneCountInString(narrow2) > 40 {
		t.Errorf("help2 not truncated to width: %d runes", utf8.RuneCountInString(narrow2))
	}
}

// The badge shows the real resolved port, never the declared one: in dynamic mode the declared port may belong to another worktree.
func TestBadgeShowsPort(t *testing.T) {
	p := scanner.Project{
		Path: "/tmp/x", Name: "x",
		Configured: true,
		Manifest:   &manifest.Manifest{Name: "x", Command: "run", Port: 8081, PortMode: manifest.PortModeDynamic},
	}
	sv := &ServiceState{Status: statusRunning, Meta: state.Meta{Port: 41501}}
	if badge := statusBadge(p, sv, "·", "·"); !strings.Contains(badge, ":41501") {
		t.Errorf("running badge with real port: %q", badge)
	}
	if badge := statusBadge(p, sv, "·", "·"); strings.Contains(badge, ":8081") {
		t.Errorf("badge should not emit the declared port: %q", badge)
	}
	sv.Status = statusUnknown
	if badge := statusBadge(p, sv, "·", "·"); !strings.Contains(badge, ":41501") {
		t.Errorf("unknown badge with real port: %q", badge)
	}
	sv.Status = statusStopped
	if badge := statusBadge(p, sv, "·", "·"); strings.Contains(badge, ":41501") {
		t.Errorf("stopped badge should not show port: %q", badge)
	}
	p2 := scanner.Project{Path: "/tmp/y", Name: "y"}
	if badge := statusBadge(p2, &ServiceState{Status: statusUnconfigured}, "·", "·"); strings.Contains(badge, ":") {
		t.Errorf("unconfigured badge with port: %q", badge)
	}
}

// The badge labels where the shown number came from: a port vroom reserved is (Dynamic), the manifest's own is (Fixed).
func TestBadgeLabelsPortOrigin(t *testing.T) {
	proj := func(mode string) scanner.Project {
		return scanner.Project{Path: "/tmp/x", Name: "x", Configured: true,
			Manifest: &manifest.Manifest{Name: "x", Command: "run", Port: 8081, PortMode: mode}}
	}
	running := func(metaPort int) *ServiceState {
		return &ServiceState{Status: statusRunning, Meta: state.Meta{Port: metaPort}}
	}

	if badge := statusBadge(proj(manifest.PortModeDynamic), running(41501), "·", "·"); !strings.Contains(badge, ":41501 (Dynamic)") {
		t.Errorf("a discovered port must say it was dynamic: %q", badge)
	}
	if badge := statusBadge(proj(manifest.PortModeFixed), running(8081), "·", "·"); !strings.Contains(badge, ":8081 (Fixed)") {
		t.Errorf("the manifest's own port must say it is fixed: %q", badge)
	}
	// Dynamic without a discovered port falls back to the declared one, and that number has not proved anything dynamic.
	if badge := statusBadge(proj(manifest.PortModeDynamic), running(0), "·", "·"); !strings.Contains(badge, ":8081 (Fixed)") {
		t.Errorf("the declared fallback must not be labelled dynamic: %q", badge)
	}
	none := scanner.Project{Path: "/tmp/y", Name: "y", Configured: true,
		Manifest: &manifest.Manifest{Name: "y", Command: "run"}}
	if badge := statusBadge(none, running(0), "·", "·"); strings.Contains(badge, "(Fixed)") || strings.Contains(badge, "(Dynamic)") {
		t.Errorf("without a port there is no origin to label: %q", badge)
	}
}

// Port pending gets its own badge instead of the generic spinner, and stays stoppable.
func TestBadgePortPendingIsItsOwnState(t *testing.T) {
	p := scanner.Project{
		Path: "/tmp/x", Name: "x", Configured: true,
		Manifest: &manifest.Manifest{Name: "x", Command: "run", Port: 8081, PortMode: manifest.PortModeDynamic},
	}
	sv := &ServiceState{Status: statusPortPending, Meta: state.Meta{Port: 41501, Pid: 4242}}

	badge := statusBadge(p, sv, "SPIN", "START")
	if strings.Contains(badge, "SPIN") || strings.Contains(badge, "unknown") {
		t.Errorf("port pending should not use the generic spinner: %q", badge)
	}
	if !strings.Contains(badge, "port pending") {
		t.Errorf("port pending should be named: %q", badge)
	}
	if !sv.Status.alive() {
		t.Error("port pending should remain stoppable")
	}

	sv.Status = statusNoPort
	if badge := statusBadge(p, sv, "SPIN", "START"); !strings.Contains(badge, "no port") {
		t.Errorf("service without port should be named: %q", badge)
	}
	if !sv.Status.alive() {
		t.Error("a service without port remains stoppable")
	}
}

func TestBuildEditorCmd(t *testing.T) {
	stdoutLog := "/state/services/abc/stdout.log"
	stderrLog := "/state/services/abc/stderr.log"

	cmd := buildEditorCmd("nvim", stdoutLog, stderrLog, false)
	want := []string{"nvim", "-O", stdoutLog, stderrLog}
	if !equalArgs(cmd.Args, want) {
		t.Errorf("args = %v, want %v", cmd.Args, want)
	}

	cmd = buildEditorCmd("nvim", stdoutLog, stderrLog, true)
	want = []string{"nvim", "-O", stderrLog, stdoutLog}
	if !equalArgs(cmd.Args, want) {
		t.Errorf("args = %v, want %v", cmd.Args, want)
	}

	cmd = buildEditorCmd("vim", stdoutLog, stderrLog, false)
	if !equalArgs(cmd.Args, []string{"vim", "-O", stdoutLog, stderrLog}) {
		t.Errorf("args = %v", cmd.Args)
	}

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
		t.Errorf("VISUAL should take priority, got %q", got)
	}
	t.Setenv("VISUAL", "")
	if got := resolveEditor(); got != "nano" {
		t.Errorf("secondary EDITOR, got %q", got)
	}
	t.Setenv("EDITOR", "")
	if got := resolveEditor(); got != "nvim" {
		t.Errorf("default nvim, got %q", got)
	}
}

func TestHelpWording(t *testing.T) {
	full := dashboardHelp1(200, nil) + " · " + dashboardHelp2(200, nil) // nil = defaults
	for _, want := range []string{"l logfile", "1-7 tabs", "enter collapse", "b build", "i install", "t tasks", "a ask", "C clear", "/ filter", "! shell"} {
		if !strings.Contains(full, want) {
			t.Errorf("help without %q: %q", want, full)
		}
	}
	if strings.Contains(full, "d info") {
		t.Error("help should not mention d info (toggle removed)")
	}
}

func TestHelpDefaultsDerived(t *testing.T) {
	want1 := "/ filter · shift+click select · ! shell · q quit · s start/stop · R restart · b build · i install"
	want2 := "j/k move · enter collapse · t tasks · a ask · C clear · c stream · l logfile · r refresh · 1-7 tabs"
	if got := dashboardHelp1(200, nil); got != want1 {
		t.Errorf("help1 defaults = %q, want %q", got, want1)
	}
	if got := dashboardHelp2(200, nil); got != want2 {
		t.Errorf("help2 defaults = %q, want %q", got, want2)
	}
}

func TestHelpRemapped(t *testing.T) {
	kb := map[string]string{"start_stop": "x"}
	full1 := dashboardHelp1(200, kb)
	if !strings.Contains(full1, "x start/stop") {
		t.Errorf("help1 without x start/stop: %q", full1)
	}
	if strings.Contains(full1, "s start/stop") {
		t.Errorf("help1 still mentions s start/stop: %q", full1)
	}
}

func TestRemappedStartStop(t *testing.T) {
	m, _ := newTestModelWithConfig(t, "[keybindings]\nstart_stop = \"x\"\n")
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	m2, cmd := press(m, "x")
	if cmd == nil {
		t.Fatal("x should trigger start (start_stop remapped)")
	}
	if m2.services[path].Status != statusStarting {
		t.Errorf("status = %q, want starting", m2.services[path].Status)
	}

	m3, cmd2 := press(m2, "s")
	if cmd2 != nil {
		t.Error("removed s should not trigger anything")
	}
	if m3.services[path].Status != statusStarting {
		t.Errorf("s should not change the status, got %q", m3.services[path].Status)
	}
}

func TestRemappedTasks(t *testing.T) {
	m, _ := newTestModelWithConfig(t, "[keybindings]\ntasks = \"m\"\n")
	m = moveCursorTo(t, m, "tienda-web")

	m2, _ := press(m, "m")
	if !strings.Contains(m2.message, "no mise.toml") {
		t.Errorf("m should trigger openPicker: msg=%q", m2.message)
	}

	m3, _ := newTestModelWithConfig(t, "[keybindings]\ntasks = \"m\"\n")
	m3 = moveCursorTo(t, m3, "tienda-web")
	m4, _ := press(m3, "t")
	if m4.message != "" || m4.pickerOpen {
		t.Errorf("removed t should not trigger anything: open=%v msg=%q", m4.pickerOpen, m4.message)
	}
}

func TestUniversalsWithRemap(t *testing.T) {
	m, _ := newTestModelWithConfig(t, "[keybindings]\nstart_stop = \"x\"\nrestart = \"z\"\n")

	m2, _ := press(m, "2")
	if m2.activeTab != tabThreads {
		t.Error("key 2 should activate Threads even with remap")
	}
	m3, _ := press(m2, "/")
	if !m3.filterOpen {
		t.Error("/ should open the filter even with remap")
	}
	m4, _ := press(m3, "esc")
	if m4.filterOpen {
		t.Error("esc should close the filter")
	}
	m4b := m4
	m4b.cursor = findPrimary(m4b, "tienda")
	m5, _ := press(m4b, "enter")
	if len(m5.tree) == len(m4b.tree) {
		t.Error("enter should collapse the group even with remap")
	}
	m6, _ := press(m5, "tab")
	if m6.activeTab != tabMetrics {
		t.Error("tab should cycle to the next tab even with remap")
	}
}

// 'o' is a fixed alias for the log editor, unless config binds 'o' to another action, which then wins.
func TestLogsAliasFixed(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, cmd := press(m, "o")
	if cmd == nil {
		t.Fatal("alias o should open the log editor")
	}
	_ = m2

	m3, _ := newTestModelWithConfig(t, "[keybindings]\ninstall = \"o\"\n")
	m3 = moveCursorTo(t, m3, "suelto") // has a manifest but no command_install
	m4, _ := press(m3, "o")
	if !strings.Contains(m4.message, "no install command") {
		t.Errorf("o claimed by install should trigger install: msg=%q", m4.message)
	}
}

func TestGroupCollapse(t *testing.T) {
	m, _ := newTestModel(t)
	gi := findPrimary(m, "tienda")
	if gi < 0 {
		t.Fatal("group tienda not found in the tree")
	}
	m.cursor = gi
	m.services[pathOfSelectedNamed(t, m, "tienda-api")].Status = statusRunning

	m2, cmd := press(m, "enter")
	if cmd != nil {
		t.Error("collapse should not emit commands")
	}
	if !m2.collapsed["tienda"] {
		t.Fatal("enter on group should collapse it")
	}
	if len(m2.tree) != 2 {
		t.Errorf("collapsed tree: %d rows, want 2", len(m2.tree))
	}
	if m2.cursor != gi {
		t.Errorf("cursor = %d, want %d (header keeps its index)", m2.cursor, gi)
	}
	tree, _ := m2.treeLines()
	treeText := strings.Join(tree, "\n")
	if !strings.Contains(treeText, "tienda (1/2)") {
		t.Errorf("collapsed header without count: %q", treeText)
	}
	if strings.Contains(treeText, "tienda-web") || strings.Contains(treeText, "tienda-api") {
		t.Errorf("members of the collapsed group should not be visible in the tree: %q", treeText)
	}
	// Collapsing hides members in the tree only: the details panel still lists them.
	if !strings.Contains(m2.renderDashboard(), "tienda-web") {
		t.Error("info panel should list members of the collapsed group")
	}

	m3, _ := press(m2, "enter")
	if m3.collapsed["tienda"] {
		t.Error("second enter should expand the group")
	}
	treeExp, _ := m3.treeLines()
	treeExpText := strings.Join(treeExp, "\n")
	if len(m3.tree) != 4 || !strings.Contains(treeExpText, "▾ tienda") || strings.Contains(treeExpText, "(1/2)") {
		t.Errorf("expanded tree should show members without count: %q", treeExpText)
	}
}

func TestGroupToggleAll(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = findPrimary(m, "tienda")

	m2, cmd := press(m, "s")
	if cmd == nil {
		t.Fatal("s on group with stopped should emit starts")
	}
	for _, name := range []string{"tienda-api", "tienda-web"} {
		sv := m2.services[pathOfSelectedNamed(t, m2, name)]
		if sv.Status != statusStarting {
			t.Errorf("%s: status = %s, want starting", name, sv.Status)
		}
	}

	for _, name := range []string{"tienda-api", "tienda-web"} {
		m2.services[pathOfSelectedNamed(t, m2, name)].Status = statusRunning
	}
	m3, cmd2 := press(m2, "s")
	if cmd2 == nil {
		t.Fatal("s on running group should emit stops")
	}
	for _, name := range []string{"tienda-api", "tienda-web"} {
		sv := m3.services[pathOfSelectedNamed(t, m3, name)]
		if sv.Status != statusStopping {
			t.Errorf("%s: status = %s, want stopping", name, sv.Status)
		}
	}
}

func TestGroupDetails(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = findPrimary(m, "tienda")
	m.services[pathOfSelectedNamed(t, m, "tienda-api")].Status = statusRunning

	lines := m.detailsLines(m.rightW)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"tienda (1/2)", "services:", "running:", "tienda-api", "tienda-web"} {
		if !strings.Contains(joined, want) {
			t.Errorf("group detail without %q: %q", want, joined)
		}
	}
}

func TestGroupConsolePlaceholder(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = findPrimary(m, "tienda")
	next, _ := m.onSelect()
	m2 := next.(Model)
	if !strings.Contains(m2.consoleView.View(), "group selected") {
		t.Errorf("viewport = %q, want group placeholder", m2.consoleView.View())
	}
	if m2.tailCmd() != nil {
		t.Error("with group selected should not tail logs")
	}
}

func pathOfSelectedNamed(t *testing.T, m Model, name string) string {
	t.Helper()
	idx := findCursor(m, name)
	if idx < 0 {
		t.Fatalf("project %s not found", name)
	}
	return m.tree[idx].project.Path
}

// tienda and otros both own a "backend" secondary, which is what proves the composite collapse keys cannot collide.
func newNestedTestModel(t *testing.T) Model {
	t.Helper()
	isolateConfig(t)
	root := t.TempDir()
	write := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mf := func(name, primary, secondary, command, port string) string {
		out := "name = \"" + name + "\"\nprimary_group = \"" + primary + "\"\n"
		if secondary != "" {
			out += "secondary_group = \"" + secondary + "\"\n"
		}
		return out + "command_start = \"" + command + "\"\nport = " + port + "\n"
	}
	write("tienda-api/go.mod", "module api\n")
	write("tienda-api/.vroom.toml", mf("tienda-api", "tienda", "backend", "go run main.go", "8081"))
	write("tienda-billing/go.mod", "module billing\n")
	write("tienda-billing/.vroom.toml", mf("tienda-billing", "tienda", "backend", "go run main.go", "8082"))
	write("tienda-inventory/requirements.txt", "flask\n")
	write("tienda-inventory/.vroom.toml", mf("tienda-inventory", "tienda", "", "python3 app.py", "8083"))
	write("tienda-web/package.json", "{}\n")
	write("tienda-web/.vroom.toml", mf("tienda-web", "tienda", "frontend", "node server.js", "5173"))
	write("otros-api/go.mod", "module otros\n")
	write("otros-api/.vroom.toml", mf("otros-api", "otros", "backend", "go run main.go", "8091"))
	write("suelto/go.mod", "module suelto\n")

	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()
	return m
}

func TestNestedTreeRender(t *testing.T) {
	m := newNestedTestModel(t)
	tree, _ := m.treeLines()
	joined := strings.Join(tree, "\n")
	for _, want := range []string{"▾ otros", "▾ tienda", "▾ frontend"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing header %q: %q", want, joined)
		}
	}
	// Secondary headers show 4 leading spaces (2 cursor + 2 indent); primary ones show 2.
	if got := strings.Count(joined, "▾ backend"); got != 2 {
		t.Errorf("expected 2 backend headers (tienda and otros), got %d: %q", got, joined)
	}
	for _, line := range tree {
		if !strings.Contains(line, "▾ backend") {
			continue
		}
		if !strings.HasPrefix(line, "    ") {
			t.Fatalf("secondary header should be indented 2 extra spaces: %q", line)
		}
	}
}

func TestSecondaryCollapse(t *testing.T) {
	m := newNestedTestModel(t)
	si := findSecondary(m, "tienda", "backend")
	if si < 0 {
		t.Fatal("header tienda/backend not found")
	}
	m.cursor = si
	m2, cmd := press(m, "enter")
	if cmd != nil {
		t.Error("collapse should not emit commands")
	}
	if !m2.collapsed["tienda/backend"] {
		t.Fatal("enter on secondary should collapse it")
	}
	if m2.cursor != si {
		t.Errorf("cursor = %d, want %d (header keeps its index)", m2.cursor, si)
	}
	tree, _ := m2.treeLines()
	joined := strings.Join(tree, "\n")
	if !strings.Contains(joined, "▸ backend") {
		t.Errorf("collapsed header should use ▸: %q", joined)
	}
	for _, name := range []string{"tienda-api", "tienda-billing"} {
		if strings.Contains(joined, name) {
			t.Errorf("%s should be hidden: %q", name, joined)
		}
	}
	for _, want := range []string{"▾ tienda", "tienda-inventory", "▾ frontend", "tienda-web", "otros-api"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s should remain visible: %q", want, joined)
		}
	}
}

func TestPrimaryCollapseHidesSecondaries(t *testing.T) {
	m := newNestedTestModel(t)
	pi := findPrimary(m, "tienda")
	m.cursor = pi
	m2, _ := press(m, "enter")
	if !m2.collapsed["tienda"] {
		t.Fatal("enter on primary should collapse it")
	}
	tree, _ := m2.treeLines()
	joined := strings.Join(tree, "\n")
	for _, want := range []string{"tienda-api", "tienda-billing", "tienda-inventory", "tienda-web", "▾ frontend"} {
		if strings.Contains(joined, want) {
			t.Errorf("%q should not be visible with primary collapsed: %q", want, joined)
		}
	}
	for _, want := range []string{"▸ tienda", "▾ otros", "▾ backend", "otros-api"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s should remain visible: %q", want, joined)
		}
	}
}

func TestEnterOnProjectInnermostNested(t *testing.T) {
	m := newNestedTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "enter")
	if !m2.collapsed["tienda/backend"] || m2.collapsed["tienda"] {
		t.Fatal("enter on project with secondary should collapse the secondary, not the primary")
	}
	m3 := moveCursorTo(t, m2, "tienda-inventory")
	m4, _ := press(m3, "enter")
	if !m4.collapsed["tienda"] {
		t.Fatal("enter on project without secondary should collapse its primary")
	}
}

func TestPrimaryCountIncludesSecondaries(t *testing.T) {
	m := newNestedTestModel(t)
	m.services[pathOfSelectedNamed(t, m, "tienda-api")].Status = statusRunning
	m.cursor = findPrimary(m, "tienda")
	m2, _ := press(m, "enter")
	tree, _ := m2.treeLines()
	if !strings.Contains(strings.Join(tree, "\n"), "tienda (1/4)") {
		t.Errorf("primary should count 4 members (backend 2 + inventory + frontend 1): %q",
			strings.Join(tree, "\n"))
	}
}

func TestSecondaryCollapseKeysNoCollision(t *testing.T) {
	m := newNestedTestModel(t)
	m.cursor = findSecondary(m, "tienda", "backend")
	m2, _ := press(m, "enter")
	tree, _ := m2.treeLines()
	joined := strings.Join(tree, "\n")
	if !strings.Contains(joined, "▸ backend") {
		t.Errorf("tienda/backend should collapse: %q", joined)
	}
	if !strings.Contains(joined, "otros-api") {
		t.Errorf("otros/backend should remain open: %q", joined)
	}
}

func TestPrimaryToggleAllSecondaries(t *testing.T) {
	m := newNestedTestModel(t)
	m.cursor = findPrimary(m, "tienda")
	m2, cmd := press(m, "s")
	if cmd == nil {
		t.Fatal("s on primary with stopped should emit starts")
	}
	for _, name := range []string{"tienda-api", "tienda-billing", "tienda-inventory", "tienda-web"} {
		if sv := m2.services[pathOfSelectedNamed(t, m2, name)]; sv.Status != statusStarting {
			t.Errorf("%s: status = %s, want starting", name, sv.Status)
		}
	}
}

func TestSecondaryToggleScoped(t *testing.T) {
	m := newNestedTestModel(t)
	m.services[pathOfSelectedNamed(t, m, "tienda-web")].Status = statusRunning
	m.cursor = findSecondary(m, "tienda", "backend")
	m2, cmd := press(m, "s")
	if cmd == nil {
		t.Fatal("s on secondary with stopped should emit starts")
	}
	for _, name := range []string{"tienda-api", "tienda-billing"} {
		if sv := m2.services[pathOfSelectedNamed(t, m2, name)]; sv.Status != statusStarting {
			t.Errorf("%s: status = %s, want starting", name, sv.Status)
		}
	}
	if sv := m2.services[pathOfSelectedNamed(t, m2, "tienda-web")]; sv.Status != statusRunning {
		t.Errorf("tienda-web (frontend) should not be touched: %s", sv.Status)
	}
}

func TestSecondaryDetails(t *testing.T) {
	m := newNestedTestModel(t)
	m.cursor = findSecondary(m, "tienda", "backend")
	m.services[pathOfSelectedNamed(t, m, "tienda-api")].Status = statusRunning

	joined := strings.Join(m.detailsLines(m.rightW), "\n")
	for _, want := range []string{"backend (1/2)", "services:", "running:", "tienda-api", "tienda-billing"} {
		if !strings.Contains(joined, want) {
			t.Errorf("secondary detail without %q: %q", want, joined)
		}
	}
	for _, no := range []string{"tienda-web", "tienda-inventory"} {
		if strings.Contains(joined, no) {
			t.Errorf("should not list members of other nodes: %q", no)
		}
	}
}

func TestDetailsGroupComposite(t *testing.T) {
	m := newNestedTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	if joined := strings.Join(m.detailsLines(m.rightW), "\n"); !strings.Contains(joined, "tienda/backend") {
		t.Errorf("details without primary/secondary composite: %q", joined)
	}
	m2 := moveCursorTo(t, m, "tienda-inventory")
	if joined := strings.Join(m2.detailsLines(m2.rightW), "\n"); strings.Contains(joined, "tienda/backend") {
		t.Errorf("primary without secondary should not show composite: %q", joined)
	}
}

func TestBuildKey(t *testing.T) {
	m, store := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	m2, cmd := press(m, "b")
	if cmd == nil {
		t.Fatal("b should trigger the build job")
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
		t.Error("jobMsg should release the project lock")
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
			t.Errorf("stdout.log without %q: %q", want, log)
		}
	}
}

func TestBuildKeyWithoutCommand(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, cmd := press(m, "b")
	if cmd != nil {
		t.Error("without build should not trigger job")
	}
	if !strings.Contains(m2.message, "no build command") {
		t.Errorf("message = %q, want no build command", m2.message)
	}
}

func TestInstallKey(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, cmd := press(m, "i")
	if cmd == nil {
		t.Fatal("i should trigger the install job")
	}
	if got := m2.jobs[pathOfSelected(t, m2)]; got != "install" {
		t.Errorf("jobs = %q, want install", got)
	}
	msg := cmd()
	if jm := msg.(jobMsg); jm.exitCode != 0 {
		t.Errorf("jobMsg = %+v, want exit 0", jm)
	}

	m4, _ := newTestModel(t)
	m4 = moveCursorTo(t, m4, "tienda-web")
	m5, cmd2 := press(m4, "i")
	if cmd2 != nil {
		t.Error("without install should not trigger job")
	}
	if !strings.Contains(m5.message, "no install command") {
		t.Errorf("message = %q, want no install command", m5.message)
	}
}

func TestJobBusyBlock(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "b")

	m3, cmd2 := press(m2, "i")
	if cmd2 != nil {
		t.Error("with job in progress should not trigger another")
	}
	if !strings.Contains(m3.message, "already running") {
		t.Errorf("message = %q, want already running", m3.message)
	}
	if m3.jobs[pathOfSelected(t, m3)] != "build" {
		t.Error("original job should remain marked")
	}

	m4 := moveCursorTo(t, m3, "tienda-web")
	m5, cmd3 := press(m4, "b")
	if cmd3 == nil {
		t.Error("another project should be able to trigger build in parallel")
	}
	_ = m5
}

func TestJobsGuards(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m.cursor = findPrimary(m, "tienda")
	m2, cmd := press(m, "b")
	if cmd != nil || !strings.Contains(m2.message, "select a service") {
		t.Errorf("b on group: cmd=%v msg=%q", cmd, m2.message)
	}

	m3, _ := newJobsTestModel(t)
	m3 = moveCursorTo(t, m3, "suelto")
	m4, cmd2 := press(m3, "i")
	if cmd2 != nil || !strings.Contains(m4.message, "no install command") {
		t.Errorf("i on suelto (without install): cmd=%v msg=%q", cmd2, m4.message)
	}
}

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
		t.Errorf("err = %v, want nil (known exit code)", jm.err)
	}

	data, err := os.ReadFile(stdout)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	for _, want := range []string{"── vroom ▶ build: echo boom && exit 3", "✗ build failed (exit 3, 0s)"} {
		if !strings.Contains(log, want) {
			t.Errorf("stdout.log without %q: %q", want, log)
		}
	}
}

func TestPickerOpenAndRun(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-web")

	m2, _ := press(m, "t")
	if !m2.pickerOpen {
		t.Fatalf("t should open the picker (msg=%q)", m2.message)
	}
	if len(m2.pickerItems) != 2 {
		t.Errorf("items = %d, want 2 (hidden excluded): %+v", len(m2.pickerItems), m2.pickerItems)
	}
	if m2.pickerItems[0].Name != "build" || m2.pickerItems[1].Name != "test" {
		t.Errorf("alphabetical order broken: %+v", m2.pickerItems)
	}
	out := m2.View().Content
	for _, want := range []string{"tasks — tienda-web", "▶ build", "j/k select · enter run · esc close"} {
		if !strings.Contains(out, want) {
			t.Errorf("picker render without %q", want)
		}
	}

	m3, _ := press(m2, "j")
	if m3.pickerCursor != 1 {
		t.Errorf("cursor = %d, want 1", m3.pickerCursor)
	}
	m4, cmd := press(m3, "enter")
	if m4.pickerOpen {
		t.Error("enter should close the picker")
	}
	if cmd == nil {
		t.Fatal("enter should trigger the task job")
	}
	if got := m4.jobs[pathOfSelected(t, m4)]; got != "task" {
		t.Errorf("jobs = %q, want task", got)
	}
}

func TestPickerModal(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-web")
	m2, _ := press(m, "t")

	m3, cmd := press(m2, "s")
	if cmd != nil || m3.services[pathOfSelected(t, m3)].Status != statusStopped {
		t.Error("modal should ignore s (no service toggle)")
	}
	if !m3.pickerOpen {
		t.Error("modal should remain open")
	}

	m4, cmd2 := press(m3, "esc")
	if m4.pickerOpen {
		t.Error("esc should close the picker")
	}
	if cmd2 != nil {
		t.Error("esc with picker should NOT quit the TUI")
	}
}

func TestPickerGuards(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "t")
	if m2.pickerOpen || !strings.Contains(m2.message, "no mise.toml") {
		t.Errorf("without mise.toml: open=%v msg=%q", m2.pickerOpen, m2.message)
	}

	m3, _ := newJobsTestModel(t)
	m3 = moveCursorTo(t, m3, "suelto")
	m4, _ := press(m3, "t")
	if m4.pickerOpen || !strings.Contains(m4.message, "no mise.toml") {
		t.Errorf("suelto without mise.toml: open=%v msg=%q", m4.pickerOpen, m4.message)
	}
}

func TestPickerNoTasks(t *testing.T) {
	isolateConfig(t)
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
		t.Errorf("without tasks: open=%v msg=%q", m2.pickerOpen, m2.message)
	}
}

func TestClearConsole(t *testing.T) {
	m, store := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	cs := m.consoleStateFor(path)
	cs.merged = "old output\n"
	m.setConsoleContent(cs.view(m.stream))
	stdout := store.StdoutLog(path)
	if err := os.MkdirAll(filepath.Dir(stdout), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stdout, []byte("old output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m2, cmd := press(m, "C")
	if cmd != nil {
		t.Error("C should not emit commands")
	}
	if !strings.Contains(m2.message, "console cleared") {
		t.Errorf("message = %q", m2.message)
	}
	cs2 := m2.consoleStateFor(path)
	if cs2.merged != "" || cs2.stdout != "" || cs2.stderr != "" {
		t.Error("buffers should be empty")
	}
	if cs2.off[0] != int64(len("old output\n")) {
		t.Errorf("off[0] = %d, want EOF of the log", cs2.off[0])
	}
	dm := consoleTailCmd(path, cs2.off[0], cs2.off[1], stdout, store.StderrLog(path))().(consoleDeltaMsg)
	if dm.stdout != "" && dm.stderr != "" {
		t.Errorf("delta after clear = %q/%q, want empty", dm.stdout, dm.stderr)
	}
	data, _ := os.ReadFile(stdout)
	if !strings.Contains(string(data), "old output") {
		t.Error("C should not truncate log files")
	}
}

func TestClearConsoleGuards(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = findPrimary(m, "tienda")
	m2, _ := press(m, "C")
	if !strings.Contains(m2.message, "select a service") {
		t.Errorf("C on group: msg=%q", m2.message)
	}
	m3, _ := newTestModel(t)
	m3 = moveCursorTo(t, m3, "suelto")
	m4, _ := press(m3, "C")
	if !strings.Contains(m4.message, "console cleared") {
		t.Errorf("C on suelto (with manifest): msg=%q", m4.message)
	}
}

func TestAskNoAgents(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no agents on PATH
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	if m2.askPromptOpen || m2.pickerOpen {
		t.Error("without agents should not open anything")
	}
	if !strings.Contains(m2.message, "no AI agent found") {
		t.Errorf("message = %q", m2.message)
	}
}

func TestAskSingleAgent(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	if m2.pickerOpen {
		t.Error("with a single agent should not open the picker")
	}
	if !m2.askPromptOpen || m2.askAgent.Name != "pi" {
		t.Errorf("askPromptOpen=%v agent=%q", m2.askPromptOpen, m2.askAgent.Name)
	}
	if !strings.Contains(m2.View().Content, "ask pi — tienda-api") {
		t.Error("prompt modal should render the title")
	}
}

func TestAskPickerFlow(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "opencode", "pi", "hermes"))
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	if !m2.pickerOpen || m2.pickerKind != pickerAgents {
		t.Fatalf("a should open the agent picker (open=%v kind=%v)", m2.pickerOpen, m2.pickerKind)
	}
	if len(m2.pickerItems) != 3 {
		t.Errorf("items = %d, want 3", len(m2.pickerItems))
	}
	m3, _ := press(m2, "j")
	m4, _ := press(m3, "enter")
	if m4.pickerOpen || !m4.askPromptOpen || m4.askAgent.Name != "pi" {
		t.Errorf("picker enter: open=%v prompt=%v agent=%q", m4.pickerOpen, m4.askPromptOpen, m4.askAgent.Name)
	}
}

func TestAskPromptGuards(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	m2.promptInput.SetValue("") // the default prefill leaves the input non-empty

	m3, _ := press(m2, "enter")
	if !m3.askPromptOpen || !strings.Contains(m3.message, "empty prompt") {
		t.Errorf("empty enter: open=%v msg=%q", m3.askPromptOpen, m3.message)
	}

	m4, cmd := press(m3, "esc")
	if m4.askPromptOpen {
		t.Error("esc should close the prompt")
	}
	if cmd != nil {
		t.Error("esc with prompt open should NOT quit the TUI")
	}
}

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
		t.Fatal("dispatch should emit the launcher cmd")
	}
	if m4.askPromptOpen {
		t.Error("dispatch should close the modal")
	}
	msg := cmd().(statusMsg)
	if !strings.Contains(msg.message, "pi → herdr pane w1:p7") {
		t.Errorf("status = %q (herdr launcher should dispatch)", msg.message)
	}
}

func TestAskOnGroup(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, _ := newTestModel(t)
	m.cursor = findPrimary(m, "tienda")
	m2, _ := press(m, "a")
	if !strings.Contains(m2.message, "select a service") {
		t.Errorf("a on group: msg=%q", m2.message)
	}
}

// The default prefill expands {name} to the project name and {logs} to its service dir.
func TestAskPromptPrefillDefault(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, store := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	if !m2.askPromptOpen {
		t.Fatal("a should open the prompt")
	}
	got := m2.promptInput.Value()
	if !strings.Contains(got, "Given the app tienda-api") {
		t.Errorf("prefill without project name: %q", got)
	}
	if !strings.Contains(got, store.ServiceDir(pathOfSelected(t, m2))) {
		t.Errorf("prefill without logs dir: %q", got)
	}
	m3, _ := press(m2, "X")
	if !strings.HasSuffix(m3.promptInput.Value(), "X") {
		t.Errorf("typing should append to the end of the prefill: %q", m3.promptInput.Value())
	}
}

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
		t.Errorf("prefill should end with the template suffix: %q", got)
	}
}

func TestAskPromptPrefillEmpty(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, _ := newTestModelWithConfig(t, "[ask]\nprompt = \"\"\n")
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	if !m2.askPromptOpen || m2.promptInput.Value() != "" {
		t.Errorf("empty prompt should leave the input clean: open=%v value=%q", m2.askPromptOpen, m2.promptInput.Value())
	}
}

func TestAskPromptDynamicHeight(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "pi"))
	m, _ := newTestModel(t)
	m.width, m.height = 100, 40
	m = moveCursorTo(t, m, "tienda-api")
	m2, _ := press(m, "a")
	if !m2.askPromptOpen {
		t.Fatal("a should open the prompt")
	}
	if m2.promptInput.Height() != 6 {
		t.Errorf("initial height = %d, want 6 (large by default)", m2.promptInput.Height())
	}
	m3 := m2
	m3.promptInput.SetValue(strings.Repeat("ab ", 300) + "TAIL1")
	if m3.promptInput.Height() < 10 {
		t.Errorf("height after long prefill = %d, want >10 (grows with content)", m3.promptInput.Height())
	}
	m4 := m3
	m4.promptInput.SetValue(strings.Repeat("ab ", 600) + "TAIL2")
	if m4.promptInput.Height() != 16 {
		t.Errorf("height after huge content = %d, want 16 (cap)", m4.promptInput.Height())
	}
}

func TestConsoleSoftWrap(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	if !m.consoleView.SoftWrap {
		t.Fatal("console viewport should have SoftWrap enabled")
	}
	tail := "END-MARKER-XYZ"
	long := strings.Repeat("abcdefghij", 30) + " " + tail
	m.setConsoleContent(long)
	rendered := m.consoleView.View()
	if !strings.Contains(rendered, tail) {
		t.Error("end of the long line should be visible after wrap")
	}
	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		if w := lipglossWidth(line); w > m.rightW {
			t.Errorf("line of %d cells exceeds rightW=%d: %q", w, m.rightW, line)
		}
	}
}

// Continuation lines keep their ANSI style because ansi.Cut re-emits the sequences that preceded the cut.
func TestConsoleSoftWrapKeepsStyle(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	styled := styleLineError.Render(strings.Repeat("error text ", 40))
	m.setConsoleContent(styled)
	rendered := m.consoleView.View()
	lines := strings.Split(strings.TrimRight(rendered, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatal("long styled line should wrap to ≥2 lines")
	}
	if !strings.Contains(lines[1], "\x1b[") {
		t.Errorf("continuation line lost ANSI style: %q", lines[1])
	}
}

// writeFilterTree adds a "frontend" secondary to tienda-web so filter-on-secondary can be exercised.
func writeFilterTree(t *testing.T) string {
	t.Helper()
	root := writeTestTree(t, false)
	manifest := "name = \"tienda-web\"\nprimary_group = \"tienda\"\nsecondary_group = \"frontend\"\ncommand_start = \"node server.js\"\nport = 5173\n"
	if err := os.WriteFile(filepath.Join(root, "tienda-web", ".vroom.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// newFilterTestModel: the tree is suelto plus tienda > {tienda-api, frontend > tienda-web}.
func newFilterTestModel(t *testing.T) Model {
	t.Helper()
	isolateConfig(t)
	store := state.NewStoreAt(t.TempDir())
	m := New(store, &stubManager{}, writeFilterTree(t))
	m.width, m.height = 100, 30
	m.updateLayout()
	return m
}

func typeFilter(m Model, s string) Model {
	for _, r := range s {
		next, _ := press(m, string(r))
		m = next
	}
	return m
}

func TestFilterOpenCapturesKeys(t *testing.T) {
	m, _ := newTestModel(t)
	m2, _ := press(m, "/")
	if !m2.filterOpen {
		t.Fatal("/ should open the filter")
	}
	if m2.filterInput.Value() != "" {
		t.Errorf("filter opens empty, got %q", m2.filterInput.Value())
	}
	if len(m2.tree) != 4 {
		t.Errorf("opening the filter should not alter the tree, rows = %d", len(m2.tree))
	}
	// The virtual cursor eats the placeholder's first letter, so the render holds "/", cursor, "ilter...".
	if !strings.Contains(m2.renderDashboard(), "ilter…") {
		t.Error("bar should show the filter… placeholder")
	}
	m3 := typeFilter(m2, "q")
	if !m3.filterOpen {
		t.Error("q should be inserted in the input, not close the filter")
	}
	if m3.filterInput.Value() != "q" {
		t.Errorf("input = %q, want q", m3.filterInput.Value())
	}
}

func TestFilterReopenKeepsText(t *testing.T) {
	m, _ := newTestModel(t)
	m2, _ := press(m, "/")
	m3 := typeFilter(m2, "tienda")
	m4, _ := press(m3, "enter")
	m5, _ := press(m4, "/")
	if !m5.filterOpen || m5.filterInput.Value() != "tienda" {
		t.Errorf("reopening should prefill the input, got open=%v value=%q", m5.filterOpen, m5.filterInput.Value())
	}
}

func TestFilterMatchNameAndPrimary(t *testing.T) {
	m, _ := newTestModel(t)
	m2, _ := press(m, "/")
	m3 := typeFilter(m2, "TIENDA")
	if len(m3.tree) != 3 {
		t.Fatalf("rows = %d, want 3", len(m3.tree))
	}
	if findCursor(m3, "suelto") != -1 {
		t.Error("suelto should not match TIENDA")
	}
	if findCursor(m3, "tienda-api") < 0 || findCursor(m3, "tienda-web") < 0 {
		t.Error("tienda-api and tienda-web should match TIENDA")
	}
}

// "front" appears in no project name and no primary group, so a match can only come from the secondary.
func TestFilterMatchSecondary(t *testing.T) {
	m := newFilterTestModel(t)
	m2, _ := press(m, "/")
	m3 := typeFilter(m2, "front")
	if len(m3.tree) != 3 {
		t.Fatalf("rows = %d, want 3", len(m3.tree))
	}
	if findSecondary(m3, "tienda", "frontend") < 0 {
		t.Error("frontend header should appear with the front filter")
	}
	if findCursor(m3, "tienda-web") < 0 {
		t.Error("tienda-web should match by its secondary")
	}
	if findCursor(m3, "tienda-api") != -1 || findCursor(m3, "suelto") != -1 {
		t.Error("only tienda-web should match front")
	}
}

func TestFilterHeadersOnlyWithMembers(t *testing.T) {
	m := newFilterTestModel(t)
	m2, _ := press(m, "/")
	m3 := typeFilter(m2, "api")
	if len(m3.tree) != 2 {
		t.Fatalf("rows = %d, want 2", len(m3.tree))
	}
	if findSecondary(m3, "tienda", "frontend") != -1 {
		t.Error("frontend header should not appear without matching members")
	}
	if findCursor(m3, "tienda-api") < 0 {
		t.Error("tienda-api should match")
	}
}

func TestFilterNoMatches(t *testing.T) {
	m, _ := newTestModel(t)
	m2, _ := press(m, "/")
	m3 := typeFilter(m2, "zzz")
	if len(m3.tree) != 0 {
		t.Fatalf("rows = %d, want 0", len(m3.tree))
	}
	if !strings.Contains(m3.renderDashboard(), "no matches") {
		t.Error("render should show the no matches line")
	}
}

func TestFilterEnterApplies(t *testing.T) {
	m, _ := newTestModel(t)
	m2, _ := press(m, "/")
	m3 := typeFilter(m2, "tienda")
	m4, _ := press(m3, "enter")
	if m4.filterOpen {
		t.Error("enter should close the box")
	}
	if m4.filterText != "tienda" {
		t.Errorf("filterText = %q, want tienda", m4.filterText)
	}
	if len(m4.tree) != 3 {
		t.Errorf("tree should remain filtered, rows = %d", len(m4.tree))
	}
	if !strings.Contains(m4.renderDashboard(), "⌕ tienda · 2") {
		t.Error("bar should show the indicator ⌕ tienda · 2")
	}
}

func TestFilterEscInBoxClears(t *testing.T) {
	m, _ := newTestModel(t)
	m2, _ := press(m, "/")
	m3 := typeFilter(m2, "tienda")
	m4, _ := press(m3, "esc")
	if m4.filterOpen {
		t.Error("esc should close the box")
	}
	if m4.filterText != "" {
		t.Errorf("filterText = %q, want empty", m4.filterText)
	}
	if len(m4.tree) != 4 {
		t.Errorf("tree should be restored complete, rows = %d", len(m4.tree))
	}
}

func TestFilterEscAppliedDoesNotQuit(t *testing.T) {
	m, _ := newTestModel(t)
	m2, _ := press(m, "/")
	m3 := typeFilter(m2, "tienda")
	m4, _ := press(m3, "enter")
	m5, cmd := press(m4, "esc")
	if cmd != nil {
		t.Error("esc with applied filter should not quit the TUI")
	}
	if m5.filterText != "" || len(m5.tree) != 4 {
		t.Errorf("esc should clear the filter, text=%q rows=%d", m5.filterText, len(m5.tree))
	}
}

func TestFilterEscWithoutFilterQuits(t *testing.T) {
	m, _ := newTestModel(t)
	_, cmd := press(m, "esc")
	if cmd == nil {
		t.Error("esc without filter should quit the TUI")
	}
}

func TestFilterResetsCursor(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = len(m.tree) - 1
	m2, _ := press(m, "/")
	m3 := typeFilter(m2, "t")
	if m3.cursor != 0 || m3.treeTop != 0 {
		t.Errorf("cursor=%d treeTop=%d, want 0/0 after filtering", m3.cursor, m3.treeTop)
	}
}

// Regression: the tree render used to always draw from row 0 and ignore treeTop.
func TestTreeRenderRespectsTreeTop(t *testing.T) {
	m, _ := newTestModel(t)
	m.height = 9 // bodyOuter=5, bodyH=3, which is shorter than the tree.
	m.updateLayout()
	m.cursor = 2
	m2, _ := press(m, "j") // cursor=3, treeTop=1
	if m2.treeTop != 1 {
		t.Fatalf("treeTop = %d, want 1", m2.treeTop)
	}
	lines := m2.treeColumnLines()
	full, _ := m2.treeLines()
	if lines[0] != full[1] {
		t.Errorf("visible line should be the treeTop row: got %q, want %q", lines[0], full[1])
	}
	if len(lines) != 3 {
		t.Errorf("visible rows = %d, want 3 (bodyH)", len(lines))
	}
}

func TestFilterBarConsumesTreeLine(t *testing.T) {
	m, _ := newTestModel(t)
	if len(m.treeColumnLines()) != 4 {
		t.Fatal("without bar the tree occupies the full height")
	}
	m2, _ := press(m, "/")
	m3 := typeFilter(m2, "tienda")
	m4, _ := press(m3, "enter")
	lines := m4.treeColumnLines()
	full, _ := m4.treeLines()
	if lines[0] != m4.filterBar() {
		t.Errorf("line 0 should be the bar, got %q", lines[0])
	}
	if lines[1] != full[0] {
		t.Errorf("tree rows should start at line 1: got %q, want %q", lines[1], full[0])
	}
}

// Structural guard: discovery on the 2s tick would turn every refresh into per-service listener work.
func TestDiscoveryIsNotInTheTUIRefreshPath(t *testing.T) {
	data, err := os.ReadFile("app.go")
	if err != nil {
		t.Skipf("cannot read app.go: %v", err)
	}
	// portless is on the same list because the seam execs the binary; start and stop use it via route.go instead.
	for _, forbidden := range []string{"DiscoverPort", "lineageListenersAt", "ReservePort", "portless."} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("TUI tick must not call %s: cost per refresh must be bounded", forbidden)
		}
	}
}

func TestRefreshEvaluatesResolvedPortWithoutPending(t *testing.T) {
	var got []process.EvalSpec
	manager := &stubManager{eval: func(spec process.EvalSpec) process.Status {
		got = append(got, spec)
		return process.StatusRunning
	}}
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	p := scanner.Project{
		Path: dir, Name: "a", Configured: true,
		Manifest: &manifest.Manifest{Name: "a", Command: "run", Port: 8080, PortMode: manifest.PortModeDynamic},
	}
	if err := store.SaveMeta(dir, state.Meta{
		Port: 41501, Pid: 1, CreationTimeMs: 2, State: state.StateRunning,
	}); err != nil {
		t.Fatal(err)
	}

	msg := refreshCmd(store, manager, []scanner.Project{p})()
	res, ok := msg.(refreshedMsg)
	if !ok {
		t.Fatalf("refreshCmd returned %T", msg)
	}
	if got[0].Port != 41501 {
		t.Errorf("tick should evaluate the real port, not the declared one: %+v", got[0])
	}
	if got[0].PortPending {
		t.Error("a resolved port is not pending")
	}
	if res.results[dir].status != process.StatusRunning {
		t.Errorf("status = %s, want running", res.results[dir].status)
	}
}

// M2: an unresolved port must never fall back to the declared one, which may belong to another worktree.
func TestHealthTabDoesNotProbeUnresolvedDeclaredPort(t *testing.T) {
	p := scanner.Project{
		Path: "/tmp/x", Name: "x", Configured: true,
		Manifest: &manifest.Manifest{
			Name: "x", Command: "run", Port: 8080, PortMode: manifest.PortModeDynamic,
		},
	}
	sv := &ServiceState{
		Status: statusPortUnresolved,
		Meta:   state.Meta{Pid: 4242, State: state.StatePortUnresolved},
	}

	if got := displayPort(p, sv); got != 0 {
		t.Errorf("displayPort = %d, want 0: an unresolved port does not fall back to the declared one", got)
	}

	badge := statusBadge(p, sv, "SPIN", "START")
	if strings.Contains(badge, "8080") {
		t.Errorf("badge should not show the unconfirmed declared port: %q", badge)
	}
	if !strings.Contains(badge, "port unresolved") {
		t.Errorf("status should be named: %q", badge)
	}
	if !sv.Status.alive() {
		t.Error("a service with unresolved port remains stoppable")
	}

	m := Model{
		projects: []scanner.Project{p},
		services: map[string]*ServiceState{p.Path: sv},
		tree:     []treeItem{{kind: itemProject, project: p}},
	}
	if cmd := m.healthCmd(); cmd != nil {
		t.Error("Health tab should not launch a probe against an unconfirmed port")
	}
	lines := m.healthLines(80)
	joined := strings.Join(lines, " ")
	if strings.Contains(joined, "8080") || strings.Contains(joined, "127.0.0.1") {
		t.Errorf("Health view should not emit a URL against the declared port: %q", joined)
	}
	if !strings.Contains(joined, "unresolved") {
		t.Errorf("Health view should explain why it does not probe: %q", joined)
	}
}

func TestHealthTabStillProbesResolvedPort(t *testing.T) {
	p := scanner.Project{
		Path: "/tmp/x", Name: "x", Configured: true,
		Manifest: &manifest.Manifest{
			Name: "x", Command: "run", Port: 8080, PortMode: manifest.PortModeDynamic,
		},
	}
	sv := &ServiceState{
		Status: statusRunning,
		Meta:   state.Meta{Pid: 4242, Port: 41501, State: state.StateRunning, PortVerified: true},
	}
	if got := displayPort(p, sv); got != 41501 {
		t.Fatalf("displayPort = %d, want the real port 41501", got)
	}
	if url := healthURL(&p, sv); url != "http://127.0.0.1:41501/" {
		t.Errorf("healthURL = %q, want the real port", url)
	}
}
