package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/agents"
	"vroom/internal/config"
	"vroom/internal/launcher"
	"vroom/internal/manifest"
	"vroom/internal/mise"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
	"vroom/internal/tail"
)

func noSelection(t *testing.T) Model {
	t.Helper()
	m, _ := newTestModel(t)
	cursorOn(t, &m, "tienda") // the primary header, not a project
	if m.selected() != nil {
		t.Fatalf("the cursor is still on a project: %v", m.selected())
	}
	return m
}

func noManifest(t *testing.T) Model {
	t.Helper()
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "suelto")
	it, ok := m.selectedItem()
	if !ok {
		t.Fatal("no item under the cursor")
	}
	it.project.Configured = false
	it.project.Manifest = nil
	m.tree[m.cursor] = it
	return m
}

// An action that cannot act must say why; the test checks only that the message exists and is not another reason's.
func TestActionsWithoutSelectionWarnAndDoNotAct(t *testing.T) {
	actions := []struct {
		name  string
		acc   func(Model) (tea.Model, tea.Cmd)
		wants string // a word that must appear in the notice
	}{
		{"restart", func(m Model) (tea.Model, tea.Cmd) { return m.restartSelected() }, "select a service"},
		{"clearConsole", func(m Model) (tea.Model, tea.Cmd) { return m.clearConsole() }, "select a service"},
		{"openLogEditor", func(m Model) (tea.Model, tea.Cmd) { return m.openLogEditor() }, "select a service"},
		{"runInstall", func(m Model) (tea.Model, tea.Cmd) { return m.runInstall() }, "select a service"},
		{"runBuild", func(m Model) (tea.Model, tea.Cmd) { return m.runBuild() }, "select a service"},
		{"openPicker", func(m Model) (tea.Model, tea.Cmd) { return m.openPicker() }, "select a service"},
	}
	for _, a := range actions {
		t.Run(a.name, func(t *testing.T) {
			m := noSelection(t)
			next, cmd := a.acc(m)
			got := next.(Model)

			if cmd != nil {
				t.Errorf("%s without selection launched something: %+v", a.name, cmd)
			}
			if got.message == "" {
				t.Errorf("%s without selection said nothing: the user has no way to know why", a.name)
			}
			if !strings.Contains(got.message, a.wants) {
				t.Errorf("%s = %q, want it to mention %q", a.name, got.message, a.wants)
			}
			if len(got.pendingRestart) > 0 {
				t.Errorf("%s marked a pending restart without doing anything", a.name)
			}
		})
	}
}

// A missing manifest must not reuse the "select a service" message: the project is selected, only unusable.
func TestActionsWithoutManifestWarnWithManifestReason(t *testing.T) {
	actions := []struct {
		name string
		acc  func(Model) (tea.Model, tea.Cmd)
	}{
		{"clearConsole", func(m Model) (tea.Model, tea.Cmd) { return m.clearConsole() }},
		{"openLogEditor", func(m Model) (tea.Model, tea.Cmd) { return m.openLogEditor() }},
		{"runInstall", func(m Model) (tea.Model, tea.Cmd) { return m.runInstall() }},
		{"runBuild", func(m Model) (tea.Model, tea.Cmd) { return m.runBuild() }},
		{"openPicker", func(m Model) (tea.Model, tea.Cmd) { return m.openPicker() }},
	}
	for _, a := range actions {
		t.Run(a.name, func(t *testing.T) {
			m := noManifest(t)
			next, cmd := a.acc(m)
			got := next.(Model)

			if cmd != nil {
				t.Errorf("%s without manifest launched something", a.name)
			}
			if !strings.Contains(got.message, "manifest") {
				t.Errorf("%s = %q, want the reason to be the manifest", a.name, got.message)
			}
			if strings.Contains(got.message, "select a service") {
				t.Errorf("%s = %q: the project IS selected, the reason is different", a.name, got.message)
			}
		})
	}
}

// The message must name the manifest field to write: the user is one file away from fixing it.
func TestActionsWithoutCommandSayWhichFieldToWrite(t *testing.T) {
	// the base tree has neither commands.install.run nor commands.build.run
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	for _, tt := range []struct {
		name  string
		acc   func(Model) (tea.Model, tea.Cmd)
		field string
	}{
		{"runInstall", func(m Model) (tea.Model, tea.Cmd) { return m.runInstall() }, "commands.install.run"},
		{"runBuild", func(m Model) (tea.Model, tea.Cmd) { return m.runBuild() }, "commands.build.run"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			next, cmd := tt.acc(m)
			got := next.(Model)
			if cmd != nil {
				t.Errorf("%s with a manifest without command launched something", tt.name)
			}
			if !strings.Contains(got.message, tt.field) {
				t.Errorf("%s = %q, want it to name the field %q: that's what the user has to write", tt.name, got.message, tt.field)
			}
		})
	}
}

// A restart is stop+start and needs something running: skipping the stop skips a commands.stop.run that may clear a volume or queue.
func TestRestartSelectedOnlyRestartsWhatIsRunning(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	m.services[path].Status = statusStopped
	next, cmd := m.restartSelected()
	got := next.(Model)
	if cmd != nil {
		t.Error("restarting a stopped service cannot be a stop+start")
	}
	if !strings.Contains(got.message, "running") {
		t.Errorf("= %q, want it to say that only what is running is restarted", got.message)
	}
	if len(got.pendingRestart) != 0 {
		t.Errorf("marked a pending restart without starting anything: %v", got.pendingRestart)
	}

	markRunning(&got, path, livePID(t))
	next, cmd = got.restartSelected()
	withRestart := next.(Model)
	if cmd == nil {
		t.Fatal("restarting a live service has to emit the stop command")
	}
	if !withRestart.pendingRestart[path] {
		t.Error("the pending restart was not marked: when returning from stop the start would not be chained")
	}
	if withRestart.services[path].Status != statusStopping {
		t.Errorf("Status = %q during restart, want stopping: otherwise, the user sees running and presses stop again",
			withRestart.services[path].Status)
	}
}

// The nil guard exists because group actions call this without a manifest; a nil deref here would panic the whole TUI.
func TestManifestStopReturnsCommandAndDoesNotPanicWithoutManifest(t *testing.T) {
	if got := manifestStop(scanner.Project{}); got != "" {
		t.Errorf("without manifest = %q, want empty string: an invented command would be a shell executed by surprise", got)
	}
	if got := manifestStop(scanner.Project{Manifest: nil}); got != "" {
		t.Errorf("with nil manifest = %q", got)
	}
	if got := manifestStop(scanner.Project{Manifest: &manifest.Manifest{Name: "p", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "./p"}, Stop: manifest.Runnable{Run: "docker compose down"}}}}); got != "docker compose down" {
		t.Errorf("= %q, want the manifest's commands.stop.run", got)
	}
}

// A corrupt mode must render as "merged", the real default: naming a nonexistent stream makes the toggle look broken.
func TestStreamModeStringCoversThreePlusUnknown(t *testing.T) {
	tests := []struct {
		in   streamMode
		want string
	}{
		{streamMerged, "merged"},
		{streamStdout, "stdout"},
		{streamStderr, "stderr"},
		{streamMode(99), "merged"},
		{streamMode(-1), "merged"},
	}
	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("streamMode(%d).String() = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The default to stopped is deliberate and the dangerous part: a status invented tomorrow in process would offer "start" on a running service.
func TestMapUIStatusTranslatesEachStateAndDoesNotInventNew(t *testing.T) {
	tests := []struct {
		in   process.Status
		want uiStatus
	}{
		{process.StatusRunning, statusRunning},
		{process.StatusStopped, statusStopped},
		{process.StatusUnknown, statusUnknown},
		{process.StatusPortPending, statusPortPending},
		{process.StatusPortUnresolved, statusPortUnresolved},
		{process.StatusNoPort, statusNoPort},
		{process.Status("invented"), statusStopped},
		{process.Status(""), statusStopped},
	}
	for _, tt := range tests {
		if got := mapUIStatus(tt.in); got != tt.want {
			t.Errorf("mapUIStatus(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStatusBadgeCoversEightStatesPlusUnconfigured(t *testing.T) {
	p := scanner.Project{
		Path: "/p", Name: "p", Configured: true,
		Manifest: manifestWithPort(4321),
	}
	new := func(s uiStatus) *ServiceState {
		return &ServiceState{Status: s, Meta: state.Meta{Pid: 4321, Pgid: 4321, Port: 4321, State: state.StateRunning}}
	}

	tests := []struct {
		name   string
		status uiStatus
		wants  string
	}{
		{"running", statusRunning, "running"},
		{"starting", statusStarting, "starting"},
		{"stopping", statusStopping, "stopping"},
		{"port pending", statusPortPending, "port pending"},
		{"port unresolved", statusPortUnresolved, "unresolved"},
		{"no port", statusNoPort, "no port"},
		{"unknown", statusUnknown, "unknown"},
		{"stopped", statusStopped, "stopped"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tail.StripANSI(statusBadge(p, new(tt.status), "◐", "◌"))
			if !strings.Contains(got, tt.wants) {
				t.Errorf("statusBadge(%s) = %q, want %q", tt.name, got, tt.wants)
			}
		})
	}

	t.Run("unconfigured", func(t *testing.T) {
		p2 := scanner.Project{Path: "/p", Name: "p"}
		if got := tail.StripANSI(statusBadge(p2, nil, "◐", "◌")); !strings.Contains(got, "unconfigured") {
			t.Errorf("= %q, want unconfigured", got)
		}
	})

	t.Run("invalid manifest", func(t *testing.T) {
		p3 := scanner.Project{Path: "/p", Name: "p", ManifestErr: "missing commands.start.run"}
		got := tail.StripANSI(statusBadge(p3, nil, "◐", "◌"))
		if !strings.Contains(got, "invalid") || strings.Contains(got, "unconfigured") {
			t.Errorf("= %q: a malformed manifest is not the same as an absent one", got)
		}
	})
}

// At n <= 1 trunc keeps the first rune and truncTail the last: swapping them swaps a character that both read as garbage.
func TestTruncAndTruncTailPreserveDifferentEnds(t *testing.T) {
	tests := []struct {
		in        string
		n         int
		wantTrunc string
		wantTail  string
	}{
		{"abcdef", 10, "abcdef", "abcdef"},
		{"abcdef", 6, "abcdef", "abcdef"},
		{"abcdef", 4, "abc…", "…def"},
		{"abcdef", 1, "a", "f"},
		{"abcdef", 0, "", ""},
		{"abcdef", -3, "", ""},
		{"αβγδε", 3, "αβ…", "…δε"},
	}
	for _, tt := range tests {
		if got := trunc(tt.in, tt.n); got != tt.wantTrunc {
			t.Errorf("trunc(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.wantTrunc)
		}
		if got := truncTail(tt.in, tt.n); got != tt.wantTail {
			t.Errorf("truncTail(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.wantTail)
		}
	}

	for n := range 8 {
		if got := trunc("αβγδεζ", n); strings.ToValidUTF8(got, "") != got {
			t.Errorf("trunc with n=%d split a rune: %q", n, got)
		}
		if got := truncTail("αβγδεζ", n); strings.ToValidUTF8(got, "") != got {
			t.Errorf("truncTail with n=%d split a rune: %q", n, got)
		}
	}
}

// padW fills to a VISIBLE width and never truncates: a negative width would cut escape codes in half.
func TestPadWDoesNotClipAndMeasuresInVisibleCells(t *testing.T) {
	if got := padW("abc", 10); len(got) != 10 {
		t.Errorf("padW(\"abc\", 10) measures %d bytes, want 10", len(got))
	}
	if got := padW("abc", 2); got != "abc" {
		t.Errorf("padW(\"abc\", 2) = %q: padding never truncates", got)
	}
	if got := padW("abc", -5); got != "abc" {
		t.Errorf("padW with negative width = %q, want the original untouched", got)
	}

	coloured := "\x1b[31mabc\x1b[0m"
	got := padW(coloured, 8)
	if n := lipglossWidth(got); n != 8 {
		t.Errorf("padW with ANSI measures %d visible cells, want 8: %q", n, got)
	}
	if !strings.Contains(got, "\x1b[31m") {
		t.Errorf("the padding ate the style: %q", got)
	}
}

// The compact line must carry the ellipsis: an unmarked cut would show a shortcut that does not exist.
func TestDashboardHelpAdaptsToWidthAndRespectsBindings(t *testing.T) {
	kb := map[string]string{
		"start_stop": "S", "restart": "R", "build": "B", "install": "I",
		"tasks": "t", "ask": "a", "clear": "c", "stream": "M", "logs": "L", "refresh": "r",
	}

	t.Run("respects the bindings", func(t *testing.T) {
		h1 := dashboardHelp1(200, kb)
		for _, want := range []string{"S start/stop", "R restart", "B build", "I install"} {
			if !strings.Contains(h1, want) {
				t.Errorf("line 1 does not have %q: %q", want, h1)
			}
		}
		h2 := dashboardHelp2(200, kb)
		for _, want := range []string{"t tasks", "a ask", "c clear", "L logfile", "r refresh"} {
			if !strings.Contains(h2, want) {
				t.Errorf("line 2 does not have %q: %q", want, h2)
			}
		}
	})

	t.Run("with nil map uses the defaults", func(t *testing.T) {
		h1 := dashboardHelp1(200, nil)
		if !strings.Contains(h1, "start/stop") {
			t.Errorf("with nil map line 1 loses start/stop: %q", h1)
		}
		h2 := dashboardHelp2(200, nil)
		if !strings.Contains(h2, "tasks") {
			t.Errorf("with nil map line 2 loses the tasks: %q", h2)
		}
	})

	t.Run("line 2 is compacted instead of split", func(t *testing.T) {
		wide := dashboardHelp2(200, kb)
		narrow := dashboardHelp2(40, kb)

		if lipglossWidth(wide) > 200 || lipglossWidth(narrow) > 40 {
			t.Errorf("the lines exceed the width: %d and %d", lipglossWidth(wide), lipglossWidth(narrow))
		}
		// MEASURED: the compact line is five segments (~57 chars) and does not fit 40 columns, so trunc marks the cut with the ellipsis.
		if !strings.HasSuffix(narrow, "…") && lipglossWidth(narrow) <= 40 {
			t.Errorf("a line truncated to 40 must have …: %q", narrow)
		}
		// start/stop is deliberately absent here: it lives in help line 1, and duplicating it would rob space from the rarer shortcuts.
		for _, want := range []string{"j/k move", "enter collapse", "a ask", "t tasks", "c clear", "M stream", "L logfile", "r refresh", "1-7 tabs"} {
			if !strings.Contains(wide, want) {
				t.Errorf("the full version lost %q: %q", want, wide)
			}
		}
		if strings.Contains(wide, "start/stop") {
			t.Errorf("start/stop is in line 1: duplicating it here steals space from the less frequent shortcuts\n%q", wide)
		}
		for _, lost := range []string{"move", "collapse", "refresh", "tabs"} {
			if strings.Contains(narrow, lost) {
				t.Errorf("the compact version still has %q: the compaction is not removing anything", lost)
			}
		}
	})

	t.Run("degenerate widths do not break", func(t *testing.T) {
		for _, w := range []int{0, -5, 1} {
			if h := dashboardHelp1(w, kb); h == "" {
				t.Errorf("dashboardHelp1(%d) = empty string", w)
			}
			if h := dashboardHelp2(w, kb); h == "" {
				t.Errorf("dashboardHelp2(%d) = empty string", w)
			}
		}
	})
}

func TestHandleMouseWheelOverDetailsAndConsole(t *testing.T) {
	t.Run("over a header scrolls details", func(t *testing.T) {
		m := noSelection(t)
		if !m.detailsShown {
			t.Fatal("precondition: the details panel must be visible")
		}
		// Start from a non-zero scroll: WheelUp clamps at 0, so from 0 the test would prove nothing.
		m.detailsTop = 6
		up := m.detailsTop

		got := wheel(m, tea.MouseWheelUp)
		if got.detailsTop >= up {
			t.Errorf("the wheel up did not scroll the detail: %d -> %d", up, got.detailsTop)
		}
		got = wheel(got, tea.MouseWheelDown)
		if got.detailsTop != up {
			t.Errorf("down and up do not cancel: %d -> %d, want %d", up, got.detailsTop, up)
		}
		down := wheel(m, tea.MouseWheelDown)
		if down.detailsTop == 0 {
			t.Error("the wheel down from the top does not scroll: the panel can never be scrolled down")
		}
		if got.consoleFollow != m.consoleFollow {
			t.Error("scrolling details cannot touch the console follow")
		}
	})

	t.Run("over a project scrolls the console", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.activeTab = tabConsole
		m.consoleFollow = true

		got := wheel(m, tea.MouseWheelUp)
		if got.consoleFollow {
			t.Error("scrolling up in the console has to pause the follow: otherwise, the text moves on its own while reading")
		}
		for range 50 {
			got = wheel(got, tea.MouseWheelDown)
		}
		if !got.consoleFollow {
			t.Error("reaching the end has to resume the follow: otherwise, the log stops flowing on its own and the user has to remember it")
		}
	})

	t.Run("on a tab that is not console does nothing", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.consoleFollow = true
		for _, tab := range []tabKind{tabThreads, tabMetrics, tabGit, tabEnv, tabTimeline, tabHealth} {
			m.activeTab = tab
			got := wheel(m, tea.MouseWheelUp)
			if got.consoleFollow != true {
				t.Errorf("tab %d: the wheel changed the console follow", tab)
			}
		}
	})
}

// Driven through the stream key, not called directly: the viewport is a value, so a direct call on a Model copy would be lost.
func TestSyncConsoleViewEmptiesConsoleForWhatHasNoHistory(t *testing.T) {
	streamKey := func(t *testing.T, m Model) string {
		t.Helper()
		k := m.cfg.KeyFor("stream")
		if k == "" {
			t.Skip("no stream key in the bindings")
		}
		return k
	}

	t.Run("over the project keeps its content", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		path := pathOfSelected(t, m)
		// All three buffers are filled because the stream key cycles through them: one alone would make "empty" look like "broken".
		cs := m.consoleStateFor(path)
		cs.merged = "old service output\n"
		cs.stdout = "old service output\n"
		cs.stderr = "old service output\n"
		m.consoleFollow = true

		got, _ := press(m, streamKey(t, m))
		if !strings.Contains(tail.StripANSI(got.consoleView.View()), "old service output") {
			t.Errorf("the project console was emptied: %q", tail.StripANSI(got.consoleView.View()))
		}
	})

	t.Run("over a header shows the help text", func(t *testing.T) {
		m := noSelection(t)
		got, _ := press(m, streamKey(t, m))
		if !strings.Contains(tail.StripANSI(got.consoleView.View()), "pick a service") {
			t.Errorf("over a group = %q, want the help text", tail.StripANSI(got.consoleView.View()))
		}
	})

	t.Run("without selection empties it", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.consoleStateFor(pathOfSelected(t, m)).merged = "output from other service\n"
		m.tree = nil

		got, _ := press(m, streamKey(t, m))
		if v := tail.StripANSI(strings.TrimSpace(got.consoleView.View())); v != "" {
			t.Errorf("without selection the console = %q: it lets you see the previous service's log as if it were its own", v)
		}
	})

	t.Run("without manifest empties it", func(t *testing.T) {
		m := noManifest(t)
		got, _ := press(m, streamKey(t, m))
		if v := tail.StripANSI(strings.TrimSpace(got.consoleView.View())); v != "" {
			t.Errorf("without manifest the console = %q, want empty", v)
		}
	})
}

// -O goes only to vim: other editors reject it and the user would lose the logs.
func TestBuildEditorCmdOpensBothLogsAndOrderFollowsStream(t *testing.T) {
	// MEASURED: exec.Command puts the binary in Args[0], so editor flags must be appended after it, not before.
	cmd := buildEditorCmd("nvim", "/l/out.log", "/l/err.log", false)
	if got := strings.Join(cmd.Args, " "); got != "nvim -O /l/out.log /l/err.log" {
		t.Errorf("argv = %q, want nvim -O stdout stderr", got)
	}

	cmd = buildEditorCmd("nvim", "/l/out.log", "/l/err.log", true)
	if got := strings.Join(cmd.Args, " "); got != "nvim -O /l/err.log /l/out.log" {
		t.Errorf("with stderr active argv = %q, want stderr first", got)
	}

	for _, editor := range []string{"code", "nano", "emacs", "subl"} {
		cmd = buildEditorCmd(editor, "/l/out.log", "/l/err.log", false)
		if got := strings.Join(cmd.Args, " "); got != editor+" /l/out.log /l/err.log" {
			t.Errorf("%s received %q: -O is only for vim", editor, got)
		}
	}

	cmd = buildEditorCmd("code --wait", "/l/out.log", "/l/err.log", false)
	if got := strings.Join(cmd.Args, " "); got != "code --wait /l/out.log /l/err.log" {
		t.Errorf("argv = %q, want the editor args to be preserved", got)
	}

	cmd = buildEditorCmd("", "/l/out.log", "/l/err.log", false)
	if got := strings.Join(cmd.Args, " "); got != "nvim -O /l/out.log /l/err.log" {
		t.Errorf("without editor = %q, want the default nvim with split", got)
	}
}

// VISUAL wins over EDITOR so a per-session GUI editor beats a permanent terminal EDITOR.
func TestResolveEditorRespectsVisualThenEditor(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if got := resolveEditor(); got != "nvim" {
		t.Errorf("with nothing = %q, want nvim", got)
	}

	t.Setenv("EDITOR", "nano")
	if got := resolveEditor(); got != "nano" {
		t.Errorf("with EDITOR = %q", got)
	}

	t.Setenv("VISUAL", "code")
	if got := resolveEditor(); got != "code" {
		t.Errorf("with VISUAL and EDITOR = %q, want VISUAL: the precedence is reversed", got)
	}
}

// In every text modal q types a q and esc is the exit: quitting on q would discard what was written.
func TestAskKeyOnlyExitsWithEmptyPromptAndEscCancels(t *testing.T) {
	open := func(t *testing.T, prompt string) Model {
		t.Helper()
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.askPromptOpen = true
		m.askAgent = agents.Agent{Name: "opencode", Cmd: []string{"opencode"}}
		m.promptInput.SetValue(prompt)
		m.sizeAskPrompt()
		return m
	}

	t.Run("esc cancels and clears", func(t *testing.T) {
		m := open(t, "a draft")
		got, cmd := m.askKey(keyMsg("esc"))
		gotModel := got.(Model)
		if cmd != nil {
			t.Error("esc cannot close the program")
		}
		if gotModel.askPromptOpen {
			t.Error("esc has to close the modal")
		}
		if gotModel.promptInput.Value() != "" {
			t.Errorf("esc left the prompt with %q: when reopening it has to be cleared manually", gotModel.promptInput.Value())
		}
	})

	t.Run("q with text types a q", func(t *testing.T) {
		m := open(t, "hello")
		next, cmd := m.askKey(keyMsg("q"))
		if cmd != nil {
			t.Error("q with text cannot exit the program: what was written would be lost, and in a prompt to an agent typing q is normal")
		}
		if !next.(Model).askPromptOpen {
			t.Error("q with text closed the modal")
		}
	})

	t.Run("q with empty prompt exits", func(t *testing.T) {
		m := open(t, "")
		if _, cmd := m.askKey(keyMsg("q")); cmd == nil {
			t.Error("q with empty prompt has to exit")
		}
	})

	t.Run("ctrl+c ALWAYS exits, with or without text", func(t *testing.T) {
		// Regression: ctrl+c used to exit only on an empty ask prompt, leaving no way out without deleting the draft.
		for _, prompt := range []string{"", "send ctrl+c to the service", "a long draft"} {
			m := open(t, prompt)
			if _, cmd := m.askKey(keyMsg("ctrl+c")); cmd == nil {
				t.Errorf("ctrl+c with prompt %q has to exit the program", prompt)
			}
		}
	})
}

// Without a project there is no cwd, so the agent would start in vroom's own directory and edit vroom.
func TestDispatchAskRejectsEmptyPromptAndMissingProject(t *testing.T) {
	t.Run("empty prompt", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.askPromptOpen = true
		m.promptInput.SetValue("   \n  ") // whitespace only

		next, cmd := m.dispatchAsk()
		got := next.(Model)
		if cmd != nil {
			t.Error("an empty prompt cannot launch an agent")
		}
		if !strings.Contains(got.message, "empty prompt") {
			t.Errorf("= %q, want it to say that the prompt is empty", got.message)
		}
		if !got.askPromptOpen {
			t.Error("the modal closed: the user would have to reopen it to write")
		}
	})

	t.Run("without project", func(t *testing.T) {
		m := noSelection(t)
		m.askPromptOpen = true
		m.promptInput.SetValue("fix the bug")

		next, cmd := m.dispatchAsk()
		got := next.(Model)
		if cmd != nil {
			t.Error("without project there is no cwd to launch the agent")
		}
		if !strings.Contains(got.message, "select a service") {
			t.Errorf("= %q", got.message)
		}
	})

	t.Run("with prompt and project closes the modal", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.askPromptOpen = true
		m.askAgent = agents.Agent{Name: "opencode", Cmd: []string{"false"}}
		m.promptInput.SetValue("fix the bug")
		// inline strategy: the command exists even though no agent binary does
		m.askLauncher = launcher.New(askInlineConfig())
		m.promptInput.Focus()

		next, cmd := m.dispatchAsk()
		got := next.(Model)
		if cmd == nil {
			t.Fatal("a valid ask has to launch something")
		}
		if got.askPromptOpen {
			t.Error("the modal has to close when launching: otherwise, it overlaps with the agent")
		}
		if got.promptInput.Value() != "" {
			t.Errorf("the prompt was left with %q: when reopening the modal the previous draft appears", got.promptInput.Value())
		}
	})
}

// An unknown placeholder is left verbatim on purpose: erasing it would hide a template the user can fix.
func TestExpandAskPromptSubstitutesKnownAndLeavesUnknown(t *testing.T) {
	got := expandAskPrompt(
		"review {name} in {dir}, logs in {logs}, branch {branch}",
		"tienda-api", "/srv/api", "/var/lib/vroom/api",
	)
	for _, want := range []string{"tienda-api", "/srv/api", "/var/lib/vroom/api"} {
		if !strings.Contains(got, want) {
			t.Errorf("did not substitute %q: %q", want, got)
		}
	}
	if !strings.Contains(got, "{branch}") {
		t.Errorf("an unknown placeholder has to remain intact: %q", got)
	}

	if got := expandAskPrompt("plain text", "a", "b", "c"); got != "plain text" {
		t.Errorf("= %q", got)
	}
	if got := expandAskPrompt("x{name}y", "", "", ""); got != "xy" {
		t.Errorf("= %q, want xy", got)
	}
	if got := expandAskPrompt("{nombre}", "n", "", ""); got != "{nombre}" {
		t.Errorf("= %q, want the unknown to remain intact", got)
	}
}

// Height has three ceilings (screen, absolute cap, usable minimum): skipping any yields a modal that cannot be typed in.
func TestSizeAskPromptSizesTextareaForScreen(t *testing.T) {
	for _, tt := range []struct{ w, h int }{
		{200, 60}, {120, 40}, {100, 30}, {80, 12}, {60, 6}, {40, 3},
	} {
		m, _ := newTestModel(t)
		m.width, m.height = tt.w, tt.h
		m.updateLayout()
		m.sizeAskPrompt()

		// bubbles stores the requested width MINUS 2, which is the "> " prompt.
		wantW := askInnerW(tt.w)
		if got := m.promptInput.Width(); got != wantW-2 {
			t.Errorf("%dx%d: textarea width = %d, want %d (= askInnerW - 2 for the prompt)", tt.w, tt.h, got, wantW-2)
		}
		if got := m.promptInput.MaxHeight; got < askMinHeight {
			t.Errorf("%dx%d: textarea minimum height = %d, want >= %d", tt.w, tt.h, got, askMinHeight)
		}
		if got := m.promptInput.MaxHeight; got > askMaxHeightCap {
			t.Errorf("%dx%d: textarea height = %d, want <= %d (the cap)", tt.w, tt.h, got, askMaxHeightCap)
		}
	}
}

// The three exits are distinct: q/ctrl+c quit the program, esc closes only the modal, enter acts.
func TestPickerKeyNavigatesAndEnterLaunchesChosenItem(t *testing.T) {
	base := func() Model {
		m := newStackModel(t)
		m.pickerItems = []pickerItem{
			{Name: "build", Description: "builds"},
			{Name: "test", Description: "the tests"},
			{Name: "lint"},
		}
		m.pickerOpen = true
		return m
	}
	nav := func(m Model, key string) Model {
		next, _ := m.pickerKey(key)
		return next.(Model)
	}

	t.Run("j/k traverse the list and wrap around", func(t *testing.T) {
		got := nav(base(), "j")
		if got.pickerCursor != 1 {
			t.Errorf("j → cursor %d, want 1", got.pickerCursor)
		}
		got = nav(got, "down")
		if got.pickerCursor != 2 {
			t.Errorf("down → cursor %d, want 2", got.pickerCursor)
		}
		got = nav(got, "j")
		if got.pickerCursor != 0 {
			t.Errorf("j at the end → cursor %d, want 0 (cyclic)", got.pickerCursor)
		}
		got = nav(got, "k")
		if got.pickerCursor != 2 {
			t.Errorf("k at the beginning → cursor %d, want 2 (cyclic)", got.pickerCursor)
		}
	})

	t.Run("navigating with empty list does not crash", func(t *testing.T) {
		empty := base()
		empty.pickerItems = nil
		empty.pickerCursor = 0
		for _, k := range []string{"j", "k"} {
			if got := nav(empty, k); got.pickerCursor != 0 {
				t.Errorf("%s with empty list moved the cursor to %d", k, got.pickerCursor)
			}
		}
		if _, cmd := empty.pickerKey("enter"); cmd != nil {
			t.Error("enter with empty list cannot launch anything")
		}
	})

	t.Run("esc closes the modal without exiting the program", func(t *testing.T) {
		next, cmd := base().pickerKey("esc")
		got := next.(Model)
		if cmd != nil {
			t.Error("esc cannot close the program: only the modal")
		}
		if got.pickerOpen {
			t.Error("esc has to close the modal")
		}
	})

	t.Run("q and ctrl+c exit the program", func(t *testing.T) {
		for _, k := range []string{"q", "ctrl+c"} {
			if _, cmd := base().pickerKey(k); cmd == nil {
				t.Errorf("%s in the modal has to exit the program, like anywhere else", k)
			}
		}
	})

	t.Run("enter on a task launches the job and closes the modal", func(t *testing.T) {
		m := base()
		m.pickerCursor = 1
		m.pickerKind = pickerTasks
		next, cmd := m.pickerKey("enter")
		got := next.(Model)
		if cmd == nil {
			t.Fatal("enter on a task has to launch the job")
		}
		if got.pickerOpen {
			t.Error("the modal has to close when launching: otherwise, it overlaps with the output")
		}
	})

	t.Run("enter on an agent opens the ask prompt", func(t *testing.T) {
		m := base()
		m.pickerKind = pickerAgents
		m.pickerItems = []pickerItem{{Name: "opencode", agentCmd: []string{"opencode"}}}
		m.pickerCursor = 0
		next, _ := m.pickerKey("enter")
		got := next.(Model)
		if !got.askPromptOpen {
			t.Error("choosing an agent has to open the ask prompt")
		}
		if got.askAgent.Name != "opencode" {
			t.Errorf("askAgent = %q, want opencode: another one was chosen", got.askAgent.Name)
		}
		if got.pickerOpen {
			t.Error("choosing agent closes the picker: otherwise the two modals overlap")
		}
	})

	t.Run("unhandled keys are ignored", func(t *testing.T) {
		next, cmd := base().pickerKey("x")
		got := next.(Model)
		if cmd != nil || !got.pickerOpen || got.pickerCursor != 0 {
			t.Error("a key without action has to do nothing: neither close the modal nor move the cursor")
		}
	})
}

// Without mise.toml the message must name the project, since the cursor can be on a group.
func TestOpenPickerDistinguishesFourRejections(t *testing.T) {
	t.Run("without mise.toml says it with the name", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api") // no mise.toml here

		next, cmd := m.openPicker()
		got := next.(Model)
		if cmd != nil || got.pickerOpen {
			t.Fatal("without mise.toml the modal does not open")
		}
		if !strings.Contains(got.message, "tienda-api") {
			t.Errorf("= %q, want it to name the project: the cursor can be on a group", got.message)
		}
		if !strings.Contains(got.message, "mise.toml") {
			t.Errorf("= %q, want it to name the missing file", got.message)
		}
	})

	t.Run("with mise.toml opens the modal with its tasks", func(t *testing.T) {
		m := newJobsTestModelWithMiseOnAPI(t)
		next, cmd := m.openPicker()
		got := next.(Model)
		if cmd != nil {
			t.Error("opening the picker does not launch anything by itself")
		}
		if !got.pickerOpen {
			t.Fatalf("the modal did not open: %q", got.message)
		}
		if got.pickerKind != pickerTasks {
			t.Errorf("pickerKind = %v, want pickerTasks", got.pickerKind)
		}
		if len(got.pickerItems) == 0 {
			t.Fatal("there are tasks in the mise.toml and the picker brings none")
		}
		if got.pickerCursor != 0 {
			t.Errorf("the cursor opens at %d, want 0: repeating the modal must start from the top", got.pickerCursor)
		}
	})

	t.Run("unreadable mise.toml propagates the reason", func(t *testing.T) {
		m := newJobsTestModelWithMiseOnAPI(t)
		if err := removeFileAndMakeDir(miseTomlPath(t, m)); err != nil {
			t.Fatal(err)
		}
		next, _ := m.openPicker()
		got := next.(Model)
		if got.pickerOpen {
			t.Error("an unreadable mise.toml cannot open a tasks modal")
		}
		if got.message == "" {
			t.Error("an unreadable mise.toml has to say something: otherwise, the modal does not open and there is no explanation")
		}
	})

	t.Run("empty mise.toml distinguishes it from absence", func(t *testing.T) {
		m := newJobsTestModelWithMiseOnAPI(t)
		if err := writeFileTo(miseTomlPath(t, m), "[other]\nx = 1\n"); err != nil {
			t.Fatal(err)
		}
		next, _ := m.openPicker()
		got := next.(Model)
		if got.pickerOpen {
			t.Error("without tasks there is nothing to choose")
		}
		if !strings.Contains(got.message, "no tasks") {
			t.Errorf("= %q, want it to say that the file exists but has no tasks: they are two different fixes", got.message)
		}
	})
}

// projectByPath must return a pointer: a copy would swallow writes like m.projectByPath(p).Configured = false.
func TestProjectByPathReturnsPointerToModify(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")

	p := m.projectByPath(path)
	if p == nil {
		t.Fatalf("did not find the project %q", path)
	}
	p.Manifest.Port = 9999
	if m.projectByPath(path).Manifest.Port != 9999 {
		t.Error("projectByPath returned a copy: the change did not reach the model")
	}

	// An unknown path returns nil, not a pointer to an empty struct a caller would mistake for a hit.
	if got := m.projectByPath("/no/exists/nothing"); got != nil {
		t.Errorf("a nonexistent path returned %+v, want nil: an empty struct would make the caller believe it found something", got)
	}
}

// -1 is the out-of-range sentinel that makes callers fall back to scrolling the details panel.
func TestSelectedItemKindReturnsMinusOneWithoutItem(t *testing.T) {
	m, _ := newTestModel(t)

	m.cursor = -1
	if got := m.selectedItemKind(); got != -1 {
		t.Errorf("without item = %v, want -1", got)
	}

	m = moveCursorTo(t, m, "tienda-api")
	if got := m.selectedItemKind(); got != itemProject {
		t.Errorf("over a project = %v, want itemProject", got)
	}
	cursorOn(t, &m, "tienda")
	if got := m.selectedItemKind(); got != itemPrimary {
		t.Errorf("over a header = %v, want itemPrimary", got)
	}
}

// newJobsTestModelWithMiseOnAPI parks the cursor on tienda-web, the only project the jobs tree gives a mise.toml.
func newJobsTestModelWithMiseOnAPI(t *testing.T) Model {
	t.Helper()
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-web")
	if !mise.HasMiseToml(projectPath(t, m, "tienda-web")) {
		t.Fatalf("precondition: tienda-web should bring mise.toml, and mise.HasMiseToml denies it")
	}
	return m
}

func miseTomlPath(t *testing.T, m Model) string {
	t.Helper()
	return filepath.Join(projectPath(t, m, "tienda-web"), "mise.toml")
}

// Replacing a file with a directory is the cheap way to provoke a real read error instead of a simulated permission one.
func removeFileAndMakeDir(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return os.Mkdir(path, 0o755)
}

func writeFileTo(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

func manifestWithPort(port int) *manifest.Manifest {
	return &manifest.Manifest{Name: "p", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "./p"}}, Port: port}
}

func askInlineConfig() config.AskConfig {
	return config.AskConfig{Launcher: "inline"}
}

func wheel(m Model, b tea.MouseButton) Model {
	next, _ := m.Update(tea.MouseWheelMsg{Button: b})
	return next.(Model)
}

func keyMsg(name string) tea.KeyMsg {
	var km tea.Msg
	switch name {
	case "esc":
		km = tea.KeyPressMsg{Code: tea.KeyEsc}
	case "ctrl+c":
		km = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		km = tea.KeyPressMsg{Code: rune(name[0]), Text: name}
	}
	k, _ := km.(tea.KeyMsg)
	return k
}

// The panic key exits everywhere and text keys depend on the text: that split is why q stays a literal q inside prompts.
func TestCtrlCExitsFromAllModalsWithoutException(t *testing.T) {
	ctrlc := keyMsg("ctrl+c")

	t.Run("ask prompt with text", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.askPromptOpen = true
		m.askAgent = fakeAgent()
		m.promptInput.SetValue("a draft I don't want to lose")
		if _, cmd := m.askKey(ctrlc); cmd == nil {
			t.Error("ctrl+c with text has to exit: it is the framework's panic key")
		}
	})

	t.Run("tree filter", func(t *testing.T) {
		m, _ := newTestModel(t)
		m.filterOpen = true
		m.filterInput.SetValue("tienda")
		if _, cmd := m.filterKey(ctrlc); cmd == nil {
			t.Error("ctrl+c in the filter has to exit")
		}
	})

	t.Run("picker", func(t *testing.T) {
		m := newStackModel(t)
		m.pickerItems = []pickerItem{{Name: "build"}}
		m.pickerOpen = true
		if _, cmd := m.pickerKey("ctrl+c"); cmd == nil {
			t.Error("ctrl+c in the picker has to exit")
		}
	})

	t.Run("main view", func(t *testing.T) {
		m, _ := newTestModel(t)
		if _, cmd := m.handleKey(ctrlc); cmd == nil {
			t.Error("ctrl+c in the main view has to exit")
		}
	})

	t.Run("q is NOT the panic key", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.askPromptOpen = true
		m.promptInput.SetValue("search for this error: q is not a problem")
		if _, cmd := m.askKey(keyMsg("q")); cmd != nil {
			t.Error("q with text written cannot exit: losing a prompt to a q would be worse than the minus key")
		}
	})
}
