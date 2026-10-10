package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"

	"vroom/internal/config"
	"vroom/internal/gitinfo"
	"vroom/internal/group"
	"vroom/internal/manifest"
	"vroom/internal/orchestrate"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// newRepoModel builds a model from a hand-annotated project slice with no real git, so nesting presentation is testable in isolation.
func newRepoModel(t *testing.T, projects []scanner.Project, collapsed map[string]bool) Model {
	t.Helper()
	isolateConfig(t)
	m := Model{
		root:           t.TempDir(),
		store:          state.NewStoreAt(t.TempDir()),
		manager:        &stubManager{},
		keyActions:     config.Load().KeyByAction(),
		projects:       projects,
		services:       make(map[string]*ServiceState, len(projects)),
		branches:       make(map[string]string, len(projects)),
		collapsed:      make(map[string]bool),
		jobs:           make(map[string]string),
		pendingRestart: make(map[string]bool),
		consoleStates:  make(map[string]*consoleState),
		threads:        make(map[string][]threadRow),
		threadPrev:     make(map[string]*threadSample),
		width:          100,
		height:         30,
		spinner:        spinner.New(spinner.WithSpinner(spinner.Dot)),
		startSpinner:   spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	for k, v := range collapsed {
		m.collapsed[k] = v
	}
	for _, p := range projects {
		st := statusStopped
		if !p.Configured {
			st = statusUnconfigured
		}
		m.services[p.Path] = &ServiceState{Status: st}
		m.branches[p.Path] = gitinfo.Branch(p.Path)
	}
	m.entries = group.Arrange(projects)
	m.tree = m.buildTree()
	m.updateLayout()
	return m
}

// repoFixture: main checkout at root/repo in primary group X, plus two worktrees of it.
func repoFixture(t *testing.T) ([]scanner.Project, string, string, string) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wtA := filepath.Join(root, "repo-wt-a")
	wtB := filepath.Join(root, "repo-wt-b")
	mk := func(dir string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk(repo)
	mk(wtA)
	mk(wtB)
	projects := []scanner.Project{
		{Path: repo, Name: "repo", Configured: true, Manifest: manifestNamed("repo", "X")},
		{Path: wtA, Name: "repo-wt-a", Configured: true, Manifest: manifestNamed("api", ""),
			IsWorktree: true, RepoRoot: repo},
		{Path: wtB, Name: "repo-wt-b", Configured: true, Manifest: manifestNamed("api", ""),
			IsWorktree: true, RepoRoot: repo},
	}
	return projects, repo, wtA, wtB
}

func manifestNamed(name, primary string) *manifest.Manifest {
	return &manifest.Manifest{Name: name, Command: "echo", PrimaryGroup: primary}
}

func findRepo(t *testing.T, m Model, name string) int {
	t.Helper()
	for i, it := range m.tree {
		if it.kind == itemRepo && it.project.Name == name {
			return i
		}
	}
	return -1
}

func TestRepoCollapsedSingleRow(t *testing.T) {
	projects, repo, _, _ := repoFixture(t)
	m := newRepoModel(t, projects, nil)

	nProjects := 0
	for _, it := range m.tree {
		if it.kind == itemProject {
			nProjects++
		}
	}
	if nProjects != 1 {
		t.Fatalf("expected 1 top-level project row, got %d", nProjects)
	}
	if findCursor(m, "repo-wt-a") >= 0 || findCursor(m, "repo-wt-b") >= 0 {
		t.Error("worktrees must not be top-level rows")
	}
	tree, _ := m.treeLines()
	joined := strings.Join(tree, "\n")
	if !strings.Contains(joined, "▸") || !strings.Contains(joined, "repo") {
		t.Errorf("repo row must be collapsed (▸): %q", joined)
	}
	if m.repoExpanded(repo) {
		t.Error("repo must be collapsed by default")
	}
}

func TestRepoWithoutWorktreesNotExpandable(t *testing.T) {
	root := t.TempDir()
	solo := filepath.Join(root, "solo")
	projects := []scanner.Project{{Path: solo, Name: "solo", Configured: true, Manifest: manifestNamed("solo", "")}}
	m := newRepoModel(t, projects, nil)
	it := m.tree[findCursor(m, "solo")]
	if it.hasKids {
		t.Error("a project without worktrees must not mark children")
	}
	tree, _ := m.treeLines()
	if strings.Contains(strings.Join(tree, "\n"), "▸") {
		t.Errorf("without worktrees there must be no expansion glyph: %q", strings.Join(tree, "\n"))
	}
}

func TestRepoRowIsOperableMainCheckout(t *testing.T) {
	projects, repo, _, _ := repoFixture(t)
	m := newRepoModel(t, projects, nil)
	m = moveCursorTo(t, m, "repo")
	p := m.selected()
	if p == nil || p.Path != repo {
		t.Fatalf("selected = %+v, want main checkout %s", p, repo)
	}
	// s arms the start-generation selector; sp confirms by_port and starts.
	m2, _ := press(m, "s")
	m2, cmd := press(m2, "p")
	if cmd == nil {
		t.Fatal("start on the repo row must emit a command")
	}
	msg := cmd()
	sm, ok := msg.(startedMsg)
	if !ok || sm.path != repo {
		t.Fatalf("start must operate on the main checkout %s, got %+v", repo, msg)
	}
	m3, _ := press(m2, "enter") // main checkout with children folds/expands its repo
	if !m3.repoExpanded(repo) {
		t.Error("enter on the repo row must expand it")
	}
}

func TestExpandRepoRevealsIndentedWorktrees(t *testing.T) {
	projects, _, _, _ := repoFixture(t)
	m := newRepoModel(t, projects, nil)
	m = moveCursorTo(t, m, "repo")
	m2, _ := press(m, "enter")
	tree, _ := m2.treeLines()
	joined := strings.Join(tree, "\n")
	if !strings.Contains(joined, "▾") {
		t.Errorf("repo must be expanded (▾): %q", joined)
	}
	for _, name := range []string{"repo-wt-a", "repo-wt-b"} {
		if !strings.Contains(joined, name) {
			t.Errorf("%s must be visible when expanded: %q", name, joined)
		}
	}
	idxRepo, idxA, idxB := findCursor(m2, "repo"), findCursor(m2, "repo-wt-a"), findCursor(m2, "repo-wt-b")
	if idxA < idxRepo || idxB < idxA {
		t.Fatalf("unexpected order: repo=%d a=%d b=%d", idxRepo, idxA, idxB)
	}
	lineA := tree[idxA]
	if !strings.HasPrefix(lineA, "  ") {
		t.Errorf("worktree must be indented: %q", lineA)
	}
}

func TestNestedWorktreeOperable(t *testing.T) {
	projects, repo, wtA, _ := repoFixture(t)
	for i := range projects {
		if projects[i].Path == wtA {
			projects[i].Manifest.Build = "echo build"
			projects[i].Manifest.Install = "echo install"
		}
	}
	collapsed := map[string]bool{repoKey(repo): true}
	atWorktree := func() Model {
		m := newRepoModel(t, projects, collapsed)
		return moveCursorTo(t, m, "repo-wt-a")
	}

	// s arms the start-generation selector; sp confirms by_port and starts.
	mArm, _ := press(atWorktree(), "s")
	_, cmd := press(mArm, "p")
	if cmd == nil {
		t.Fatal("start on a worktree must emit a command")
	}
	if sm, ok := cmd().(startedMsg); !ok || sm.path != wtA {
		t.Fatalf("service must start with workdir %s", wtA)
	}
	if _, bcmd := press(atWorktree(), "b"); bcmd == nil {
		t.Fatal("build on a worktree must emit a command")
	}
	if _, icmd := press(atWorktree(), "i"); icmd == nil {
		t.Fatal("install on a worktree must emit a command")
	}
	m := atWorktree()
	m.services[wtA].Status = statusRunning
	if _, scmd := press(m, "s"); scmd == nil {
		t.Fatal("stop on a worktree must emit a command")
	}
}

func TestRepoCollapsePersistedAndNamespaced(t *testing.T) {
	projects, repo, _, _ := repoFixture(t)
	m := newRepoModel(t, projects, map[string]bool{repoKey(repo): true})
	if !m.repoExpanded(repo) {
		t.Fatal("repo must be restored expanded")
	}
	tree, _ := m.treeLines()
	if !strings.Contains(strings.Join(tree, "\n"), "repo-wt-a") {
		t.Errorf("with repo expanded, worktrees must be visible: %q", strings.Join(tree, "\n"))
	}
	m = moveCursorTo(t, m, "repo")
	m2, _ := press(m, "enter")
	if m2.repoExpanded(repo) {
		t.Error("enter must collapse the repo")
	}
	if m2.collapsed["X"] {
		t.Error("repo collapse must not touch the group X key")
	}
	m3 := m2
	m3.cursor = findPrimary(m3, "X")
	m4, _ := press(m3, "enter")
	if !m4.collapsed["X"] {
		t.Error("enter on the group must fold it")
	}
	if m4.repoExpanded(repo) {
		t.Error("folding the group must not expand the repo")
	}
}

// repoKey is structurally disjoint from group keys, so even a primary_group literally spelled "repo:<path>" (the old repoKey format) cannot collide.
func TestRepoKeyDoesNotCollideWithGroupKey(t *testing.T) {
	projects, repo, _, _ := repoFixture(t)
	groupKey := "repo:" + repo
	for i := range projects {
		if projects[i].Path == repo {
			projects[i].Manifest.PrimaryGroup = groupKey
		}
	}
	build := func() Model {
		m := newRepoModel(t, projects, nil)
		if findPrimary(m, groupKey) < 0 {
			t.Fatalf("missing group header %q: %+v", groupKey, m.tree)
		}
		return m
	}

	g := build()
	g.cursor = findPrimary(g, groupKey)
	g2, _ := press(g, "enter")
	if !g2.collapsed[groupKey] {
		t.Fatal("enter on the group must fold it")
	}
	if g2.repoExpanded(repo) {
		t.Error("folding the group must not expand the repo (key collision)")
	}

	// Fresh model: the collapsed map is shared by reference across model copies, so reusing one would leak the earlier toggle.
	r := build()
	r = moveCursorTo(t, r, "repo")
	r2, _ := press(r, "enter")
	if !r2.repoExpanded(repo) {
		t.Error("enter on the repo row must expand it")
	}
	if r2.collapsed[groupKey] {
		t.Error("expanding the repo must not fold the group (key collision)")
	}
}

func TestRepoCollapseKeyPersists(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	key := repoKey("/some/repo")
	if err := store.SaveCollapsed(map[string]bool{key: true}); err != nil {
		t.Fatal(err)
	}
	loaded := store.LoadCollapsed()
	if !loaded[key] {
		t.Errorf("repo key must persist and be restored: %#v", loaded)
	}
}

func TestBareContainerNotOperable(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "bare")
	wtA := filepath.Join(root, "bare-wt-a")
	projects := []scanner.Project{
		{Path: bare, Name: "bare", IsBareContainer: true},
		{Path: wtA, Name: "bare-wt-a", Configured: true, Manifest: manifestNamed("api", ""),
			IsWorktree: true, RepoRoot: bare},
	}
	m := newRepoModel(t, projects, nil)
	bi := findRepo(t, m, "bare")
	if bi < 0 {
		t.Fatalf("bare repo must render as container: %+v", m.tree)
	}
	m.cursor = bi
	if p := m.selected(); p != nil {
		t.Error("bare container must not be selected as a project")
	}
	m2, cmd := press(m, "s")
	if cmd != nil {
		t.Error("bare container must not be operable")
	}
	m3, _ := press(m2, "enter")
	tree, _ := m3.treeLines()
	joined := strings.Join(tree, "\n")
	if !strings.Contains(joined, "bare-wt-a") {
		t.Errorf("when expanding the bare, its worktrees must be visible: %q", joined)
	}
	if !strings.Contains(joined, "▾") {
		t.Errorf("container with children must show expansion glyph: %q", joined)
	}
}

func TestBareContainerWithoutWorktreesHasNoGlyph(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "bare")
	projects := []scanner.Project{{Path: bare, Name: "bare", IsBareContainer: true}}
	m := newRepoModel(t, projects, nil)
	it := m.tree[findRepo(t, m, "bare")]
	if it.hasKids {
		t.Fatal("without worktrees must not mark children")
	}
	joined := strings.Join(mustTree(t, m), "\n")
	if strings.Contains(joined, "▸") || strings.Contains(joined, "▾") {
		t.Errorf("container without children must not show glyph: %q", joined)
	}
	if !strings.Contains(joined, "(bare)") {
		t.Errorf("container must still show (bare): %q", joined)
	}
}

func TestDetachedWorktreeShowsSha(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wtDet := filepath.Join(root, "repo-wt-det")
	gitdir := filepath.Join(root, "gitdir")
	if err := os.MkdirAll(wtDet, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDet, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "HEAD"), []byte("abcdef1234567890abcdef1234567890abcdef12\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	projects := []scanner.Project{
		{Path: repo, Name: "repo", Configured: true, Manifest: manifestNamed("repo", "")},
		{Path: wtDet, Name: "repo-wt-det", Configured: true, Manifest: manifestNamed("api", ""),
			IsWorktree: true, RepoRoot: repo},
	}
	m := newRepoModel(t, projects, map[string]bool{repoKey(repo): true})
	tree, _ := m.treeLines()
	joined := strings.Join(tree, "\n")
	if !strings.Contains(joined, "repo-wt-det") {
		t.Fatalf("missing detached worktree row: %q", joined)
	}
	if !strings.Contains(joined, "abcdef1 (detached)") {
		t.Errorf("detached branch must show as sha (detached): %q", joined)
	}
}

func TestUnconfiguredWorktreeRow(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wt := filepath.Join(root, "repo-wt-sin-mf")
	projects := []scanner.Project{
		{Path: repo, Name: "repo", Configured: true, Manifest: manifestNamed("repo", "")},
		{Path: wt, Name: "repo-wt-sin-mf", IsWorktree: true, RepoRoot: repo},
	}
	m := newRepoModel(t, projects, map[string]bool{repoKey(repo): true})
	tree, _ := m.treeLines()
	if !strings.Contains(strings.Join(tree, "\n"), "⚠") {
		t.Errorf("worktree without manifest must have ⚠: %q", strings.Join(tree, "\n"))
	}
	m = moveCursorTo(t, m, "repo-wt-sin-mf")
	m2, cmd := press(m, "s")
	if cmd != nil {
		t.Error("worktree without manifest must not be operable")
	}
	if m2.message == "" {
		t.Error("must notify why it is not operable")
	}
}

func TestOutOfRootWorktreeNestsUnderSyntheticContainer(t *testing.T) {
	outside := "/outside/repo"
	wtA := "/root/repo-wt-a"
	projects := []scanner.Project{
		{Path: wtA, Name: "repo-wt-a", Configured: true, Manifest: manifestNamed("api", ""),
			IsWorktree: true, RepoRoot: outside},
	}
	m := newRepoModel(t, projects, map[string]bool{repoKey(outside): true})
	if findCursor(m, "repo-wt-a") < 0 {
		t.Fatalf("worktree must nest under the synthesized container: %+v", m.tree)
	}
	bi := findRepo(t, m, "repo")
	if bi < 0 {
		t.Fatalf("missing synthesized container row for %s: %+v", outside, m.tree)
	}
	tree, _ := m.treeLines()
	if !strings.Contains(strings.Join(tree, "\n"), "repo-wt-a") {
		t.Errorf("worktree must be visible under the container: %q", strings.Join(tree, "\n"))
	}
}

func TestRepoKeepsGroup(t *testing.T) {
	projects, _, _, _ := repoFixture(t)
	m := newRepoModel(t, projects, nil)
	if findPrimary(m, "X") < 0 {
		t.Fatalf("group X block must exist: %+v", m.tree)
	}
	if findCursor(m, "repo") < 0 {
		t.Fatal("repo row must be in the tree")
	}
	it := m.tree[findCursor(m, "repo")]
	if !it.hasKids {
		t.Error("worktree toggle lives on the repo row")
	}
	if it.primary != "X" {
		t.Errorf("repo must keep its group X, got %q", it.primary)
	}
}

func TestWorktreeGroupIsInert(t *testing.T) {
	projects, repo, _, _ := repoFixture(t)
	for i := range projects {
		if projects[i].Path == projects[i].RepoRoot {
			continue
		}
		if projects[i].Name == "repo-wt-a" {
			projects[i].Manifest.PrimaryGroup = "Y"
		}
	}
	m := newRepoModel(t, projects, map[string]bool{repoKey(repo): true})
	if findPrimary(m, "Y") >= 0 {
		t.Errorf("Y block must not be emitted (only has one worktree): %+v", m.tree)
	}
	tree, _ := m.treeLines()
	if !strings.Contains(strings.Join(tree, "\n"), "repo-wt-a") {
		t.Errorf("worktree must nest under /repo, not in Y: %q", strings.Join(tree, "\n"))
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	base := []string{"-c", "user.email=test@test", "-c", "user.name=test"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// End-to-end acceptance: real git scan, real topology annotation, real nesting in the tree.
func TestNewNestsRealGitWorktrees(t *testing.T) {
	requireGit(t)
	isolateConfig(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "init", "-q", "-b", "main", repo)
	write(filepath.Join(repo, ".vroom.toml"), "name = \"repo\"\ncommand_start = \"echo\"\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "init")

	wtA := filepath.Join(root, "repo-wt-a")
	runGit(t, repo, "worktree", "add", "-q", wtA, "-b", "feature")
	write(filepath.Join(wtA, ".vroom.toml"), "name = \"api\"\ncommand_start = \"echo\"\n")

	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()

	if findCursor(m, "repo-wt-a") >= 0 {
		t.Fatal("worktree must not be a top-level row by default")
	}
	if findCursor(m, "repo") < 0 {
		t.Fatal("missing main checkout row")
	}
	it := m.tree[findCursor(m, "repo")]
	if !it.hasKids {
		t.Fatalf("main checkout must mark worktrees: %+v", it)
	}
	m = moveCursorTo(t, m, "repo")
	m2, _ := press(m, "enter")
	if findCursor(m2, "repo-wt-a") < 0 {
		t.Fatalf("when expanding, worktrees must appear: %+v", m2.tree)
	}
}

func TestNewNotifiesTopologyDegradation(t *testing.T) {
	isolateConfig(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(repo, ".vroom.toml"), "name = \"repo\"\ncommand_start = \"echo\"\n")
	write(filepath.Join(repo, ".git", "config"), "[core]\n\tbare = false\n")

	// A git shim that always exits 1 goes first in PATH, so the topology lookup degrades per repo.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	if !strings.Contains(m.message, "worktree topology unavailable") {
		t.Errorf("must notify topology degradation: %q", m.message)
	}
	if findCursor(m, "repo") < 0 {
		t.Error("project must remain visible despite degradation")
	}
}

// The TUI must report the engine's verdict instead of picking the first project that shares the name.
func TestStackStatsConflictMatchesEngine(t *testing.T) {
	projects := []scanner.Project{
		{Path: "/repo-wt/a", Name: "a", Configured: true, Manifest: manifestNamed("api", "")},
		{Path: "/repo-wt/b", Name: "b", Configured: true, Manifest: manifestNamed("api", "")},
	}
	m := newRepoModel(t, projects, nil)
	stack := &orchestrate.Stack{
		Name:   "s",
		Stages: []orchestrate.Stage{{Name: "s1", Services: []string{"api"}}},
	}
	_, _, err := m.stackStats(stack)
	if err == nil {
		t.Fatal("stackStats must report the duplicate name conflict")
	}
	if _, cerr := orchestrate.LookupService("api", projects); cerr == nil {
		t.Fatal("engine must report the same conflict")
	}
	if !strings.Contains(m.stackRow(stack), "conflict") {
		t.Errorf("stack row must mark the conflict: %q", m.stackRow(stack))
	}

	unique := []scanner.Project{{Path: "/dev/api", Name: "api", Configured: true, Manifest: manifestNamed("api", "")}}
	mu := newRepoModel(t, unique, nil)
	mu.services["/dev/api"].Status = statusRunning
	r, n, uerr := mu.stackStats(stack)
	if uerr != nil || r != 1 || n != 1 {
		t.Fatalf("unique stackStats = (%d,%d,%v), want (1,1,nil)", r, n, uerr)
	}
}

// H2: a failed "git worktree list" means 0 children, so the row must still flag the topology error.
func TestRepoRowShowsTopologyErrorWithoutChildren(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	projects := []scanner.Project{{
		Path: repo, Name: "repo", Configured: true, Manifest: manifestNamed("repo", ""),
		WorktreeErr: "git binary not available",
	}}
	m := newRepoModel(t, projects, nil)
	it := m.tree[findCursor(m, "repo")]
	if it.hasKids {
		t.Fatal("without worktrees must not mark children")
	}
	joined := strings.Join(mustTree(t, m), "\n")
	if !strings.Contains(joined, "⚠") {
		t.Errorf("topology error must be marked with ⚠ on the row: %q", joined)
	}
	if strings.Contains(joined, "▸") {
		t.Errorf("without children there must be no expansion glyph: %q", joined)
	}
}

func mustTree(t *testing.T, m Model) []string {
	t.Helper()
	tree, _ := m.treeLines()
	return tree
}

// M1: group aggregation ignores nested worktrees: not counted in the header, not toggled with the group, still visible under their repo row (option B).
func TestGroupAggregationExcludesNestedWorktrees(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	member := filepath.Join(root, "member")
	wt := filepath.Join(root, "repo-wt")
	projects := []scanner.Project{
		{Path: repo, Name: "repo", Configured: true, Manifest: manifestNamed("repo", "X")},
		{Path: member, Name: "member", Configured: true, Manifest: manifestNamed("member", "X")},
		{Path: wt, Name: "repo-wt", Configured: true, Manifest: manifestNamed("api", "X"),
			IsWorktree: true, RepoRoot: repo},
	}
	m := newRepoModel(t, projects, map[string]bool{repoKey(repo): true})

	if _, n := m.nodeStats("X", ""); n != 2 {
		t.Fatalf("X must count 2 members (without the worktree), got %d", n)
	}
	m.collapsed["X"] = true
	if row := m.primaryRow("X"); !strings.Contains(row, "(0/2)") {
		t.Errorf("header X must show (0/2): %q", row)
	}
	m.collapsed["X"] = false

	next, _ := m.toggleNode("X", "")
	m2 := next.(Model)
	if sv := m2.services[wt]; sv != nil && sv.Status != statusStopped {
		t.Errorf("toggleNode(X) must not start the nested worktree: %v", sv.Status)
	}
	idx := findCursor(m2, "repo-wt")
	if idx < 0 {
		t.Fatal("worktree must remain visible under its repo")
	}
	if m2.tree[idx].indent != 1 {
		t.Errorf("worktree must be indented, indent=%d", m2.tree[idx].indent)
	}
}

func rowLine(t *testing.T, m Model, name string) string {
	t.Helper()
	idx := findCursor(m, name)
	if idx < 0 {
		t.Fatalf("row %q not found", name)
	}
	tree, _ := m.treeLines()
	if idx >= len(tree) {
		t.Fatalf("index %d out of lines (%d)", idx, len(tree))
	}
	return tree[idx]
}

func TestRepoRowBadgeCountsRunningWorktrees(t *testing.T) {
	projects, repo, wtA, _ := repoFixture(t)

	m := newRepoModel(t, projects, nil)
	if row := rowLine(t, m, "repo"); strings.Contains(row, "+") {
		t.Errorf("without running worktrees there must be no badge: %q", row)
	}

	m.services[wtA].Status = statusRunning
	if row := rowLine(t, m, "repo"); !strings.Contains(row, "+1") {
		t.Errorf("one running worktree must mark +1: %q", row)
	}

	m2 := newRepoModel(t, projects, nil)
	m2.services[repo].Status = statusRunning
	if row := rowLine(t, m2, "repo"); strings.Contains(row, "+") {
		t.Errorf("only main running must not mark badge: %q", row)
	}

	m2.services[wtA].Status = statusRunning
	row := rowLine(t, m2, "repo")
	if !strings.Contains(row, "+1") {
		t.Errorf("main + worktree running must mark +1: %q", row)
	}
	if !strings.Contains(row, "●") {
		t.Errorf("main dot must remain visible next to the badge: %q", row)
	}
}

func TestContainerRowBadgeCountsRunningWorktrees(t *testing.T) {
	outside := "/outside/repo"
	wtA := "/root/repo-wt-a"
	projects := []scanner.Project{
		{Path: wtA, Name: "repo-wt-a", Configured: true, Manifest: manifestNamed("api", ""),
			IsWorktree: true, RepoRoot: outside},
	}
	m := newRepoModel(t, projects, map[string]bool{repoKey(outside): true})
	m.services[wtA].Status = statusRunning
	idx := findRepo(t, m, "repo")
	if idx < 0 {
		t.Fatal("missing synthesized container row")
	}
	tree, _ := m.treeLines()
	if !strings.Contains(tree[idx], "+1") {
		t.Errorf("container row must mark +1: %q", tree[idx])
	}
}

func TestRepoRowBadgeKeepsColumnWidth(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("r", 40)
	repo := filepath.Join(root, long)
	wt := filepath.Join(root, long+"-wt")
	projects := []scanner.Project{
		{Path: repo, Name: long, Configured: true, Manifest: manifestNamed("repo", "")},
		{Path: wt, Name: long + "-wt", Configured: true, Manifest: manifestNamed("api", ""),
			IsWorktree: true, RepoRoot: repo},
	}
	for _, expanded := range []bool{false, true} {
		collapsed := map[string]bool{}
		if expanded {
			collapsed[repoKey(repo)] = true
		}
		m := newRepoModel(t, projects, collapsed)
		m.services[wt].Status = statusRunning
		line := rowLine(t, m, long)
		if w := lipglossWidth(line); w > treeWidth {
			t.Errorf("expanded=%v: line measures %d, exceeds treeWidth=%d: %q", expanded, w, treeWidth, line)
		}
		if !strings.Contains(line, "+1") {
			t.Errorf("expanded=%v: missing badge: %q", expanded, line)
		}
	}

	outside := filepath.Join(root, long+"-outside")
	mc := newRepoModel(t, []scanner.Project{
		{Path: wt, Name: long + "-wt", Configured: true, Manifest: manifestNamed("api", ""),
			IsWorktree: true, RepoRoot: outside},
	}, nil)
	mc.services[wt].Status = statusRunning
	idx := findRepo(t, mc, filepath.Base(outside))
	if idx < 0 {
		t.Fatal("missing synthesized container")
	}
	tree, _ := mc.treeLines()
	if w := lipglossWidth(tree[idx]); w > treeWidth {
		t.Errorf("container: line measures %d, exceeds treeWidth=%d: %q", w, treeWidth, tree[idx])
	}

	mw := newRepoModel(t, []scanner.Project{{
		Path: repo, Name: long, Configured: true, Manifest: manifestNamed("repo", ""),
		WorktreeErr: "git binary not available",
	}}, nil)
	if line := rowLine(t, mw, long); lipglossWidth(line) > treeWidth {
		t.Errorf("repo with WorktreeErr: line measures %d: %q", lipglossWidth(line), line)
	}

	me := newRepoModel(t, []scanner.Project{
		{Path: repo, Name: long, Configured: true, Manifest: manifestNamed("repo", ""), WorktreeErr: "boom"},
		{Path: wt, Name: long + "-wt", Configured: true, Manifest: manifestNamed("api", ""),
			IsWorktree: true, RepoRoot: repo},
	}, nil)
	me.services[wt].Status = statusRunning
	if line := rowLine(t, me, long); lipglossWidth(line) > treeWidth {
		t.Errorf("repo with ⚠ and badge: line measures %d: %q", lipglossWidth(line), line)
	}

	bare := filepath.Join(root, long+"-bare")
	wt2 := filepath.Join(root, long+"-bare-wt")
	mb := newRepoModel(t, []scanner.Project{
		{Path: bare, Name: filepath.Base(bare), IsBareContainer: true, WorktreeErr: "boom"},
		{Path: wt2, Name: long + "-bare-wt", Configured: true, Manifest: manifestNamed("api", ""),
			IsWorktree: true, RepoRoot: bare},
	}, nil)
	bi := findRepo(t, mb, filepath.Base(bare))
	if bi < 0 {
		t.Fatal("missing bare container")
	}
	btree, _ := mb.treeLines()
	if w := lipglossWidth(btree[bi]); w > treeWidth {
		t.Errorf("bare container with ⚠: line measures %d, exceeds treeWidth=%d: %q", w, treeWidth, btree[bi])
	}
}
