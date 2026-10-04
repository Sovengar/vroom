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
		t.Fatalf("esperaba 1 fila de proyecto top-level, got %d", nProjects)
	}
	if findCursor(m, "repo-wt-a") >= 0 || findCursor(m, "repo-wt-b") >= 0 {
		t.Error("los worktrees no deben ser filas top-level")
	}
	tree, _ := m.treeLines()
	joined := strings.Join(tree, "\n")
	if !strings.Contains(joined, "▸") || !strings.Contains(joined, "repo") {
		t.Errorf("la fila de repo debe estar colapsada (▸): %q", joined)
	}
	if m.repoExpanded(repo) {
		t.Error("el repo debe estar colapsado por defecto")
	}
}

func TestRepoWithoutWorktreesNotExpandable(t *testing.T) {
	root := t.TempDir()
	solo := filepath.Join(root, "solo")
	projects := []scanner.Project{{Path: solo, Name: "solo", Configured: true, Manifest: manifestNamed("solo", "")}}
	m := newRepoModel(t, projects, nil)
	it := m.tree[findCursor(m, "solo")]
	if it.hasKids {
		t.Error("un proyecto sin worktrees no debe marcar hijos")
	}
	tree, _ := m.treeLines()
	if strings.Contains(strings.Join(tree, "\n"), "▸") {
		t.Errorf("sin worktrees no debe haber glifo de expansión: %q", strings.Join(tree, "\n"))
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
	m2, cmd := press(m, "s")
	if cmd == nil {
		t.Fatal("start sobre la fila del repo debe emitir un comando")
	}
	msg := cmd()
	sm, ok := msg.(startedMsg)
	if !ok || sm.path != repo {
		t.Fatalf("start debe operar sobre el main checkout %s, got %+v", repo, msg)
	}
	m3, _ := press(m2, "enter") // el main checkout con hijos pliega/expande su repo
	if !m3.repoExpanded(repo) {
		t.Error("enter sobre la fila de repo debe expandirla")
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
		t.Errorf("el repo debe quedar expandido (▾): %q", joined)
	}
	for _, name := range []string{"repo-wt-a", "repo-wt-b"} {
		if !strings.Contains(joined, name) {
			t.Errorf("%s debe verse al expandir: %q", name, joined)
		}
	}
	idxRepo, idxA, idxB := findCursor(m2, "repo"), findCursor(m2, "repo-wt-a"), findCursor(m2, "repo-wt-b")
	if idxA < idxRepo || idxB < idxA {
		t.Fatalf("orden inesperado: repo=%d a=%d b=%d", idxRepo, idxA, idxB)
	}
	lineA := tree[idxA]
	if !strings.HasPrefix(lineA, "  ") {
		t.Errorf("el worktree debe ir indentado: %q", lineA)
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

	_, cmd := press(atWorktree(), "s")
	if cmd == nil {
		t.Fatal("start sobre un worktree debe emitir un comando")
	}
	if sm, ok := cmd().(startedMsg); !ok || sm.path != wtA {
		t.Fatalf("el servicio debe arrancar con workdir %s", wtA)
	}
	if _, bcmd := press(atWorktree(), "b"); bcmd == nil {
		t.Fatal("build sobre un worktree debe emitir un comando")
	}
	if _, icmd := press(atWorktree(), "i"); icmd == nil {
		t.Fatal("install sobre un worktree debe emitir un comando")
	}
	m := atWorktree()
	m.services[wtA].Status = statusRunning
	if _, scmd := press(m, "s"); scmd == nil {
		t.Fatal("stop sobre un worktree debe emitir un comando")
	}
}

func TestRepoCollapsePersistedAndNamespaced(t *testing.T) {
	projects, repo, _, _ := repoFixture(t)
	m := newRepoModel(t, projects, map[string]bool{repoKey(repo): true})
	if !m.repoExpanded(repo) {
		t.Fatal("el repo debe restaurarse expandido")
	}
	tree, _ := m.treeLines()
	if !strings.Contains(strings.Join(tree, "\n"), "repo-wt-a") {
		t.Errorf("con el repo expandido deben verse los worktrees: %q", strings.Join(tree, "\n"))
	}
	m = moveCursorTo(t, m, "repo")
	m2, _ := press(m, "enter")
	if m2.repoExpanded(repo) {
		t.Error("enter debe colapsar el repo")
	}
	if m2.collapsed["X"] {
		t.Error("el colapso del repo no debe tocar la clave del grupo X")
	}
	m3 := m2
	m3.cursor = findPrimary(m3, "X")
	m4, _ := press(m3, "enter")
	if !m4.collapsed["X"] {
		t.Error("enter sobre el grupo debe plegarlo")
	}
	if m4.repoExpanded(repo) {
		t.Error("plegar el grupo no debe expandir el repo")
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
			t.Fatalf("falta el header del grupo %q: %+v", groupKey, m.tree)
		}
		return m
	}

	g := build()
	g.cursor = findPrimary(g, groupKey)
	g2, _ := press(g, "enter")
	if !g2.collapsed[groupKey] {
		t.Fatal("enter sobre el grupo debe plegarlo")
	}
	if g2.repoExpanded(repo) {
		t.Error("plegar el grupo no debe expandir el repo (colisión de claves)")
	}

	// Fresh model: the collapsed map is shared by reference across model copies, so reusing one would leak the earlier toggle.
	r := build()
	r = moveCursorTo(t, r, "repo")
	r2, _ := press(r, "enter")
	if !r2.repoExpanded(repo) {
		t.Error("enter sobre la fila del repo debe expandirlo")
	}
	if r2.collapsed[groupKey] {
		t.Error("expandir el repo no debe plegar el grupo (colisión de claves)")
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
		t.Errorf("la clave de repo debe persistir y restaurarse: %#v", loaded)
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
		t.Fatalf("el bare repo debe renderizarse como contenedor: %+v", m.tree)
	}
	m.cursor = bi
	if p := m.selected(); p != nil {
		t.Error("el contenedor bare no debe seleccionarse como proyecto")
	}
	m2, cmd := press(m, "s")
	if cmd != nil {
		t.Error("el contenedor bare no debe ser operable")
	}
	m3, _ := press(m2, "enter")
	tree, _ := m3.treeLines()
	joined := strings.Join(tree, "\n")
	if !strings.Contains(joined, "bare-wt-a") {
		t.Errorf("al expandir el bare deben verse sus worktrees: %q", joined)
	}
	if !strings.Contains(joined, "▾") {
		t.Errorf("el contenedor con hijos debe mostrar glifo de expansión: %q", joined)
	}
}

func TestBareContainerWithoutWorktreesHasNoGlyph(t *testing.T) {
	root := t.TempDir()
	bare := filepath.Join(root, "bare")
	projects := []scanner.Project{{Path: bare, Name: "bare", IsBareContainer: true}}
	m := newRepoModel(t, projects, nil)
	it := m.tree[findRepo(t, m, "bare")]
	if it.hasKids {
		t.Fatal("sin worktrees no debe marcar hijos")
	}
	joined := strings.Join(mustTree(t, m), "\n")
	if strings.Contains(joined, "▸") || strings.Contains(joined, "▾") {
		t.Errorf("un contenedor sin hijos no debe mostrar glifo: %q", joined)
	}
	if !strings.Contains(joined, "(bare)") {
		t.Errorf("el contenedor debe seguir mostrando (bare): %q", joined)
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
		t.Fatalf("falta la fila del worktree detached: %q", joined)
	}
	if !strings.Contains(joined, "abcdef1 (detached)") {
		t.Errorf("la rama detached debe mostrarse como sha (detached): %q", joined)
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
		t.Errorf("un worktree sin manifiesto debe llevar ⚠: %q", strings.Join(tree, "\n"))
	}
	m = moveCursorTo(t, m, "repo-wt-sin-mf")
	m2, cmd := press(m, "s")
	if cmd != nil {
		t.Error("un worktree sin manifiesto no debe ser operable")
	}
	if m2.message == "" {
		t.Error("debe notificar por qué no es operable")
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
		t.Fatalf("el worktree debe anidarse bajo el contenedor sintetizado: %+v", m.tree)
	}
	bi := findRepo(t, m, "repo")
	if bi < 0 {
		t.Fatalf("falta la fila contenedora sintetizada de %s: %+v", outside, m.tree)
	}
	tree, _ := m.treeLines()
	if !strings.Contains(strings.Join(tree, "\n"), "repo-wt-a") {
		t.Errorf("el worktree debe verse bajo el contenedor: %q", strings.Join(tree, "\n"))
	}
}

func TestRepoKeepsGroup(t *testing.T) {
	projects, _, _, _ := repoFixture(t)
	m := newRepoModel(t, projects, nil)
	if findPrimary(m, "X") < 0 {
		t.Fatalf("el bloque del grupo X debe existir: %+v", m.tree)
	}
	if findCursor(m, "repo") < 0 {
		t.Fatal("la fila del repo debe estar en el árbol")
	}
	it := m.tree[findCursor(m, "repo")]
	if !it.hasKids {
		t.Error("el toggle de worktrees vive en la fila del repo")
	}
	if it.primary != "X" {
		t.Errorf("el repo debe conservar su grupo X, got %q", it.primary)
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
		t.Errorf("no debe emitirse el bloque Y (solo tiene un worktree): %+v", m.tree)
	}
	tree, _ := m.treeLines()
	if !strings.Contains(strings.Join(tree, "\n"), "repo-wt-a") {
		t.Errorf("el worktree debe anidarse bajo /repo, no en Y: %q", strings.Join(tree, "\n"))
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no disponible")
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
		t.Fatal("el worktree no debe ser fila top-level por defecto")
	}
	if findCursor(m, "repo") < 0 {
		t.Fatal("falta la fila del main checkout")
	}
	it := m.tree[findCursor(m, "repo")]
	if !it.hasKids {
		t.Fatalf("el main checkout debe marcar worktrees: %+v", it)
	}
	m = moveCursorTo(t, m, "repo")
	m2, _ := press(m, "enter")
	if findCursor(m2, "repo-wt-a") < 0 {
		t.Fatalf("al expandir deben aparecer los worktrees: %+v", m2.tree)
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
		t.Errorf("debe notificar la degradación de topología: %q", m.message)
	}
	if findCursor(m, "repo") < 0 {
		t.Error("el proyecto debe seguir visible pese a la degradación")
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
		t.Fatal("stackStats debe reportar el conflicto de nombre duplicado")
	}
	if _, cerr := orchestrate.LookupService("api", projects); cerr == nil {
		t.Fatal("el engine debe reportar el mismo conflicto")
	}
	if !strings.Contains(m.stackRow(stack), "conflict") {
		t.Errorf("la fila del stack debe marcar el conflicto: %q", m.stackRow(stack))
	}

	unique := []scanner.Project{{Path: "/dev/api", Name: "api", Configured: true, Manifest: manifestNamed("api", "")}}
	mu := newRepoModel(t, unique, nil)
	mu.services["/dev/api"].Status = statusRunning
	r, n, uerr := mu.stackStats(stack)
	if uerr != nil || r != 1 || n != 1 {
		t.Fatalf("stackStats único = (%d,%d,%v), want (1,1,nil)", r, n, uerr)
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
		t.Fatal("sin worktrees no debe marcar hijos")
	}
	joined := strings.Join(mustTree(t, m), "\n")
	if !strings.Contains(joined, "⚠") {
		t.Errorf("el error de topología debe marcarse con ⚠ en la fila: %q", joined)
	}
	if strings.Contains(joined, "▸") {
		t.Errorf("sin hijos no debe haber glifo de expansión: %q", joined)
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
		t.Fatalf("X debe contar 2 miembros (sin el worktree), got %d", n)
	}
	m.collapsed["X"] = true
	if row := m.primaryRow("X"); !strings.Contains(row, "(0/2)") {
		t.Errorf("header X debe mostrar (0/2): %q", row)
	}
	m.collapsed["X"] = false

	next, _ := m.toggleNode("X", "")
	m2 := next.(Model)
	if sv := m2.services[wt]; sv != nil && sv.Status != statusStopped {
		t.Errorf("toggleNode(X) no debe arrancar el worktree anidado: %v", sv.Status)
	}
	idx := findCursor(m2, "repo-wt")
	if idx < 0 {
		t.Fatal("el worktree debe seguir visible bajo su repo")
	}
	if m2.tree[idx].indent != 1 {
		t.Errorf("el worktree debe ir indentado, indent=%d", m2.tree[idx].indent)
	}
}

func rowLine(t *testing.T, m Model, name string) string {
	t.Helper()
	idx := findCursor(m, name)
	if idx < 0 {
		t.Fatalf("no se encontró la fila %q", name)
	}
	tree, _ := m.treeLines()
	if idx >= len(tree) {
		t.Fatalf("índice %d fuera de las líneas (%d)", idx, len(tree))
	}
	return tree[idx]
}

func TestRepoRowBadgeCountsRunningWorktrees(t *testing.T) {
	projects, repo, wtA, _ := repoFixture(t)

	m := newRepoModel(t, projects, nil)
	if row := rowLine(t, m, "repo"); strings.Contains(row, "+") {
		t.Errorf("sin worktrees corriendo no debe haber badge: %q", row)
	}

	m.services[wtA].Status = statusRunning
	if row := rowLine(t, m, "repo"); !strings.Contains(row, "+1") {
		t.Errorf("un worktree corriendo debe marcar +1: %q", row)
	}

	m2 := newRepoModel(t, projects, nil)
	m2.services[repo].Status = statusRunning
	if row := rowLine(t, m2, "repo"); strings.Contains(row, "+") {
		t.Errorf("solo el main corriendo no debe marcar badge: %q", row)
	}

	m2.services[wtA].Status = statusRunning
	row := rowLine(t, m2, "repo")
	if !strings.Contains(row, "+1") {
		t.Errorf("main + worktree corriendo debe marcar +1: %q", row)
	}
	if !strings.Contains(row, "●") {
		t.Errorf("la bolita del main debe seguir visible junto al badge: %q", row)
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
		t.Fatal("falta la fila contenedora sintetizada")
	}
	tree, _ := m.treeLines()
	if !strings.Contains(tree[idx], "+1") {
		t.Errorf("la fila contenedora debe marcar +1: %q", tree[idx])
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
			t.Errorf("expanded=%v: la línea mide %d, excede treeWidth=%d: %q", expanded, w, treeWidth, line)
		}
		if !strings.Contains(line, "+1") {
			t.Errorf("expanded=%v: falta el badge: %q", expanded, line)
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
		t.Fatal("falta la contenedora sintetizada")
	}
	tree, _ := mc.treeLines()
	if w := lipglossWidth(tree[idx]); w > treeWidth {
		t.Errorf("contenedora: la línea mide %d, excede treeWidth=%d: %q", w, treeWidth, tree[idx])
	}

	mw := newRepoModel(t, []scanner.Project{{
		Path: repo, Name: long, Configured: true, Manifest: manifestNamed("repo", ""),
		WorktreeErr: "git binary not available",
	}}, nil)
	if line := rowLine(t, mw, long); lipglossWidth(line) > treeWidth {
		t.Errorf("repo con WorktreeErr: la línea mide %d: %q", lipglossWidth(line), line)
	}

	me := newRepoModel(t, []scanner.Project{
		{Path: repo, Name: long, Configured: true, Manifest: manifestNamed("repo", ""), WorktreeErr: "boom"},
		{Path: wt, Name: long + "-wt", Configured: true, Manifest: manifestNamed("api", ""),
			IsWorktree: true, RepoRoot: repo},
	}, nil)
	me.services[wt].Status = statusRunning
	if line := rowLine(t, me, long); lipglossWidth(line) > treeWidth {
		t.Errorf("repo con ⚠ y badge: la línea mide %d: %q", lipglossWidth(line), line)
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
		t.Fatal("falta la contenedora bare")
	}
	btree, _ := mb.treeLines()
	if w := lipglossWidth(btree[bi]); w > treeWidth {
		t.Errorf("contenedora bare con ⚠: la línea mide %d, excede treeWidth=%d: %q", w, treeWidth, btree[bi])
	}
}
