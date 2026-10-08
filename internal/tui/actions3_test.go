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

func TestStopCmdWritesStopWarningsToLog(t *testing.T) {
	m, store := newTestModel(t)
	p := firstConfiguredProject(t, m)

	port := portWithAmbiguousOwner(t)

	if err := store.SaveMeta(p.Path, state.Meta{Pid: 0, Port: port}); err != nil {
		t.Fatal(err)
	}

	cmd := stopCmd(store, process.NewManager(), p.Path, "")
	if cmd == nil {
		t.Fatal("stopCmd must return a command")
	}
	if _, ok := cmd().(stoppedMsg); !ok {
		t.Fatalf("the command returned %T, want stoppedMsg", cmd())
	}

	log, err := os.ReadFile(store.StderrLog(p.Path))
	if err != nil {
		t.Fatalf("no stderr log for the service: %v", err)
	}
	warning := string(log)
	if !strings.Contains(warning, "vroom ▶ stop") {
		t.Errorf("stderr log = %q, want a stop warning line: a warning that is not written "+
			"anywhere is not a warning", warning)
	}
	if !strings.Contains(warning, "nothing is killed") {
		t.Errorf("the warning = %q, want it to explain that nothing is killed: the user "+
			"needs to understand why their port is still occupied", warning)
	}
}

// Regression anchor for the "spawned but meta failed" bug: the propagated error carries no PID because the child is already killed by then.
func TestStartCmdPropagatesMetaPersistFailure(t *testing.T) {
	m, store := newTestModel(t)
	p := firstConfiguredProject(t, m)

	// A command that really spawns, so the failure under test is the persistence one and not an impossible spawn.
	p.Manifest = &manifest.Manifest{Name: "tienda-api", Command: "sleep 30", Port: 8081}

	dir, err := store.EnsureServiceDir(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	// MEASURED: SaveMeta writes meta.json.tmp and then renames, so with meta.json already a non-empty directory the rename fails with EISDIR.
	if err := os.MkdirAll(filepath.Join(dir, "meta.json", "lock"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Logf("meta of %s in the service directory %s", p.Path, dir)

	cmd := startCmd(store, &stubManager{}, p, "")
	if cmd == nil {
		t.Fatal("startCmd must return a command")
	}
	msg, ok := cmd().(startedMsg)
	if !ok {
		t.Fatalf("the command returned %T, want startedMsg", cmd())
	}
	if msg.err == nil {
		t.Error("startedMsg.err = nil with a meta that cannot be saved: the service would " +
			"remain stopped in the TUI without explanation of why")
	}
	if msg.path != p.Path {
		t.Errorf("path = %q, want %q: the message must say which service it refers to", msg.path, p.Path)
	}
	if msg.res.Pid != 0 {
		t.Errorf("startedMsg.res.Pid = %d with a meta that could not be saved, want 0: the "+
			"process stops before propagating the error, so the message cannot carry a "+
			"pid that the user cannot stop", msg.res.Pid)
	}
}

// stackStats resolves every stack before launching any, so an unresolvable name in the second must leave the first untouched.
func TestToggleStackGroupWarnsOfUnresolvableStack(t *testing.T) {
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
name = "broken"
primary_group = "tienda"
  [[stack.stage]]
  name = "broken"
  services = ["service-that-does-not-exist"]
`)
	store := state.NewStoreAt(t.TempDir())
	m := New(store, &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()
	if m.engine == nil {
		t.Fatalf("New did not build the engine")
	}
	if len(m.stacksForPrimary("tienda")) != 2 {
		t.Fatalf("there are %d stacks, want 2", len(m.stacksForPrimary("tienda")))
	}

	for _, p := range m.projects {
		if sv := m.services[p.Path]; sv != nil {
			sv.Status = statusStopped
		}
	}

	new, cmd := m.toggleComposers("tienda")
	got, ok := new.(Model)
	if !ok {
		t.Fatalf("toggleComposers returned %T, want Model", new)
	}
	if cmd != nil {
		t.Error("a stack that does not resolve cannot launch anything: not even the other one")
	}
	if !strings.Contains(got.message, "stack conflict") {
		t.Errorf("message = %q, want it to say there is a stack conflict", got.message)
	}
	if !strings.Contains(got.message, "service-that-does-not-exist") {
		t.Errorf("message = %q, want it to name the service that does not resolve", got.message)
	}
	for _, p := range m.projects {
		if sv := got.services[p.Path]; sv != nil && sv.Status != statusStopped {
			t.Errorf("%s remained in %q after a stack conflict: the failure was detected "+
				"BEFORE launching anything, and that is how it must remain", p.Name, sv.Status)
		}
	}
}

// Regression anchor: before this, no test pressed the key on an itemStack row, only called toggleStack directly.
func TestToggleOnRealStackGoesThroughStackBranch(t *testing.T) {
	m := newStackModel(t)

	idx := -1
	for i, e := range m.tree {
		if e.kind == itemStack {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("the tree with compose file did not bring any stack row: this test is not testing anything")
	}
	m.cursor = idx

	new, cmd := m.toggleSelected()
	got, ok := new.(Model)
	if !ok {
		t.Fatalf("toggleSelected returned %T, want Model", new)
	}
	if cmd == nil {
		t.Error("pressing the key on a stopped stack must launch the orchestration")
	}
	if !strings.Contains(got.message, "launching stack") {
		t.Errorf("message = %q, want it to say it is launching the stack: it is the "+
			"confirmation that the key went to the row the cursor was pointing at", got.message)
	}
}

// Without the nil-sv continue an unknown member panics mid-launch while the rest of the group is already starting.
func TestToggleGroupSkipsMemberWithoutKnownStateInSelectedGroup(t *testing.T) {
	m, _ := newTestModel(t)

	primary := ""
	idx := -1
	for i, e := range m.tree {
		if e.kind == itemPrimary {
			primary = e.primary
			idx = i
			break
		}
	}
	if primary == "" {
		t.Skip("the test tree does not have groups")
	}

	members := m.nodeMembers(primary, "")
	if len(members) < 2 {
		t.Skipf("the group has %d members, want at least 2", len(members))
	}

	for _, p := range m.projects {
		if sv := m.services[p.Path]; sv != nil {
			sv.Status = statusStopped
		}
	}
	lost := members[0].Path
	delete(m.services, lost)

	m.cursor = idx
	new, _ := m.toggleSelected()
	got, ok := new.(Model)
	if !ok {
		t.Fatalf("toggleSelected returned %T, want Model", new)
	}
	if sv := got.services[lost]; sv != nil {
		t.Errorf("the member without state %s reappeared with state %v", lost, sv.Status)
	}
	for _, p := range members[1:] {
		sv := got.services[p.Path]
		if sv == nil {
			t.Fatalf("the member %s without state disappeared from the map along the way", p.Path)
		}
		if sv.Status != statusStarting {
			t.Errorf("%s remained in %q after starting the group, want starting: skipping "+
				"a member without state cannot be an excuse for not starting the others", p.Name, sv.Status)
		}
	}
}

func TestPickerInnerWHasFloorOnNarrowTerminal(t *testing.T) {
	m, _ := newTestModel(t)
	m.width = 20
	m.updateLayout()
	m.pickerItems = []pickerItem{{Name: "a service with a very long name", Description: "and its description"}}

	if w := m.pickerInnerW(); w != 28 {
		t.Errorf("pickerInnerW() = %d with a 20-column terminal, want 28: below that the "+
			"modal is not usable and the user cannot read what they choose", w)
	}

	if s := m.pickerBox(); s == "" {
		t.Error("pickerBox() returned empty")
	}
}

func TestTreeHidesStacksOfCollapsedGroups(t *testing.T) {
	m := modelWithTwoPrimariesAndStacks(t)

	all := m.tree
	withStacks := -1
	for i, e := range all {
		if e.kind == itemSecondary && e.secondary == composersGroup {
			withStacks = i
			break
		}
	}
	if withStacks < 0 {
		t.Fatalf("the tree did not bring any Composers header: there are %d rows", len(all))
	}

	primaryWithStacks := all[withStacks].primary
	p := projectPath(t, m, "tienda-web")
	primaryWithoutStacks := manifestPrimary(t, p)

	if !m.treeHasRow(func(e treeItem) bool {
		return e.kind == itemSecondary && e.primary == primaryWithoutStacks && e.secondary == composersGroup
	}) {
		t.Errorf("the primary %s has no stacks but opened a Composers header: a row "+
			"that leads nowhere is noise", primaryWithoutStacks)
	}

	collapsed := m
	collapsed.collapsed = map[string]bool{primaryWithStacks: true}
	tree := collapsed.buildTree()
	for _, e := range tree {
		if e.primary == primaryWithStacks && (e.kind == itemStack || e.secondary == composersGroup) {
			t.Errorf("with the primary %s collapsed the row %v/%v appeared: collapsing a "+
				"group must also hide its stacks, or they remain floating without an owner", primaryWithStacks, e.kind, e.secondary)
		}
	}

	byComposers := m
	byComposers.collapsed = map[string]bool{byComposers.secondaryKey(primaryWithStacks, composersGroup): true}
	tree = byComposers.buildTree()
	sawStacks := false
	for _, e := range tree {
		if e.primary == primaryWithStacks && e.kind == itemStack {
			sawStacks = true
		}
	}
	if sawStacks {
		t.Errorf("with the Composers header of %s collapsed its stacks are still in the tree",
			primaryWithStacks)
	}
	if !byComposers.treeHasRow(func(e treeItem) bool {
		return e.kind == itemProject && e.project.Path == p
	}) {
		t.Errorf("collapsing the Composers header of %s also took the group's projects: "+
			"one thing is collapsing the stacks and another the entire group", primaryWithStacks)
	}
}

// A real second primary is required because a group with no stacks must not open an empty Composers header.
func modelWithTwoPrimariesAndStacks(t *testing.T) Model {
	t.Helper()
	isolateConfig(t)
	root := writeTestTree(t, false)

	writeStr(t, filepath.Join(root, "blog", ".vroom.toml"),
		"name = \"blog\"\ncommand_start = \"true\"\nprimary_group = \"other\"\n")

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

func (m Model) treeHasRow(pred func(treeItem) bool) bool {
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

// MEASURED: /proc/net/tcp lists one entry per socket, so the same pid shows up as two owners of one port and killPortHolderWith refuses to kill what it cannot attribute.
func portWithAmbiguousOwner(t *testing.T) int {
	t.Helper()
	ln4, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// t.Cleanup, not defer: a defer would free the port right when the test needs it held and the assertions would pass without provoking anything.
	t.Cleanup(func() { _ = ln4.Close() })

	port := ln4.Addr().(*net.TCPAddr).Port
	ln6, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port))
	if err != nil {
		// Skip instead of fail when the host has no IPv6, so a missing family is not reported as a pass.
		t.Skipf("could not open the same port in IPv6: %v", err)
	}
	t.Cleanup(func() { _ = ln6.Close() })

	// MEASURED: without these accept loops PortOwnerPIDs finds nothing, since it reads real SYN_RECV/ESTABLISHED entries from /proc/net/tcp.
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
	waitForOwner(t, port)
	if !process.PortOpen(port) {
		t.Fatalf("port %d is not open, so the path to be tested —the occupied port that "+
			"cannot be attributed— will not be traversed", port)
	}

	return port
}

func waitForOwner(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(process.PortOwnerPIDs(port)) >= 2 {
			return
		}
		// A connection closed instantly can vanish from the kernel before it is read, so one is kept alive.
		go func() {
			for _, addr := range []string{
				fmt.Sprintf("127.0.0.1:%d", port),
				fmt.Sprintf("[::1]:%d", port),
			} {
				c, err := net.Dial("tcp", addr)
				if err == nil {
					_ = c.Close()
				}
			}
		}()
		time.Sleep(50 * time.Millisecond)
	}
	t.Skipf("port %d did not get two owners in /proc/net/tcp in 3s: this test cannot "+
		"provoke the warning on this host", port)
}

// A stack whose primary has no scanned projects (deleted directory, stack copied from another workspace) used to vanish with it, leaving a valid compose stack with no row to press.
func TestTreeShowsStacksOfPrimaryWithoutProjects(t *testing.T) {
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
name = "ghost"
primary_group = "group-without-projects"
  [[stack.stage]]
  name = "ghost"
  services = ["tienda-web"]
`)

	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()

	stacks := m.stacksForPrimary("group-without-projects")
	if len(stacks) != 1 {
		t.Fatalf("there are %d stacks for the ghost group, want 1", len(stacks))
	}

	seen := false
	for _, e := range m.tree {
		if e.kind == itemStack && e.primary == "group-without-projects" {
			seen = true
			if e.stack == nil {
				t.Error("the ghost group's stack row came out with stack nil: the cursor " +
					"can point at it and `toggleSelected` would not know what to launch")
			}
		}
	}
	if !seen {
		var rows []string
		for _, e := range m.tree {
			rows = append(rows, string(rune('0'+int(e.kind)))+":"+e.primary+"/"+e.secondary)
		}
		t.Errorf("the ghost group's stack does not appear in the tree (%v): the user has "+
			"a compose with that stack and has no row to press", rows)
	}
}
