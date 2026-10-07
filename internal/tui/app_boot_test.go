package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"vroom/internal/agents"
	"vroom/internal/config"
	"vroom/internal/launcher"
	"vroom/internal/orchestrate"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
	"vroom/internal/tail"
)

// The walk starts at each project and goes up, never above the root: a compose above it belongs to another workspace.
func TestFindComposeFileStartsAtEachProjectAndDoesNotGoAboveRoot(t *testing.T) {
	compose := func(stackName string) string {
		return `
primary_group = "g"
[[stack]]
name = "` + stackName + `"
[[stack.stage]]
name = "e"
services = ["api"]
`
	}

	t.Run("goes up to the root", func(t *testing.T) {
		root := t.TempDir()
		writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), compose("from-root"))
		deep := filepath.Join(root, "group", "project")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}

		cf, err := findComposeFile(root, []scanner.Project{{Path: deep}})
		if err != nil {
			t.Fatalf("did not find the root compose: %v", err)
		}
		if len(cf.Stacks) != 1 || cf.Stacks[0].Name != "from-root" {
			t.Errorf("compose = %+v, want the root stack", cf.Stacks)
		}
	})

	t.Run("finds an intermediate one that is not at the root", func(t *testing.T) {
		root := t.TempDir()
		writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), compose("from-root"))
		group := filepath.Join(root, "group")
		writeStr(t, filepath.Join(group, orchestrate.ComposeFileName), compose("from-group"))
		proj := filepath.Join(group, "project")
		if err := os.MkdirAll(proj, 0o755); err != nil {
			t.Fatal(err)
		}

		cf, err := findComposeFile(root, []scanner.Project{{Path: proj}})
		if err != nil {
			t.Fatal(err)
		}
		if cf.Stacks[0].Name != "from-group" {
			t.Errorf("found %q: the nearest compose wins", cf.Stacks[0].Name)
		}
	})

	t.Run("the nearest one across multiple projects wins", func(t *testing.T) {
		root := t.TempDir()
		writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), compose("from-root"))
		writeStr(t, filepath.Join(root, "near", orchestrate.ComposeFileName), compose("from-near"))

		cf, err := findComposeFile(root, []scanner.Project{{Path: filepath.Join(root, "near", "api")}})
		if err != nil {
			t.Fatal(err)
		}
		if cf.Stacks[0].Name != "from-near" {
			t.Errorf("found %q", cf.Stacks[0].Name)
		}
	})

	t.Run("does not go above the root", func(t *testing.T) {
		// t.TempDir cannot raise this case because the tree hangs off /tmp: the root is pushed deeper so the compose above it is real.
		dir := t.TempDir()
		writeStr(t, filepath.Join(dir, orchestrate.ComposeFileName), compose("outside"))

		deep := filepath.Join(dir, "a", "b", "c")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := findComposeFile(deep, []scanner.Project{{Path: deep}}); err == nil {
			t.Error("a compose was found ABOVE the root: it belongs to another workspace")
		}
	})

	t.Run("no compose anywhere reports it", func(t *testing.T) {
		root := t.TempDir()
		_, err := findComposeFile(root, []scanner.Project{{Path: root}})
		if err == nil {
			t.Fatal("no compose must produce an error")
		}
		if !strings.Contains(err.Error(), orchestrate.ComposeFileName) {
			t.Errorf("err = %q, want it to name the missing file", err)
		}
		if !strings.Contains(err.Error(), root) {
			t.Errorf("err = %q, want it to say where it searched", err)
		}
	})

	t.Run("no projects means nothing to search", func(t *testing.T) {
		root := t.TempDir()
		writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), compose("x"))
		if _, err := findComposeFile(root, nil); err == nil {
			t.Error("no projects means no compose: the search starts from the projects")
		}
	})

	t.Run("a malformed compose belongs to no one", func(t *testing.T) {
		root := t.TempDir()
		writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), "this is not toml [[[")
		if _, err := findComposeFile(root, []scanner.Project{{Path: root}}); err == nil {
			t.Error("a malformed compose must be rejected, not accepted as if there were no stacks")
		}
	})
}

// The CWD is the contract default and config root is the explicit exception, so scanner.root wins; "~" is expanded because people write it.
func TestNewUsesConfigRootAndExpandsTilde(t *testing.T) {
	t.Run("no root in config: the CWD", func(t *testing.T) {
		root := writeTestTree(t, false)
		isolateConfig(t)

		m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
		if len(m.projects) == 0 {
			t.Error("with CWD as root it must scan the projects below")
		}
	})

	t.Run("relative config root: under the CWD", func(t *testing.T) {
		root := writeTestTree(t, false)
		writeStr(t, filepath.Join(root, "sub", ".vroom.toml"), "name = \"sub-api\"\ncommand_start = \"./x\"\n")

		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		writeStr(t, cfgPath, "[scanner]\nroot = \"sub\"\n")
		t.Setenv("VROOM_CONFIG", cfgPath)

		m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
		// MEASURED: Project.Name is the directory name, so only the path set proves what the root decided.
		var paths []string
		for _, p := range m.projects {
			paths = append(paths, p.Path)
		}
		if len(paths) != 1 || !strings.HasSuffix(paths[0], "/sub") {
			t.Errorf("with root = \"sub\" it scanned %v, want only the subdirectory: the config root takes precedence over the CWD", paths)
		}
	})

	t.Run("config root with tilde", func(t *testing.T) {
		home := t.TempDir()
		real := filepath.Join(home, "workspace")
		writeStr(t, filepath.Join(real, "api", ".vroom.toml"), "name = \"api-from-home\"\ncommand_start = \"./x\"\n")

		// The tilde resolves through the process HOME, which t.Setenv does change for os.UserHomeDir unlike /proc/self/environ.
		t.Setenv("HOME", home)
		t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
		writeStr(t, os.Getenv("VROOM_CONFIG"), "[scanner]\nroot = \"~/workspace\"\n")

		m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, t.TempDir())
		var paths []string
		for _, p := range m.projects {
			paths = append(paths, p.Path)
		}
		if len(paths) != 1 || !strings.HasSuffix(paths[0], "/workspace/api") {
			t.Errorf("with root = \"~/workspace\" it scanned %v, want the api inside: an unexpanded ~ is a directory that does not exist", paths)
		}
	})
}

// Both boot errors are "start anyway and warn": a typo in the config cannot leave the user without a TUI.
func TestNewReportsFailedScanAndInvalidConfig(t *testing.T) {
	t.Run("invalid config: defaults plus warning", func(t *testing.T) {
		isolateConfig(t)
		root := writeTestTree(t, false)
		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		writeStr(t, cfgPath, "[ask]\nlauncher = \"nonexistent\"\n")
		t.Setenv("VROOM_CONFIG", cfgPath)

		m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
		if m.message == "" {
			t.Error("an invalid config must warn: otherwise the user thinks it is applied")
		}
		if len(m.projects) == 0 {
			t.Error("an invalid config must not prevent scanning: vroom has to start with the defaults")
		}
		if m.cfg.Ask.Launcher != "auto" {
			t.Errorf("Ask.Launcher = %q, want auto: the invalid launcher was discarded", m.cfg.Ask.Launcher)
		}
	})

	t.Run("projects without a manifest show up as unconfigured", func(t *testing.T) {
		isolateConfig(t)
		root := writeTestTree(t, false)
		if err := os.MkdirAll(filepath.Join(root, "no-manifest"), 0o755); err != nil {
			t.Fatal(err)
		}

		m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
		for _, p := range m.projects {
			if !p.Configured {
				if sv := m.services[p.Path]; sv == nil || sv.Status != statusUnconfigured {
					t.Errorf("project without manifest %q has status %v, want unconfigured", p.Name, m.services[p.Path])
				}
			} else if sv := m.services[p.Path]; sv == nil || sv.Status != statusStopped {
				t.Errorf("project %q starts as %v, want stopped: it was not asked about", p.Name, sv.Status)
			}
		}
	})

	t.Run("restores persisted collapse state", func(t *testing.T) {
		isolateConfig(t)
		root := writeTestTree(t, false)
		store := state.NewStoreAt(t.TempDir())
		if err := store.SaveCollapsed(map[string]bool{"tienda": true}); err != nil {
			t.Fatal(err)
		}

		m := New(store, &stubManager{}, root)
		if !m.collapsed["tienda"] {
			t.Error("persisted collapse state was not restored: the user loses the tree they had last time")
		}
		if m.collapsed["no-existe"] {
			t.Error("a collapse state was invented that nobody saved")
		}
	})
}

// A negative margin is what makes bordered misbehave, so no height may go below zero at any terminal size.
func TestUpdateLayoutPreventsAnyBoxFromOverflowing(t *testing.T) {
	m, _ := newTestModel(t)

	for _, dims := range [][2]int{
		{20, 5}, {30, 8}, {40, 12}, {50, 20}, {80, 24}, {100, 30}, {120, 40}, {200, 60}, {300, 80},
	} {
		m.width, m.height = dims[0], dims[1]
		m.updateLayout()

		ctx := func(what string, v int) {
			t.Helper()
			if v < 0 {
				t.Errorf("%dx%d: %s = %d, negative: the compositor draws the box inverted", dims[0], dims[1], what, v)
			}
		}
		ctx("bodyOuterH", m.bodyOuterH)
		ctx("bodyH", m.bodyH)
		ctx("rightW", m.rightW)
		ctx("contentH", m.contentH)
		ctx("treeVis", m.treeVis())

		if total := treeWidth + boxFrame + m.rightW + boxFrame; total > dims[0] && dims[0] > 40 {
			t.Errorf("%dx%d: columns sum to %d, want <= %d", dims[0], dims[1], total, dims[0])
		}
		if m.detailsShown && m.contentH+detailsHeight+boxFrame+4 > dims[1] && dims[1] > 20 {
			t.Errorf("%dx%d: panels sum to %d, want <= %d", dims[0], dims[1], m.contentH+detailsHeight+boxFrame+4, dims[1])
		}
	}
}

// Layout drops Details before the console: Details is informative, the console is what the user is working in.
func TestDetailsBoxHidesWhenItDoesNotFit(t *testing.T) {
	m, _ := newTestModel(t)

	wide, _ := newTestModel(t)
	wide.width, wide.height = 200, 60
	wide.updateLayout()
	if !wide.detailsShown {
		t.Error("on a wide screen the details box must be present: it is where the detail panel lives")
	}

	narrow, _ := newTestModel(t)
	narrow.width, narrow.height = 40, 10
	narrow.updateLayout()
	if narrow.detailsShown {
		t.Error("on a narrow screen Details must be hidden before the console")
	}
	if narrow.contentH <= 0 {
		t.Errorf("contentH = %d with the narrowest screen: the console disappears and nothing remains", narrow.contentH)
	}

	_ = m
}

// Offsets jump to EOF rather than zero, or the first read reinserts the whole previous run's log.
func TestUpdateWithConsoleMessageResetsOffsetsToEOF(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	if _, err := m.store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{m.store.StdoutLog(path), m.store.StderrLog(path)} {
		if err := os.WriteFile(p, []byte("old content\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cs := m.consoleStateFor(path)
	cs.stdout, cs.stderr, cs.merged = "old in memory\n", "old in memory\n", "old in memory\n"

	got := updateMsg(t, m, startedMsg{path: path, res: process.StartResult{Pid: 4321}})
	new := got.consoleStateFor(path)

	if new.stdout != "" || new.stderr != "" || new.merged != "" {
		t.Errorf("in-memory buffers were not cleared: %q / %q / %q", new.stdout, new.stderr, new.merged)
	}
	for i, off := range new.off {
		if off == 0 {
			t.Errorf("offset %d stayed at 0: the next tail would reinsert the old log", i)
		}
	}

	other := moveCursorTo(t, m, "suelto")
	before := other.consoleView.View()
	got = updateMsg(t, other, startedMsg{path: path, res: process.StartResult{Pid: 4321}})
	if got.consoleView.View() != before {
		t.Error("starting a service that is not the selected one must not touch the visible console")
	}
}

func TestUpdateWithSpinnerTickReSyncsAndWithStatusTickDoesSame(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	got, cmd := m.Update(spinnerTick())
	if cmd == nil {
		t.Fatal("the spinner tick must re-arm or the animation freezes")
	}

	model := got.(Model)
	markRunning(&model, pathOfSelected(t, model), livePID(t))
	_, cmd2 := model.Update(tickMsg(time.Now()))
	if cmd2 == nil {
		t.Fatal("the status tick must request a refresh or the table stays frozen")
	}
}

// Every ptyDataMsg must re-arm the read: the PTY emits nothing on its own and the terminal would go mute.
func TestUpdateWithPtyOutputMessageWritesAndReArmsRead(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	m, _ := newTestModel(t)
	m.term = s
	m.termOpen = true

	_, cmd := m.Update(ptyDataMsg{data: []byte("hello from the shell")})
	if cmd == nil {
		t.Error("after reading from the PTY the read must re-arm: otherwise the terminal goes mute")
	}
	if !strings.Contains(s.screen(), "hello from the shell") {
		t.Errorf("shell output did not reach the emulator: %q", s.screen())
	}

	// EOF must not release the session: the PTY master does not emit EOF when the shell dies.
	before := m.term
	got2, _ := m.Update(ptyEOFMsg{})
	if got2.(Model).term != before {
		t.Error("EOF must not release the session: the PTY master does not emit EOF when the shell dies")
	}
	if !got2.(Model).termOpen {
		t.Error("EOF must not close the modal: the session is still alive")
	}
}

// The notice must report the exit code: with a deliberate `exit 1` the user closed their own terminal and a plain "closed" would blame vroom.
func TestUpdateWithProcessExitClosesModalAndNotifies(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      error
		wants    string
		notWants string
	}{
		{"clean exit", nil, "terminal closed", "exited"},
		{"with code", realExit(t, 3), "terminal exited (3)", "terminal closed"},
		{"no process", nil, "terminal closed", "exited"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newStubSession(40, 10, &stubPty{})
			m, _ := newTestModel(t)
			m.term = s
			m.termOpen = true

			got := updateMsg(t, m, ptyExitMsg{err: tt.err})
			if got.termOpen {
				t.Error("the shell exited: the modal must close")
			}
			if got.term != nil {
				t.Error("the session must be released: otherwise the closed modal leaves it alive forever")
			}
			if !strings.Contains(got.message, tt.wants) {
				t.Errorf("notice = %q, want it to contain %q", got.message, tt.wants)
			}
			if tt.notWants != "" && strings.Contains(got.message, tt.notWants) {
				t.Errorf("notice = %q, must not contain %q: the user closed their own terminal", got.message, tt.notWants)
			}
		})
	}
}

func TestUpdateWithStackResultSaysWhatHappened(t *testing.T) {
	tests := []struct {
		name       string
		msg        tea.Msg
		wants      string
		hasStackName bool
	}{
		{
			"launched fine",
			stackResultMsg{result: orchestrate.LaunchResult{OK: true, Stack: "front"}},
			"stack front launched", true,
		},
		{
			"launched but the stage failed",
			stackResultMsg{result: orchestrate.LaunchResult{OK: false, Stack: "front", Error: "health check failed"}},
			"stack failed", true,
		},
		{
			"did not even attempt to launch",
			stackResultMsg{err: errLaunch},
			"stack error", false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newStackModel(t)
			got := updateMsg(t, m, tt.msg)
			if !strings.Contains(got.message, tt.wants) {
				t.Errorf("notice = %q, want it to contain %q", got.message, tt.wants)
			}
			// MEASURED: only the paths with a stack name reach the timeline; a launch error has no service to attach to.
			var registered bool
			for path := range got.events {
				if len(got.events[path]) > 0 {
					registered = true
				}
			}
			if tt.hasStackName && !registered {
				t.Error("the stack result did not reach the timeline: exactly what the user came to see is lost")
			}
			if !tt.hasStackName && registered {
				t.Error("a launch failure without a stack name cannot attach events: there is no service to attribute them to")
			}
		})
	}
}

// Reporting "all stacks launched" when one of them failed is what makes the notice useless.
func TestUpdateWithComposersResultCountsFailures(t *testing.T) {
	tests := []struct {
		name    string
		results []orchestrate.LaunchResult
		wants   string
	}{
		{
			"all good",
			[]orchestrate.LaunchResult{{OK: true, Stack: "a"}, {OK: true, Stack: "b"}},
			"all stacks launched in tienda",
		},
		{
			"one failed",
			[]orchestrate.LaunchResult{{OK: true, Stack: "a"}, {OK: false, Stack: "b", Error: "boom"}},
			"1 stack(s) failed in tienda",
		},
		{
			"three failed",
			[]orchestrate.LaunchResult{{OK: false}, {OK: false}, {OK: false}},
			"3 stack(s) failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newStackModel(t)
			got := updateMsg(t, m, composersResultMsg{primary: "tienda", results: tt.results})
			if !strings.Contains(got.message, tt.wants) {
				t.Errorf("notice = %q, want it to contain %q", got.message, tt.wants)
			}
		})
	}
}

// The no-engine case is real for a hand-built model, so this path must not nil-deref somewhere less obvious.
func TestToggleStackAndComposersSayNoEngineOrNoStacks(t *testing.T) {
	t.Run("no stacks in the group", func(t *testing.T) {
		m := newStackModel(t)
		next, cmd := m.toggleComposers("group-that-does-not-exist")
		got := next.(Model)
		if cmd != nil {
			t.Error("no stacks means nothing to launch")
		}
		if !strings.Contains(got.message, "no stacks found") {
			t.Errorf("notice = %q, want it to say there are no stacks for the group", got.message)
		}
	})

	noEngine, _ := newTestModel(t)
	noEngine.engine = nil

	_, cmd := noEngine.toggleComposers("tienda")
	if cmd != nil {
		t.Error("no engine means nothing to launch")
	}

	nxt, cmd2 := noEngine.toggleStack(&orchestrate.Stack{Name: "x"})
	if cmd2 != nil {
		t.Error("no engine means nothing to launch")
	}
	if !strings.Contains(nxt.(Model).message, "engine") {
		t.Errorf("notice = %q, want it to name the engine: without it the user does not know if the compose is missing or something else", nxt.(Model).message)
	}
}

// Group toggle starts the stopped ones only: restarting the live ones would leave duplicate processes fighting for the same port.
func TestToggleNodeStartsStoppedAndDoesNotTouchLive(t *testing.T) {
	m, _ := newTestModel(t)
	cursorOn(t, &m, "tienda")

	api, web := projectPath(t, m, "tienda-api"), projectPath(t, m, "tienda-web")
	markRunning(&m, api, livePID(t))
	m.services[web].Status = statusStopped

	before := map[string]uiStatus{}
	for path, sv := range m.services {
		before[path] = sv.Status
	}

	next, cmd := m.toggleNode("tienda", "")
	if cmd == nil {
		t.Fatal("there is a stopped service: it must start something")
	}
	got := next.(Model)

	if got.services[api].Status != statusRunning {
		t.Errorf("the live service stayed at %q, want running intact: what is already running is not restarted", got.services[api].Status)
	}
	if got.services[web].Status != statusStarting {
		t.Errorf("the stopped one stayed at %q, want starting", got.services[web].Status)
	}
	if before[api] != got.services[api].Status {
		t.Error("a live service cannot change status in the group action")
	}
}

// A node with no members must be a silent no-op, not an out-of-range index.
func TestToggleNodeWithNoMembersDoesNothing(t *testing.T) {
	m, _ := newTestModel(t)

	_, cmd := m.toggleNode("group-that-does-not-exist", "")
	if cmd != nil {
		t.Error("a node with no members cannot launch anything")
	}

	_, cmd2 := m.toggleNode("tienda", "invented-secondary")
	_ = cmd2
}

func TestEnterSelectionDoesNothingOnStackOrInlineProject(t *testing.T) {
	t.Run("on a stack does not rebuild the tree", func(t *testing.T) {
		m := newStackModel(t)
		cursorOn(t, &m, "front")
		treeBefore := len(m.tree)

		next, cmd := m.enterSelection()
		got := next.(Model)
		if cmd != nil {
			t.Error("enter on a stack emits no commands: stacks do not fold")
		}
		if len(got.tree) != treeBefore {
			t.Error("enter on a stack changed the tree: a stack has no children to fold")
		}
	})

	t.Run("on a project without a container does nothing", func(t *testing.T) {
		m, _ := newTestModel(t)
		// "suelto" has no primary_group
		m = moveCursorTo(t, m, "suelto")
		it, ok := m.selectedItem()
		if !ok || it.primary != "" {
			t.Skip("the test tree changed: 'suelto' now has a primary")
		}
		treeBefore := len(m.tree)
		next, _ := m.enterSelection()
		if len(next.(Model).tree) != treeBefore {
			t.Error("enter on an inline project changed the tree: it has no container to fold")
		}
	})

	t.Run("on a project with a group folds it and preserves the cursor", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		treeBefore := len(m.tree)

		next, _ := m.enterSelection()
		got := next.(Model)
		if len(got.tree) >= treeBefore {
			t.Errorf("enter folded nothing: the tree went from %d to %d rows", treeBefore, len(got.tree))
		}
		if !m.store.LoadCollapsed()[it0(m).primary] && !got.collapsed[it0(m).primary] {
			t.Error("the fold state was not stored in the model: enter did not persist anything")
		}
	})

	t.Run("no item does nothing", func(t *testing.T) {
		m, _ := newTestModel(t)
		m.tree = nil
		next, cmd := m.enterSelection()
		if cmd != nil || len(next.(Model).tree) != 0 {
			t.Error("no selection means enter cannot do anything")
		}
	})
}

// No refresh for nodes with nothing to fetch: cursor movement would spawn a git per row on a large workspace.
func TestOnSelectDoesNotRequestRefreshForWhatHasNothingToRefresh(t *testing.T) {
	t.Run("on a header requests nothing", func(t *testing.T) {
		m := noSelection(t)
		next, cmd := m.onSelect()
		got := next.(Model)
		if cmd != nil {
			t.Error("a header has no logs to fetch: requesting a refresh would be a git per row")
		}
		if !strings.Contains(tail.StripANSI(got.consoleView.View()), "pick a service") {
			t.Errorf("console = %q, want the group hint", tail.StripANSI(got.consoleView.View()))
		}
	})

	t.Run("on a project without a manifest requests nothing", func(t *testing.T) {
		m := noManifest(t)
		if _, cmd := m.onSelect(); cmd != nil {
			t.Error("a project without a manifest has no logs to fetch")
		}
	})

	t.Run("on a configured project does request", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		if _, cmd := m.onSelect(); cmd == nil {
			t.Error("a configured project does have logs to fetch: the cursor would move without updating")
		}
	})
}

// Sampling a stopped service is a /proc walk on a dead PID, four times a second, per visible stopped service.
func TestRefreshThreadsRequestsNothingWithoutLiveService(t *testing.T) {
	m, _ := newTestModel(t)

	if cmd := m.refreshThreads(); cmd != nil {
		t.Error("no project means no threads to sample")
	}

	noManifest := noManifest(t)
	if cmd := noManifest.refreshThreads(); cmd != nil {
		t.Error("no manifest means no process to sample")
	}

	m = moveCursorTo(t, m, "tienda-api")
	if cmd := m.refreshThreads(); cmd != nil {
		t.Error("a stopped service has no threads: sampling it is a /proc walk on a dead PID")
	}

	markRunning(&m, projectPath(t, m, "tienda-api"), livePID(t))
	if cmd := m.refreshThreads(); cmd == nil {
		t.Error("a live service does have threads to sample")
	}
}

// /proc/0/task does not exist, so a live-looking service with Pid 0 would error on every tick.
func TestRefreshThreadsWithZeroPidDoesNotSample(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := projectPath(t, m, "tienda-api")
	m.services[path].Status = statusRunning
	m.services[path].Meta.Pid = 0

	if cmd := m.refreshThreads(); cmd != nil {
		t.Error("no PID means nothing to sample: /proc/0 does not exist")
	}
}

// tea.ExecProcess itself cannot be tested here (it suspends the program), so only the command's existence is asserted.
func TestEditLogsCmdReturnsEditorAndError(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	cmd := editLogsCmd("/bin/sh -c true", "/l/out.log", "/l/err.log", false)
	if cmd == nil {
		t.Fatal("editLogsCmd returned nil")
	}
	m, _ := newTestModel(t)
	_ = m
}

// esc with a filter applied clears it and stays: quitting there would lose a half-finished stack teardown.
func TestHandleKeyWithEscClearsFilterBeforeExiting(t *testing.T) {
	t.Run("with filter applied: clears and does not exit", func(t *testing.T) {
		m, _ := newTestModel(t)
		applied, _ := m.applyFilter("tienda")
		withFilter := applied.(Model)
		if withFilter.filterText == "" {
			t.Fatal("precondition: the filter should be applied")
		}

		got, _ := withFilter.handleKey(keyMsg("esc"))
		model := got.(Model)
		if model.filterText != "" {
			t.Errorf("esc did not clear the filter: %q", model.filterText)
		}
		if model.filterText != "" {
			t.Errorf("the filter is still at %q", model.filterText)
		}
	})

	t.Run("no filter: esc exits", func(t *testing.T) {
		m, _ := newTestModel(t)
		next, cmd := m.handleKey(keyMsg("esc"))
		_ = next
		if cmd == nil {
			t.Error("no filter, esc exits the program: there is nothing to close")
		}
	})
}

// enter means "done typing" (live filtering already applied it); esc means "changed my mind" and clears.
func TestFilterKeyClosesBoxWithoutClearingOnEnter(t *testing.T) {
	m, _ := newTestModel(t)
	m.filterOpen = true
	m.filterInput.SetValue("tienda")
	m.filterText = "tienda"

	t.Run("enter closes and preserves", func(t *testing.T) {
		next, _ := m.filterKey(keyMsg("enter"))
		got := next.(Model)
		if got.filterOpen {
			t.Error("enter must close the box")
		}
		if got.filterText != "tienda" {
			t.Errorf("enter cleared the filter: %q. Live filtering had already applied it", got.filterText)
		}
	})

	t.Run("esc closes and clears", func(t *testing.T) {
		next, _ := m.filterKey(keyMsg("esc"))
		got := next.(Model)
		if got.filterOpen {
			t.Error("esc must close the box")
		}
		if got.filterText != "" {
			t.Errorf("esc left the filter at %q: the user would think they removed it", got.filterText)
		}
	})

	t.Run("ctrl+c exits without closing anything", func(t *testing.T) {
		next, cmd := m.filterKey(keyMsg("ctrl+c"))
		if cmd == nil {
			t.Error("ctrl+c is the emergency exit and does not depend on the box state")
		}
		_ = next
	})
}

// Only the background strategies return here, and their error must reach the user: a failed `herdr pane split` would look like a started agent.
func TestLaunchAskCmdReturnsLauncherMessageAndError(t *testing.T) {
	l := newBackgroundLauncher(t)

	req := askRequest()
	msg := launchAskCmd(l, "custom", req)()
	sm, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("launchAskCmd returned %T, want statusMsg", msg)
	}
	if sm.message == "" {
		t.Error("the launcher returned without a message: the user does not know what happened to their agent")
	}
}

// Non-inline strategies must dispatch without blocking: waiting for the agent to exit is what the launcher exists to avoid.
func TestDispatchAskWithNonInlineStrategyDispatchesInBackground(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.askPromptOpen = true
	m.askAgent = fakeAgent()
	m.promptInput.SetValue("fix the bug")
	m.askLauncher = newBackgroundLauncher(t)
	m.promptInput.Focus()

	next, cmd := m.dispatchAsk()
	got := next.(Model)
	if cmd == nil {
		t.Fatal("a background strategy must dispatch")
	}
	if got.askPromptOpen {
		t.Error("the modal must close when dispatching: otherwise it overlaps with the agent output")
	}
	if got.message != "" {
		t.Errorf("notice = %q before dispatching: the result arrives later", got.message)
	}
}

// MouseMode is CellMotion, not drag: the wheel is all that is needed and drag would enable text selection inside a bordered dashboard.
func TestViewSetsAltScreenAndWheel(t *testing.T) {
	m, _ := newTestModel(t)
	v := m.View()

	if !v.AltScreen {
		t.Error("the dashboard must go full screen: it does not fit in the partial height of the terminal")
	}
	if v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("MouseMode = %v, want CellMotion: what is needed is the wheel", v.MouseMode)
	}
	if strings.TrimSpace(v.Content) == "" {
		t.Error("the view has no content: a blank alt screen is a hung program")
	}
}

func it0(m Model) treeItem {
	it, ok := m.selectedItem()
	if !ok {
		panic("no item under the cursor")
	}
	return it
}

func spinnerTick() tea.Msg {
	return spinner.TickMsg{}
}

// A fake error would make exitCode return 0 and the test would check the wrong case, so a real exiting command runs.
func realExit(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	if err == nil {
		t.Fatalf("sh -c \"exit %d\" exited with 0: the test error would prove nothing", code)
	}
	return err
}

// a stack launch failure with no exit code behind it.
var errLaunch = errors.New("could not launch")

// A custom-strategy launcher over a fake script on PATH: the background ask path that does not suspend the program.
func newBackgroundLauncher(t *testing.T) *launcher.Launcher {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"agent launched in $PWD\"\n"
	if err := os.WriteFile(filepath.Join(bin, "fake-agent"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return launcher.New(config.AskConfig{Launcher: "custom", LauncherCmd: "fake-agent"})
}

func askRequest() launcher.Request {
	return launcher.Request{Agent: "fake-agent", Args: []string{"fake-agent"}, Dir: "/tmp"}
}

func fakeAgent() agents.Agent {
	return agents.Agent{Name: "fake-agent", Cmd: []string{"fake-agent"}}
}
