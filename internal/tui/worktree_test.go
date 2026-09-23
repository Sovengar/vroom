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
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// newRepoModel construye un modelo a partir de un slice plano ya anotado
// (sin git real): permite testear la presentación del anidado 0011 en
// aislamiento.
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

// repoFixture: main checkout en root/repo (primary X) y dos worktrees.
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

// S1: repo con worktrees = una sola fila colapsada; worktrees no top-level.
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
	if !m.collapsed[repoKey(repo)] && m.repoExpanded(repo) {
		t.Error("el repo debe estar colapsado por defecto")
	}
}

// S1b: repo sin worktrees no ofrece toggle.
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

// S1c: la fila del repo es el main checkout operable.
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

// S2a: expandir revela los worktrees indentados.
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
	// Indentación: las filas de worktree van 2 espacios más adentro que
	// la fila de su repo.
	idxRepo, idxA, idxB := findCursor(m2, "repo"), findCursor(m2, "repo-wt-a"), findCursor(m2, "repo-wt-b")
	if idxA < idxRepo || idxB < idxA {
		t.Fatalf("orden inesperado: repo=%d a=%d b=%d", idxRepo, idxA, idxB)
	}
	lineA := tree[idxA]
	if !strings.HasPrefix(lineA, "  ") {
		t.Errorf("el worktree debe ir indentado: %q", lineA)
	}
}

// S2b: un worktree anidado es operable con su propio workdir.
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

	// start arranca con el workdir del worktree.
	_, cmd := press(atWorktree(), "s")
	if cmd == nil {
		t.Fatal("start sobre un worktree debe emitir un comando")
	}
	if sm, ok := cmd().(startedMsg); !ok || sm.path != wtA {
		t.Fatalf("el servicio debe arrancar con workdir %s", wtA)
	}
	// build e install operan sobre el worktree (jobs independientes).
	if _, bcmd := press(atWorktree(), "b"); bcmd == nil {
		t.Fatal("build sobre un worktree debe emitir un comando")
	}
	if _, icmd := press(atWorktree(), "i"); icmd == nil {
		t.Fatal("install sobre un worktree debe emitir un comando")
	}
	// stop: con el servicio running, s para el worktree.
	m := atWorktree()
	m.services[wtA].Status = statusRunning
	if _, scmd := press(m, "s"); scmd == nil {
		t.Fatal("stop sobre un worktree debe emitir un comando")
	}
}

// S2c: el colapso del repo se restaura y no colisiona con las claves de grupo.
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
	// Plegar el repo no toca la clave del grupo "X" y viceversa.
	m = moveCursorTo(t, m, "repo")
	m2, _ := press(m, "enter")
	if m2.repoExpanded(repo) {
		t.Error("enter debe colapsar el repo")
	}
	if m2.collapsed["X"] {
		t.Error("el colapso del repo no debe tocar la clave del grupo X")
	}
	// Plegar el grupo X no colapsa/expande el repo.
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

// S3: bare repo se muestra como contenedor no ejecutable y anida sus worktrees.
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
	// Expandir anida el worktree.
	m3, _ := press(m2, "enter")
	tree, _ := m3.treeLines()
	if !strings.Contains(strings.Join(tree, "\n"), "bare-wt-a") {
		t.Errorf("al expandir el bare deben verse sus worktrees: %q", strings.Join(tree, "\n"))
	}
}

// S4a: un worktree detached muestra su sha como rama.
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

// S4b: worktree sin manifiesto se lista como no configurado y no es operable.
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

// S4e: worktree fuera del root se anida bajo un contenedor sintetizado.
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

// S5a: el repo conserva su grupo y posición; el toggle vive en su fila.
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

// S5b: el grupo propio de un worktree no lo reposiciona.
func TestWorktreeGroupIsInert(t *testing.T) {
	projects, repo, _, _ := repoFixture(t)
	// wt-a declara primary_group "Y" (debe ser inerte).
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

// ---- Integración con git real (0011) ----

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

// TestNewNestsRealGitWorktrees es el acceptance end-to-end: escaneo real,
// anotación de topología y anidado en el árbol de la TUI.
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
