package scanner

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree construye un árbol en un directorio temporal.
type tree struct {
	t    *testing.T
	root string
}

func newTree(t *testing.T) *tree {
	return &tree{t: t, root: t.TempDir()}
}

func (tr *tree) mkdir(rel string) *tree {
	if err := os.MkdirAll(filepath.Join(tr.root, rel), 0o755); err != nil {
		tr.t.Fatal(err)
	}
	return tr
}

func (tr *tree) file(rel, content string) *tree {
	full := filepath.Join(tr.root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		tr.t.Fatal(err)
	}
	return tr
}

func (tr *tree) path() string { return tr.root }

func find(projects []Project, name string) *Project {
	for i := range projects {
		if projects[i].Name == name {
			return &projects[i]
		}
	}
	return nil
}

// Solo se detectan proyectos con .vroom.toml
func TestScanDetectsVroomTomlProject(t *testing.T) {
	tr := newTree(t).
		mkdir("myapp").
		file("myapp/.vroom.toml", "name = \"myapp\"\ncommand_start = \"go run main.go\"\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "myapp")
	if p == nil {
		t.Fatalf("proyecto myapp no detectado: %+v", result.Projects)
	}
	if !p.Configured {
		t.Error("proyecto con manifiesto válido debe estar configurado")
	}
}

// Directorio sin .vroom.toml NO es proyecto
func TestScanIgnoresDirsWithoutManifest(t *testing.T) {
	tr := newTree(t).
		mkdir("no-manifest").
		mkdir("another").
		file("no-manifest/go.mod", "module nope\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 0 {
		t.Errorf("debe ignorar dirs sin .vroom.toml, got %+v", result.Projects)
	}
}

// Depth 2 con .vroom.toml se detecta
func TestScanDepth2(t *testing.T) {
	tr := newTree(t).
		file("a/b/.vroom.toml", "name = \"b\"\ncommand_start = \"echo hi\"\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 1 || result.Projects[0].Name != "b" {
		t.Errorf("proyecto a depth 2 debe detectarse, got %+v", result.Projects)
	}
}

// Depth 3 se ignora con depth=2
func TestScanDepth3IgnoredWithDepth2(t *testing.T) {
	tr := newTree(t).
		file("a/b/c/.vroom.toml", "name = \"too-deep\"\ncommand_start = \"echo\"\n")
	result, err := Scan(tr.path(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 0 {
		t.Errorf("proyecto a depth 3 debe ignorarse con depth=2, got %+v", result.Projects)
	}
}

// Depth 3 se detecta con depth=4
func TestScanDepth3DetectedWithDepth4(t *testing.T) {
	tr := newTree(t).
		file("a/b/c/.vroom.toml", "name = \"c\"\ncommand_start = \"echo\"\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 1 || result.Projects[0].Name != "c" {
		t.Errorf("proyecto a depth 3 debe detectarse con depth=4, got %+v", result.Projects)
	}
}

// Manifiesto malformado sigue visible pero no marcado como configurado
func TestScanMalformedManifest(t *testing.T) {
	tr := newTree(t).
		file("broken/.vroom.toml", "name = [toml roto")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "broken")
	if p == nil {
		t.Fatal("proyecto con manifiesto malformado debe seguir visible")
	}
	if p.Configured {
		t.Error("manifiesto malformado no debe marcarse configurado")
	}
	if p.ManifestErr == "" {
		t.Error("debe reportar el error de parseo")
	}
}

// fd con --hidden encuentra .vroom.toml en directorios ocultos (correcto)
func TestScanFindsVroomTomlInHiddenDirs(t *testing.T) {
	tr := newTree(t).
		file(".hidden/.vroom.toml", "name = \"h\"\ncommand_start = \"echo\"\n").
		file("real/.vroom.toml", "name = \"real\"\ncommand_start = \"echo\"\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 2 {
		t.Errorf("esperaba 2 proyectos (incluyendo .hidden), got %d: %v", len(result.Projects), projectNames(result.Projects))
	}
}

// Orden por ruta
func TestScanSortsByPath(t *testing.T) {
	tr := newTree(t).
		file("zebra/.vroom.toml", "name = \"zebra\"\ncommand_start = \"echo z\"\n").
		file("alpha/.vroom.toml", "name = \"alpha\"\ncommand_start = \"echo a\"\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 2 {
		t.Fatalf("esperaba 2 proyectos, got %d", len(result.Projects))
	}
	if result.Projects[0].Name != "alpha" || result.Projects[1].Name != "zebra" {
		t.Errorf("orden inesperado: %s, %s", result.Projects[0].Name, result.Projects[1].Name)
	}
}

// Manifiesto con grupos
func TestScanManifestWithGroups(t *testing.T) {
	tr := newTree(t).
		file("api/.vroom.toml", "name = \"api\"\nprimary_group = \"tienda\"\nsecondary_group = \"backend\"\ncommand_start = \"go run .\"\nport = 8080\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 1 {
		t.Fatalf("esperaba 1 proyecto, got %+v", result.Projects)
	}
	p := result.Projects[0]
	if !p.Configured || p.Manifest == nil {
		t.Error("debe estar configurado")
	}
	if p.Manifest.PrimaryGroup != "tienda" || p.Manifest.SecondaryGroup != "backend" {
		t.Errorf("groups = %q/%q, want tienda/backend", p.Manifest.PrimaryGroup, p.Manifest.SecondaryGroup)
	}
	if p.Manifest.Port != 8080 {
		t.Errorf("port = %d, want 8080", p.Manifest.Port)
	}
}

// El playground completo se escanea (depth=2 desde playground/)
func TestScanPlaygroundFixture(t *testing.T) {
	result, err := Scan(filepath.Join("..", "..", "playground"), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 8 {
		t.Fatalf("esperaba 8 proyectos, got %d: %v", len(result.Projects), projectNames(result.Projects))
	}
	for _, p := range result.Projects {
		if !p.Configured || p.Manifest == nil {
			t.Errorf("%s: debe estar configurado", p.Name)
		}
	}
}

// ScanResult indica si usó fd o WalkDir
func TestScanResultIndicatesMethod(t *testing.T) {
	tr := newTree(t).
		file("proj/.vroom.toml", "name = \"proj\"\ncommand_start = \"echo\"\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if fdPath() != "" && !result.UsedFD {
		t.Error("fd disponible pero UsedFD=false")
	}
	if fdPath() == "" && result.UsedFD {
		t.Error("fd no disponible pero UsedFD=true")
	}
}

func projectNames(projects []Project) string {
	var names []string
	for _, p := range projects {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

// ---- Topología repo/worktree ----

// fakeGitPATH escribe un git falso con el porcelain dado y devuelve un
// PATH que lo antepone al real (degradación y topología sin git real).
func fakeGitPATH(t *testing.T, output string, code int) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'EOF'\n" + output + "EOF\nexit " + codeStr(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func codeStr(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}

// porcelainRepo construye el porcelain de un repo con sus worktrees.
func porcelainRepo(main string, worktrees ...string) string {
	var b strings.Builder
	b.WriteString("worktree " + main + "\nHEAD aaaa000000000000000000000000000000000000\nbranch refs/heads/main\n\n")
	for i, wt := range worktrees {
		b.WriteString("worktree " + wt + "\nHEAD bbbb000000000000000000000000000000000000\nbranch refs/heads/wt" + string(rune('a'+i)) + "\n\n")
	}
	return b.String()
}

// Un worktree in-root se anota con RepoRoot y no aparece como top-level
// por sí mismo (el slice sigue plano).
func TestScanAnnotatesWorktrees(t *testing.T) {
	tr := newTree(t).
		file("repo/.vroom.toml", "name = \"repo\"\ncommand_start = \"echo\"\n").
		file("repo/.git/config", "[core]\n\tbare = false\n").
		file("repo/.git/HEAD", "ref: refs/heads/main\n").
		file("repo-wt-a/.vroom.toml", "name = \"api\"\ncommand_start = \"echo\"\n").
		file("repo-wt-a/.git", "gitdir: /nowhere/.git/worktrees/a\n")
	main := filepath.Join(tr.path(), "repo")
	wtA := filepath.Join(tr.path(), "repo-wt-a")
	t.Setenv("PATH", fakeGitPATH(t, porcelainRepo(main, wtA), 0))

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "repo-wt-a")
	if p == nil {
		t.Fatalf("worktree repo-wt-a no detectado: %v", projectNames(result.Projects))
	}
	if !p.IsWorktree || p.RepoRoot != main {
		t.Errorf("anotación = worktree:%v repoRoot:%q, want true %q", p.IsWorktree, p.RepoRoot, main)
	}
	// El main checkout no es worktree.
	if r := find(result.Projects, "repo"); r == nil || r.IsWorktree {
		t.Errorf("el main checkout no debe marcarse worktree: %+v", r)
	}
}

// Un worktree sin manifiesto se sintetiza como fila no configurada.
func TestScanSynthesizesUnconfiguredWorktree(t *testing.T) {
	tr := newTree(t).
		file("repo/.vroom.toml", "name = \"repo\"\ncommand_start = \"echo\"\n").
		file("repo/.git/config", "[core]\n\tbare = false\n").
		mkdir("repo-wt-sin-mf")
	main := filepath.Join(tr.path(), "repo")
	wt := filepath.Join(tr.path(), "repo-wt-sin-mf")
	t.Setenv("PATH", fakeGitPATH(t, porcelainRepo(main, wt), 0))

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "repo-wt-sin-mf")
	if p == nil {
		t.Fatalf("worktree sin manifiesto no sintetizado: %v", projectNames(result.Projects))
	}
	if p.Configured || !p.IsWorktree || p.RepoRoot != main {
		t.Errorf("fila sintetizada inesperada: %+v", p)
	}
}

// Un worktree prunable no aparece como fila.
func TestScanSkipsPrunableWorktree(t *testing.T) {
	tr := newTree(t).
		file("repo/.vroom.toml", "name = \"repo\"\ncommand_start = \"echo\"\n").
		file("repo/.git/config", "[core]\n\tbare = false\n")
	main := filepath.Join(tr.path(), "repo")
	gone := filepath.Join(tr.path(), "repo-wt-gone")
	out := porcelainRepo(main) + "worktree " + gone + "\nHEAD cccc000000000000000000000000000000000000\nprunable gitdir file points to non-existent location\n\n"
	t.Setenv("PATH", fakeGitPATH(t, out, 0))

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if p := find(result.Projects, "repo-wt-gone"); p != nil {
		t.Errorf("worktree prunable no debe aparecer: %+v", p)
	}
}

// Un worktree prunable cuyo directorio sigue existiendo se trata como
// worktree normal (se anota/nida), no se omite.
func TestScanKeepsPrunableWorktreeWhenDirExists(t *testing.T) {
	tr := newTree(t).
		file("repo/.vroom.toml", "name = \"repo\"\ncommand_start = \"echo\"\n").
		file("repo/.git/config", "[core]\n\tbare = false\n").
		file("repo-wt-a/.vroom.toml", "name = \"api\"\ncommand_start = \"echo\"\n").
		file("repo-wt-a/.git", "gitdir: /nowhere/.git/worktrees/a\n")
	main := filepath.Join(tr.path(), "repo")
	wtA := filepath.Join(tr.path(), "repo-wt-a")
	out := porcelainRepo(main) + "worktree " + wtA + "\nHEAD bbbb000000000000000000000000000000000000\nbranch refs/heads/wta\nprunable gitdir file points to non-existent location\n\n"
	t.Setenv("PATH", fakeGitPATH(t, out, 0))

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "repo-wt-a")
	if p == nil || !p.IsWorktree || p.RepoRoot != main {
		t.Errorf("prunable con directorio existente debe anotarse como worktree: %+v", p)
	}
}

// Un bare repo se detecta y se expone como contenedor no configurado.
func TestScanDetectsBareRepo(t *testing.T) {
	tr := newTree(t).
		file("bare/HEAD", "ref: refs/heads/main\n").
		file("bare/config", "[core]\n\tbare = true\n").
		mkdir("bare/objects").
		mkdir("bare/refs").
		file("normal/.git/HEAD", "ref: refs/heads/main\n").
		file("normal/.vroom.toml", "name = \"normal\"\ncommand_start = \"echo\"\n")
	t.Setenv("PATH", fakeGitPATH(t, "", 0))

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	b := find(result.Projects, "bare")
	if b == nil {
		t.Fatalf("bare repo no detectado: %v", projectNames(result.Projects))
	}
	if !b.IsBareContainer || b.Configured || b.Manifest != nil {
		t.Errorf("contenedor bare inesperado: %+v", b)
	}
	if n := find(result.Projects, "normal"); n == nil || n.IsBareContainer {
		t.Errorf("dir con .git no debe ser contenedor: %+v", n)
	}
}

// finalize deduplica la fila contenedora bare cuando ya existe un
// proyecto con esa ruta (no se duplica la fila).
func TestFinalizeDedupsBareContainer(t *testing.T) {
	dir := t.TempDir()
	project := Project{Path: dir, Name: "bare", Configured: true}
	bare := Project{Path: dir, Name: "bare", IsBareContainer: true}
	got := finalize([]Project{project}, []Project{bare}, t.TempDir())
	if len(got) != 1 {
		t.Fatalf("esperaba 1 fila, got %d: %+v", len(got), got)
	}
	if got[0].IsBareContainer || !got[0].Configured {
		t.Errorf("debe conservarse el proyecto, no el contenedor: %+v", got[0])
	}
}

// Un bare repo se consulta vía git aunque no tenga .git: sus worktrees// in-root sin manifiesto se descubren y se sintetizan anidadas (H1).
func TestScanBareRepoDiscoversManifestlessWorktree(t *testing.T) {
	tr := newTree(t).
		file("bare/HEAD", "ref: refs/heads/main\n").
		file("bare/config", "[core]\n\tbare = true\n").
		mkdir("bare/objects").
		mkdir("bare/refs").
		mkdir("bare-wt-a")
	bare := filepath.Join(tr.path(), "bare")
	wt := filepath.Join(tr.path(), "bare-wt-a")
	t.Setenv("PATH", fakeGitPATH(t, porcelainRepo(bare, wt), 0))

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	b := find(result.Projects, "bare")
	if b == nil || !b.IsBareContainer {
		t.Fatalf("bare container no detectado: %+v", b)
	}
	p := find(result.Projects, "bare-wt-a")
	if p == nil {
		t.Fatalf("worktree sin manifiesto del bare no sintetizado: %v", projectNames(result.Projects))
	}
	if !p.IsWorktree || p.RepoRoot != bare || p.Configured {
		t.Errorf("worktree del bare mal anotado: %+v", p)
	}
}

// Git ausente degrada: los proyectos siguen y se registra el motivo.
func TestScanDegradesWhenGitUnavailable(t *testing.T) {
	tr := newTree(t).
		file("repo/.vroom.toml", "name = \"repo\"\ncommand_start = \"echo\"\n").
		file("repo/.git/config", "[core]\n\tbare = false\n")
	t.Setenv("PATH", t.TempDir()) // sin git (ni fd): degrada, no crashea

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "repo")
	if p == nil {
		t.Fatal("el proyecto debe seguir visible sin git")
	}
	if p.WorktreeErr == "" {
		t.Error("debe registrar el motivo de la degradación")
	}
}

// git worktree list con exit != 0 se trata como sin worktrees + error.
func TestScanWorktreeListFailure(t *testing.T) {
	tr := newTree(t).
		file("repo/.vroom.toml", "name = \"repo\"\ncommand_start = \"echo\"\n").
		file("repo/.git/config", "[core]\n\tbare = false\n")
	t.Setenv("PATH", fakeGitPATH(t, "boom\n", 1))

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "repo")
	if p == nil || p.WorktreeErr == "" {
		t.Errorf("debe registrar el error de topología: %+v", p)
	}
}

// Salida malformada de porcelain se trata como sin worktrees + error.
func TestScanWorktreeListMalformed(t *testing.T) {
	tr := newTree(t).
		file("repo/.vroom.toml", "name = \"repo\"\ncommand_start = \"echo\"\n").
		file("repo/.git/config", "[core]\n\tbare = false\n")
	t.Setenv("PATH", fakeGitPATH(t, "not a porcelain\n", 0))

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "repo")
	if p == nil || p.WorktreeErr == "" {
		t.Errorf("salida malformada debe registrar error: %+v", p)
	}
}

// Worktrees cuyo main checkout está fuera del root se anotan igual
// (RepoRoot apunta fuera; el contenedor lo sintetiza la TUI).
func TestScanWorktreeOutsideRoot(t *testing.T) {
	tr := newTree(t).
		file("repo-wt-a/.vroom.toml", "name = \"api\"\ncommand_start = \"echo\"\n").
		file("repo-wt-a/.git", "gitdir: /elsewhere/.git/worktrees/a\n")
	outside := "/outside/repo"
	wtA := filepath.Join(tr.path(), "repo-wt-a")
	t.Setenv("PATH", fakeGitPATH(t, porcelainRepo(outside, wtA), 0))

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "repo-wt-a")
	if p == nil || !p.IsWorktree || p.RepoRoot != outside {
		t.Fatalf("worktree fuera de root mal anotado: %+v", p)
	}
}

// M2: el camino WalkDir detecta bare repos en un único recorrido (sin un
// segundo walk).
func TestScanWithWalkDetectsBareWithoutExtraWalk(t *testing.T) {
	tr := newTree(t).
		file("bare/HEAD", "ref: refs/heads/main\n").
		file("bare/config", "[core]\n\tbare = true\n").
		mkdir("bare/objects").
		mkdir("bare/refs").
		file("proj/.vroom.toml", "name = \"proj\"\ncommand_start = \"echo\"\n")

	calls := 0
	orig := walkDir
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		calls++
		return orig(root, fn)
	}
	defer func() { walkDir = orig }()

	projects, bare, err := scanWithWalk(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("walkDir invocado %d veces, want 1 (sin walk extra)", calls)
	}
	if find(projects, "proj") == nil {
		t.Error("proyecto no detectado en el walk")
	}
	if find(bare, "bare") == nil {
		t.Error("bare repo no detectado en el walk")
	}
}

// M2: el camino fd detecta bare repos sin usar WalkDir en absoluto.
func TestScanWithFDUsesNoWalkForBare(t *testing.T) {
	tr := newTree(t).
		file("bare/HEAD", "ref: refs/heads/main\n").
		file("bare/config", "[core]\n\tbare = true\n").
		mkdir("bare/objects").
		mkdir("bare/refs").
		file("proj/.vroom.toml", "name = \"proj\"\ncommand_start = \"echo\"\n")

	manifest := filepath.Join(tr.path(), "proj", ".vroom.toml")
	dirs := filepath.Join(tr.path(), "proj") + "\n" + filepath.Join(tr.path(), "bare")
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n  *\"--type d\"*) printf '%s\\n' \"" + dirs + "\" ;;\n  *) printf '%s\\n' \"" + manifest + "\" ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "fd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	calls := 0
	orig := walkDir
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		calls++
		return orig(root, fn)
	}
	defer func() { walkDir = orig }()

	projects, bare, err := scanWithFD(filepath.Join(bin, "fd"), tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("scanWithFD no debe usar WalkDir, got %d invocaciones", calls)
	}
	if find(projects, "proj") == nil {
		t.Error("proyecto no detectado por fd")
	}
	if find(bare, "bare") == nil {
		t.Error("bare repo no detectado por fd")
	}
}

// countingGitPATH escribe un git falso que incrementa un contador por
// invocación y devuelve un PATH que lo antepone al real.
func countingGitPATH(t *testing.T, counter, output string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho x >> \"" + counter + "\"\ncat <<'EOF'\n" + output + "EOF\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func gitCallCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "x")
}

// M3: un repo con varios worktrees en el scan invoca git una sola vez.
func TestQueryWorktreeRelationsOneGitCallPerRepo(t *testing.T) {
	tr := newTree(t)
	repo := filepath.Join(tr.path(), "repo")
	wtA := filepath.Join(tr.path(), "repo-wt-a")
	wtB := filepath.Join(tr.path(), "repo-wt-b")
	tr.file("repo/.git/config", "[core]\n\tbare = false\n").
		file("repo/.vroom.toml", "name = \"repo\"\ncommand_start = \"echo\"\n").
		file("repo-wt-a/.git", "gitdir: "+filepath.Join(repo, ".git", "worktrees", "a")+"\n").
		file("repo/.git/worktrees/a/commondir", "../..\n").
		file("repo-wt-a/.vroom.toml", "name = \"api\"\ncommand_start = \"echo\"\n").
		file("repo-wt-b/.git", "gitdir: "+filepath.Join(repo, ".git", "worktrees", "b")+"\n").
		file("repo/.git/worktrees/b/commondir", "../..\n").
		file("repo-wt-b/.vroom.toml", "name = \"api\"\ncommand_start = \"echo\"\n")

	counter := filepath.Join(t.TempDir(), "count")
	t.Setenv("PATH", countingGitPATH(t, counter, porcelainRepo(repo, wtA, wtB)))

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if calls := gitCallCount(t, counter); calls != 1 {
		t.Errorf("git invocado %d veces, want 1 (una consulta por repo)", calls)
	}
	for _, name := range []string{"repo-wt-a", "repo-wt-b"} {
		p := find(result.Projects, name)
		if p == nil || !p.IsWorktree || p.RepoRoot != repo {
			t.Errorf("%s mal anotado: %+v", name, p)
		}
	}
}
