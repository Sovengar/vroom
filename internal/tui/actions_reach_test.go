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

type unknownMsg struct{ something int }

func TestUpdateWithUnknownMessageReturnsModelIntact(t *testing.T) {
	m, _ := newTestModel(t)
	before := m

	new, cmd := m.Update(unknownMsg{something: 1})
	if cmd != nil {
		t.Error("an unknown message cannot return a command: the command would be the TUI's, " +
			"not ours, and there is none")
	}
	got, ok := new.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", new)
	}
	if got.message != before.message {
		t.Errorf("message changed from %q to %q: a message that is not ours cannot speak", before.message, got.message)
	}
	if got.cursor != before.cursor {
		t.Errorf("cursor moved from %d to %d without any key", before.cursor, got.cursor)
	}
}

// The editor is never launched here: tea.ExecProcess returns its ExecMsg without starting anything.
func TestLogsActionOpensEditorWithProjectSelected(t *testing.T) {
	// Remapped to a free key because the default collides with another binding.
	m, store := newTestModelWithConfig(t, "[keybindings]\nlogs = \"y\"\n")

	p := firstConfiguredProject(t, m)
	m = selectProject(t, m, p.Path)

	_, cmd := m.Update(key("y"))
	if cmd == nil {
		t.Fatal("the logs key must return the editor command")
	}
	// ExecMsg is private to bubbletea, so the only thing assertable here is that a message came back; nil would skip suspending the TUI and the editor would open on top of it.
	msg := cmd()
	if msg == nil {
		t.Fatal("logs returned a command that produces no message: the TUI would not suspend and the " +
			"editor would open on top of the interface")
	}
	// Editor choice and arguments are asserted in TestBuildEditorCmd, where the *exec.Cmd is inspectable.
	_ = store
}

func TestRefreshActionLaunchesRefreshWithoutChangingView(t *testing.T) {
	m, _ := newTestModelWithConfig(t, "[keybindings]\nrefresh = \"y\"\n")
	m.activeTab = tabThreads
	before := m.activeTab

	new, cmd := m.Update(key("y"))
	if cmd == nil {
		t.Fatal("refresh must return the refresh batch")
	}
	got, ok := new.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", new)
	}
	if got.activeTab != before {
		t.Errorf("refresh changed the tab from %d to %d: refreshing is not switching views", before, got.activeTab)
	}
	if got.message != "" {
		t.Errorf("message = %q after a correct refresh, want empty", got.message)
	}
}

// The tab guard exists so a resize outside the console leaves no stale buffer for the console to show later.
func TestSyncConsoleViewDoesNothingOutsideConsoleTab(t *testing.T) {
	m, _ := newTestModel(t)
	m.activeTab = tabGit

	before := m.consoleView.View()
	if cmd := m.syncConsoleView(); cmd != nil {
		t.Error("syncConsoleView outside the console cannot return a command: there is nothing to read")
	}
	if after := m.consoleView.View(); after != before {
		t.Errorf("the viewport changed outside the console tab:\n-- before --\n%s\n-- after --\n%s",
			before, after)
	}
}

// Unreachable through the UI (tree items always carry a stack), but toggleStack(nil) would nil-deref on the start/stop path.
func TestToggleOnStackWithoutStackDoesNotPanic(t *testing.T) {
	m, _ := newTestModel(t)
	m.tree = []treeItem{{kind: itemStack, primary: "empty"}}
	m.cursor = 0

	new, cmd := m.toggleSelected()
	if cmd != nil {
		t.Error("a stack without a stack cannot start anything")
	}
	if new == nil {
		t.Fatal("Update returned nil")
	}
}

// MEASURED: a directory with no .vroom.toml never enters the scan, so a broken manifest is the only way to get an unconfigured project; the message must name the manifest, not say "you can't".
func TestRestartOnProjectWithBrokenManifestSaysSo(t *testing.T) {
	m, _ := newTestModel(t)

	m, broken := brokenProject(t, m)
	m = selectProject(t, m, broken.Path)

	// The default restart key is capital "R" because it is the only stop-then-start action, so it stays apart from "r".
	new, _ := m.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	got, ok := new.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", new)
	}
	if !strings.Contains(got.message, "No manifest") {
		t.Errorf("message = %q, want it to say the manifest is missing: the message has to "+
			"distinguish \"there is nothing to restart\" from \"you can't\"", got.message)
	}
	if sv := got.services[broken.Path]; sv != nil && sv.Status == statusStopping {
		t.Error("the service transitioned to stopping with a broken manifest: there is nothing to stop")
	}
}

// Kept out of writeTestTree because the rest of the suite assumes every project starts and its row counts depend on that.
func brokenProject(t *testing.T, m Model) (Model, scanner.Project) {
	t.Helper()
	path := filepath.Join(m.projects[0].Path, "broken")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	// Invalid TOML on purpose: the trailing comma after the value.
	if err := os.WriteFile(filepath.Join(path, ".vroom.toml"), []byte("name = \"broken\",\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := filepath.Dir(m.projects[0].Path)
	sig, err := scanner.Scan(root, config.Defaults().Scanner.Depth)
	if err != nil {
		t.Fatal(err)
	}
	var broken scanner.Project
	for _, p := range sig.Projects {
		if filepath.Base(p.Path) == "broken" {
			broken = p
			break
		}
	}
	if broken.Path == "" {
		t.Fatal("the scan did not bring the newly created project")
	}

	if broken.Configured {
		t.Fatalf("project %s has Configured=true with an invalid manifest: this test "+
			"is not testing the case it intended to test", broken.Path)
	}

	// The whole model is rebuilt because what changes here is the project list, not a status.
	m2 := New(m.store, &stubManager{}, root)
	m2.width, m2.height = m.width, m.height
	m2.updateLayout()
	return m2, broken
}

// A group can carry members the model does not know, so without the continue a nil sv would nil-deref mid group start.
func TestToggleGroupIgnoresMembersWithoutKnownState(t *testing.T) {
	m, _ := newTestModel(t)

	idx := -1
	for i, e := range m.tree {
		if e.kind == itemPrimary || e.kind == itemSecondary {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Skip("the test tree has no groups")
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
		t.Fatal("the test tree must bring projects")
	}
	delete(m.services, target)

	new, _ := m.toggleSelected()
	if new == nil {
		t.Fatal("toggleSelected returned nil: a member without state cannot take down the group")
	}
}

// Only the selected member's console is cleared: emptying a service nobody is looking at would discard its history for nothing.
func TestToggleGroupClearsConsoleOfSelectedMember(t *testing.T) {
	m, _ := newTestModel(t)
	m.activeTab = tabConsole

	primary := ""
	for i, e := range m.tree {
		if e.kind == itemPrimary {
			primary = e.primary
			m.cursor = i
			break
		}
	}
	if primary == "" {
		t.Skip("the test tree has no groups")
	}

	for _, p := range m.projects {
		if sv := m.services[p.Path]; sv != nil {
			sv.Status = statusStopped
		}
	}

	sel := ""
	for i, e := range m.tree {
		if e.kind == itemProject && group.PrimaryOf(e.project) == primary {
			sel = e.project.Path
			m.cursor = i
			break
		}
	}
	if sel == "" {
		t.Skip("the test group has no projects")
	}

	other := ""
	for _, e := range m.tree {
		if e.kind == itemProject && group.PrimaryOf(e.project) == primary && e.project.Path != sel {
			other = e.project.Path
			break
		}
	}
	if other == "" {
		t.Skip("the test group has only one member")
	}
	csOther := m.consoleStateFor(other)
	csOther.stdout = "output from previous sibling"

	cs := m.consoleStateFor(sel)
	cs.stdout = "output from previous service"

	_, _ = m.toggleNode(primary, "")

	if cs := m.consoleStateFor(sel); cs.stdout != "" {
		t.Errorf("the console of %s still has %q after starting the group: the user would see the output "+
			"from the previous service as if it were from the new one", sel, cs.stdout)
	}
	// The sibling's in-memory buffer is cleared too: every member restarts tailing from the current offset, so stale text would mix two runs; the viewport of an unselected service is not rewritten, because painting an empty console where the user is not looking looks like a regression.
	if cs := m.consoleStateFor(other); cs.stdout != "" {
		t.Errorf("the in-memory buffer of %s = %q after starting the group, want empty: the offset "+
			"was reset, so the old text belongs to a previous run", other, cs.stdout)
	}
	if v := tail.StripANSI(m.consoleView.View()); strings.Contains(v, "output from previous sibling") {
		t.Errorf("the viewport was rewritten with the output of a service that is not selected:\n%s", v)
	}
}

// MEASURED: these heights are subtractions that can go negative and strings.Repeat panics on a negative count; updateLayout is pure, so any size can be fed without a real tiny terminal.
func TestLayoutPreventsNegativeInnerHeight(t *testing.T) {
	cases := []struct {
		name       string
		width, height int
	}{
		{"1x1 terminal", 1, 1},
		{"one column and one row", 1, 1},
		{"height 3 with detail", 40, 3},
		{"width 0", 0, 24},
		{"negative height", 80, -5},
		{"all negative", -1, -1},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newTestModel(t)
			m.width, m.height = tt.width, tt.height
			m.detailsShown = true
			m.updateLayout()

			if m.bodyH < 1 {
				t.Errorf("bodyH = %d with %dx%d: an interior of 0 or less makes the box compositor "+
					"receive a negative number of rows", m.bodyH, tt.width, tt.height)
			}
			if m.contentH < 0 {
				t.Errorf("contentH = %d with %dx%d: the viewport would receive a negative height", m.contentH, tt.width, tt.height)
			}
			if s := tail.StripANSI(m.View().Content); strings.TrimSpace(s) == "" {
				t.Error("View() drew nothing")
			}
		})
	}
}

// The command must not launch at all, and the mkdir error must reach the caller unwrapped because whoever reads it needs the errno.
func TestRunLoggedPropagatesLogDirCreationFailure(t *testing.T) {
	// A regular file where the log directory goes, so mkdir fails with ENOTDIR.
	block := filepath.Join(t.TempDir(), "logs")
	if err := os.WriteFile(block, []byte("I am a file"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, code, err := runLogged("build", "echo hello", t.TempDir(), filepath.Join(block, "out.log"), "")
	if err == nil {
		t.Fatal("with the log directory unusable the command cannot be launched")
	}
	if code != 0 {
		t.Errorf("exit code = %d with a launch failure, want 0: nothing got to execute", code)
	}
	info, serr := os.Stat(block)
	if serr != nil {
		t.Fatal(serr)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("the block stopped being a regular file (mode %s): the command wrote where it could not", info.Mode())
	}
}

// stdout is created earlier by appendLine, so stderr's open is the first that can really fail, and it must fail before launch or the child writes to the inherited fd, which is the TUI.
func TestRunLoggedFailsIfStderrCannotBeOpened(t *testing.T) {
	dir := t.TempDir()
	stderrPath := filepath.Join(dir, "no-existe", "err.log")

	_, code, err := runLogged("build", "echo hello", dir, filepath.Join(dir, "out.log"), stderrPath)
	if err == nil {
		t.Fatal("with stderr impossible to open the command cannot be launched")
	}
	if code != 0 {
		t.Errorf("exit code = %d with a launch failure, want 0", code)
	}
	// The stdout banner is written before stderr is opened, so the log names the job even when stderr fails.
	out, rerr := os.ReadFile(filepath.Join(dir, "out.log"))
	if rerr != nil {
		t.Fatalf("the stdout banner must be written even if stderr fails: %v", rerr)
	}
	if !strings.Contains(string(out), "build") {
		t.Errorf("the stdout banner = %q, want it to name the job", string(out))
	}
}

// MEASURED (bug): with 40 projects and treeTop at the end, growing the window from 100x30 to 200x400 left the scroll in place and rendered one row of tree over hundreds of blank lines.
func TestResizeWithTreeTallerThanWindowKeepsItInPlace(t *testing.T) {
	m := modelWithTreeOf(t, 40)
	m.width, m.height = 100, 30
	m.updateLayout()
	if m.treeVis() >= len(m.tree) {
		t.Skipf("the test tree fits entirely in %d rows: there is nothing to scroll", m.treeVis())
	}

	m.cursor = len(m.tree) - 1
	m.treeTop = m.cursor + 3

	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	got, ok := m2.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", m2)
	}
	// The clamp floors at len(tree)-visible, not at the cursor, so the cursor row ends up last visible instead of first.
	wantTop := len(got.tree) - got.treeVis()
	if got.treeTop != wantTop {
		t.Errorf("treeTop = %d after resize, want %d (the tree rows minus the visible ones): "+
			"with the highest scroll, the cursor row ends up first in the window "+
			"and the %d above disappear", got.treeTop, wantTop, got.treeTop)
	}
	if got.cursor != m.cursor {
		t.Errorf("the cursor changed from %d to %d on resize", m.cursor, got.cursor)
	}

	m3, _ := got.Update(tea.WindowSizeMsg{Width: 200, Height: 400})
	large, ok := m3.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", m3)
	}
	if large.treeVis() < len(m.tree) {
		t.Skipf("with %dx%d the tree still does not fit entirely (%d visible rows, %d lines)",
			200, 400, large.treeVis(), len(m.tree))
	}
	if large.treeTop != 0 {
		t.Errorf("treeTop = %d with the window grown until the tree fits entirely, want 0: "+
			"the tree is drawn scrolled and with a blank gap below", large.treeTop)
	}

	column := large.treeColumnLines()
	if len(column) != len(large.tree) {
		t.Errorf("the tree column drew %d rows for a tree of %d: when growing the window "+
			"until it fits entirely, all of them must appear", len(column), len(large.tree))
	}
}

// A project whose manifest fails to parse must be shown unconfigured, not stopped, or the UI offers a start button that does nothing.
func TestBootMarksUnparsableAsUnconfigured(t *testing.T) {
	m0, _ := newTestModel(t)
	m, broken := brokenProject(t, m0)

	seen := map[uiStatus]int{}
	for _, p := range m.projects {
		sv := m.services[p.Path]
		if sv == nil {
			t.Fatalf("project %s has no ServiceState after startup", p.Name)
		}
		seen[sv.Status]++
		if p.Configured && sv.Status != statusStopped {
			t.Errorf("%s has a manifest but its status is %q, want stopped", p.Name, sv.Status)
		}
		if !p.Configured && sv.Status != statusUnconfigured {
			t.Errorf("%s has no manifest but its status is %q, want unconfigured", p.Name, sv.Status)
		}
	}
	if seen[statusUnconfigured] == 0 {
		t.Fatal("the test tree brought no project without a manifest: this test is not testing anything")
	}
	selectProject(t, m, broken.Path)
	if sv := m.services[broken.Path]; sv == nil || sv.Status != statusUnconfigured {
		t.Errorf("the service of %s = %v, want unconfigured: without this row the broken manifest "+
			"warning is never seen", broken.Path, sv)
	}
}

// The default test tree is 6 rows and fits in a 30-row window, so without a taller tree the scroll clamp has nothing to correct.
func modelWithTreeOf(t *testing.T, projects int) Model {
	t.Helper()
	isolateConfig(t)
	root := t.TempDir()
	for i := range projects {
		dir := filepath.Join(root, fmt.Sprintf("p%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := fmt.Sprintf("name = \"p%02d\"\ncommand_start = \"true\"\nprimary_group = \"g\"\nsecondary_group = \"s%02d\"\n", i, i)
		if err := os.WriteFile(filepath.Join(dir, ".vroom.toml"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()
	if len(m.tree) <= projects {
		t.Fatalf("the tree has %d rows for %d projects: a header per group is not being generated",
			len(m.tree), projects)
	}
	return m
}

func firstConfiguredProject(t *testing.T, m Model) scanner.Project {
	t.Helper()
	for _, p := range m.projects {
		if p.Configured {
			return p
		}
	}
	t.Fatal("the test tree has no configured projects")
	return scanner.Project{}
}

func selectProject(t *testing.T, m Model, path string) Model {
	t.Helper()
	for i, e := range m.tree {
		if e.kind == itemProject && e.project.Path == path {
			m.cursor = i
			return m
		}
	}
	t.Fatalf("there is no project entry for %s", path)
	return m
}

func key(name string) tea.KeyMsg {
	return tea.KeyPressMsg{Code: rune(name[0]), Text: name}
}
