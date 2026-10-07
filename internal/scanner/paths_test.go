package scanner

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/worktree"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// An error that does not name the root reads as an empty workspace.
func TestScanDaErrorCuandoElRootNoExiste(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-existe")

	_, err := Scan(missing, 3)
	if err == nil {
		t.Fatal("a nonexistent root should produce an error")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the error does not name the path that could not be read: %q", err)
	}
	if !strings.Contains(err.Error(), "could not access") {
		t.Errorf("the error does not say the problem is access: %q", err)
	}
}

// The two root failures are fixed differently, so they must not share a message.
func TestScanDaErrorCuandoElRootNoEsUnDirectorio(t *testing.T) {
	root := writeTree(t, map[string]string{"a-file.txt": "x"})

	_, err := Scan(filepath.Join(root, "a-file.txt"), 3)
	if err == nil {
		t.Fatal("a root that is a file should produce an error")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("err = %q, want 'not a directory'", err)
	}
}

// Parity is what makes "the CI runner has no fd" the same fact as "my machine has fd"; divergence would only show up on the other machine.
func TestScanSinFdDaLaMismaRespuestaQueConFd(t *testing.T) {
	files := map[string]string{
		"api/go.mod":              "module api\n",
		"api/.vroom.toml":         "name = \"api\"\ncommand_start = \"go run .\"\n",
		"api/.git/HEAD":           "ref: refs/heads/main\n",
		"api/.git/config":         "[core]\n",
		"web/package.json":        "{}\n",
		"web/.vroom.toml":         "name = \"web\"\ncommand_start = \"node .\"\n",
		"group/admin/.vroom.toml": "name = \"admin\"\ncommand_start = \"./admin\"\n",
		"group/admin/.git/config": "[core]\n",
		"no-manifest/go.mod":      "module x\n",
		"hidden/.vroom.toml":      "name = \"hidden\"\ncommand_start = \"./x\"\n",
	}
	root := writeTree(t, files)

	conFD, err := Scan(root, 3)
	if err != nil {
		t.Fatalf("with fd: %v", err)
	}
	if !conFD.UsedFD {
		t.Skip("no fd on this system: only the WalkDir path can be compared with itself")
	}

	// scanWith with fd == "" forces the WalkDir path; t.Setenv cannot reach the absolute fallbacks fdPath also checks.
	sinFD, err := scanWith(root, 3, "")
	if err != nil {
		t.Fatalf("without fd: %v", err)
	}
	if sinFD.UsedFD {
		t.Fatal("scanWith with empty fd says it used fd")
	}

	if got, want := pathsOf(sinFD.Projects), pathsOf(conFD.Projects); !equalStrings(got, want) {
		t.Errorf("the two scan paths differ:\n with fd: %v\nwithout fd: %v", want, got)
	}
}

func TestScanRespetaLaProfundidad(t *testing.T) {
	files := map[string]string{
		"level1/.vroom.toml":           "name = \"level1\"\ncommand_start = \"./a\"\n",
		"level1/level2/.vroom.toml":    "name = \"level2\"\ncommand_start = \"./b\"\n",
		"level1/level2/level3/.v.toml": "",
	}
	root := writeTree(t, files)

	tests := []struct {
		depth int
		want  []string
	}{
		{1, []string{"level1"}},
		{2, []string{"level1", "level2"}},
		{3, []string{"level1", "level2"}}, // level3 holds .v.toml, not a manifest
		{0, nil},
	}
	for _, tt := range tests {
		got, err := scanWith(root, tt.depth, "")
		if err != nil {
			t.Fatalf("depth %d: %v", tt.depth, err)
		}
		var names []string
		for _, p := range got.Projects {
			names = append(names, p.Name)
		}
		if tt.depth == 0 {
			if len(names) != 0 {
				t.Errorf("depth 0 yielded %v, want none: the root alone is not a project", names)
			}
			continue
		}
		if !equalStrings(names, tt.want) {
			t.Errorf("depth %d yielded %v, want %v", tt.depth, names, tt.want)
		}
	}
}

// The cut needs SkipDir, not continue, or the traversal keeps descending into the hundreds of thousands of files under node_modules.
func TestScanSaltaDirectoriosOcultosYDeDependencias(t *testing.T) {
	files := map[string]string{
		"api/.vroom.toml":                  "name = \"api\"\ncommand_start = \"./a\"\n",
		"api/node_modules/dep/.vroom.toml": "name = \"dep\"\ncommand_start = \"./d\"\n",
		"api/.cache/thing/.vroom.toml":     "name = \"cache\"\ncommand_start = \"./c\"\n",
		"api/.git/modules/x/.vroom.toml":   "name = \"module\"\ncommand_start = \"./m\"\n",
		"api/target/classes/.vroom.toml":   "name = \"target\"\ncommand_start = \"./t\"\n",
	}
	root := writeTree(t, files)

	res, err := scanWith(root, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	names := namesOf(res.Projects)
	if len(names) != 1 || names[0] != "api" {
		t.Errorf("found %v, want only api: hidden and dependency directories are not traversed", names)
	}
}

// A bare repo missing from the scan leaves its worktrees showing with no parent row.
func TestScanDetectaBareReposComoFilasContenedoras(t *testing.T) {
	bare := writeTree(t, map[string]string{
		"bare.git/HEAD":            "ref: refs/heads/main\n",
		"bare.git/config":          "[core]\n\tbare = true\n",
		"bare.git/objects/00/0000": "",
	})
	// The real git repo wins over the hand-written tree: bare detection is a heuristic over git's own layout.
	if repo := initBare(t); repo != "" {
		bare = filepath.Dir(repo)
	}

	res, err := Scan(bare, 2)
	if err != nil {
		t.Fatal(err)
	}
	var found *Project
	for i := range res.Projects {
		if res.Projects[i].IsBareContainer {
			found = &res.Projects[i]
		}
	}
	if found == nil {
		t.Fatalf("no container row came out: %v", namesOf(res.Projects))
	}
	if found.Configured {
		t.Error("a container row is not configured: it has no .vroom.toml")
	}
	if !found.IsNestedRow() {
		t.Error("a container row is a nested row: it renders under its repo, not in the group")
	}
}

// Accepting any of these yields a repo root of "" and hangs every worktree row off a repo that does not exist.
func TestReadGitDirRechazaLoQueNoEsUnPunteroAGit(t *testing.T) {
	dir := t.TempDir()

	t.Run("nonexistent file", func(t *testing.T) {
		if _, ok := readGitDir(dir, filepath.Join(dir, "nothing")); ok {
			t.Error("a nonexistent .git is not a pointer")
		}
	})

	t.Run("without the gitdir: prefix", func(t *testing.T) {
		p := writeStr(t, filepath.Join(dir, "a"), "/path/to/git\n")
		if _, ok := readGitDir(dir, p); ok {
			t.Error("a .git without 'gitdir:' is not a valid pointer")
		}
	})

	t.Run("pointer without path", func(t *testing.T) {
		p := writeStr(t, filepath.Join(dir, "b"), "gitdir:   \n")
		if _, ok := readGitDir(dir, p); ok {
			t.Error("'gitdir:' without a path is not a valid pointer")
		}
	})

	t.Run("relative path: resolved against the directory itself", func(t *testing.T) {
		sub := filepath.Join(dir, "rel")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		p := writeStr(t, filepath.Join(dir, "c"), "gitdir: rel\n")
		gd, ok := readGitDir(dir, p)
		if !ok {
			t.Fatal("a valid relative pointer was rejected")
		}
		if gd != filepath.Clean(sub) {
			t.Errorf("gitdir = %q, want %q", gd, filepath.Clean(sub))
		}
	})

	t.Run("absolute path: respected as-is", func(t *testing.T) {
		abs := filepath.Join(dir, "absolute")
		p := writeStr(t, filepath.Join(dir, "d"), "gitdir: "+abs+"\n")
		gd, ok := readGitDir(dir, p)
		if !ok {
			t.Fatal("a valid absolute pointer was rejected")
		}
		if gd != filepath.Clean(abs) {
			t.Errorf("gitdir = %q, want %q", gd, filepath.Clean(abs))
		}
	})
}

// Without commondir each worktree is its own repo and the TUI shows them as loose projects instead of under one row.
func TestCommonDirResuelveElRepoPrincipalDeUnWorktree(t *testing.T) {
	dir := t.TempDir()

	t.Run("without commondir: the gitdir itself", func(t *testing.T) {
		if got := commonDir(dir); got != dir {
			t.Errorf("commonDir = %q, want the gitdir itself %q", got, dir)
		}
	})

	t.Run("empty commondir: the gitdir itself", func(t *testing.T) {
		writeStr(t, filepath.Join(dir, "commondir"), "   \n")
		if got := commonDir(dir); got != dir {
			t.Errorf("commonDir = %q with an empty commondir, want the gitdir itself", got)
		}
	})

	t.Run("relative commondir: resolved against the gitdir", func(t *testing.T) {
		wtDir := filepath.Join(dir, ".git", "worktrees", "wt")
		if err := os.MkdirAll(wtDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// MEASURED in a real repo: <repo>/.git/worktrees/<name>/commondir is "../.."; "../../.." climbs one level too far.
		writeStr(t, filepath.Join(wtDir, "commondir"), "../..\n")
		want := filepath.Clean(filepath.Join(dir, ".git"))
		if got := commonDir(wtDir); got != want {
			t.Errorf("commonDir = %q, want the .git of main %q", got, want)
		}
	})

	t.Run("absolute commondir: respected", func(t *testing.T) {
		abs := filepath.Join(dir, "shared")
		wtDir := filepath.Join(dir, "other")
		if err := os.MkdirAll(wtDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeStr(t, filepath.Join(wtDir, "commondir"), abs+"\n")
		if got := commonDir(wtDir); got != filepath.Clean(abs) {
			t.Errorf("commonDir = %q, want %q", got, filepath.Clean(abs))
		}
	})
}

// /a/bc starts with /a/b but is outside, which is the case a naive HasPrefix gets wrong.
func TestWithinRootNoDejaSalirAlArbol(t *testing.T) {
	root := "/srv/work"
	tests := []struct {
		path string
		want bool
	}{
		{"/srv/work/api", true},
		{"/srv/work", true},
		{"/srv/work/grupo/api", true},
		{"/srv/work/../other", false},
		{"/srv/work-other/api", false}, // same prefix, outside
		{"/etc/passwd", false},
	}
	for _, tt := range tests {
		if got := withinRoot(root, tt.path); got != tt.want {
			t.Errorf("withinRoot(%q, %q) = %v, want %v", root, tt.path, got, tt.want)
		}
	}

	// A Rel that cannot be computed counts as outside, the closed failure.
	if withinRoot("relativo", "/absoluto") {
		t.Error("a comparison that cannot be made cannot say 'inside'")
	}
}

// Group aggregation excludes nested rows; counting one in both places makes the group header disagree with the rows under it.
func TestIsNestedRowCubreLasDosFormasDeFilaAnidada(t *testing.T) {
	tests := []struct {
		name string
		p    Project
		want bool
	}{
		{"normal project", Project{}, false},
		{"normal project with repo", Project{RepoRoot: "/repo"}, false},
		{"linked worktree", Project{IsWorktree: true}, true},
		{"bare container row", Project{IsBareContainer: true}, true},
		{"worktree that is also a root repo", Project{IsWorktree: true, IsBareContainer: true}, true},
	}
	for _, tt := range tests {
		if got := tt.p.IsNestedRow(); got != tt.want {
			t.Errorf("%s: IsNestedRow() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestFdPathDevuelveAlgoDistintoDeCadenaVaciaOElFichero(t *testing.T) {
	got := fdPath()
	if got == "" {
		t.Skip("no fd on this system: only the absence branch can be tested")
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("fdPath returned %q, which does not exist: %v", got, err)
	}
	if info.IsDir() {
		t.Errorf("fdPath returned a directory: %q", got)
	}
	if !strings.Contains(got, "fd") {
		t.Errorf("fdPath returned %q, which does not look like fd", got)
	}
}

// A normal repo with worktrees has a .git directory; read as bare, its worktree becomes the parent row and the manifested repo drops out of its group.
func TestIsBareRepoDeVerdadYDeMentira(t *testing.T) {
	t.Run("a normal repo is not bare", func(t *testing.T) {
		root := writeTree(t, map[string]string{
			"repo/.git/HEAD":   "ref: refs/heads/main\n",
			"repo/.git/config": "[core]\n\tbare = false\n",
		})
		if worktree.IsBareRepo(filepath.Join(root, "repo")) {
			t.Error("a repo with worktree (.git is a directory) is not bare")
		}
	})

	t.Run("a real bare repo is", func(t *testing.T) {
		bare := t.TempDir()
		if !worktree.IsBareRepo(bare) {
			t.Skip("the helper needs a real bare repo: git not available or layout change")
		}
	})

	t.Run("a normal directory is not bare", func(t *testing.T) {
		if worktree.IsBareRepo(t.TempDir()) {
			t.Error("an empty directory is not a bare repo")
		}
	})
}

func writeStr(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func pathsOf(projects []Project) []string {
	out := make([]string, 0, len(projects))
	for _, p := range projects {
		out = append(out, p.Path)
	}
	return out
}

func namesOf(projects []Project) []string {
	out := make([]string, 0, len(projects))
	for _, p := range projects {
		out = append(out, p.Name)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func initBare(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := filepath.Join(t.TempDir(), "repo.git")
	runGit(t, t.TempDir(), "init", "--bare", "-q", dir)
	return dir
}

// A failing fd must not silently degrade to WalkDir, since a scan whose result depends on whether fd failed is the worst surprise; the error must carry fd's own output, otherwise the user has to reproduce the run by hand.
func TestScanConFdQueFallaDaErrorConSuSalida(t *testing.T) {
	root := writeTree(t, map[string]string{"api/.vroom.toml": "name = \"api\"\n"})
	failing := writeStr(t, filepath.Join(t.TempDir(), "fd-broken"), "#!/bin/sh\necho 'boom in fd' >&2\nexit 2\n")
	if err := os.Chmod(failing, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := scanWith(root, 3, failing)
	if err == nil {
		t.Fatal("with fd failing the scan should fail, not silently degrade to WalkDir")
	}
	if !strings.Contains(err.Error(), "fd failed") {
		t.Errorf("err = %q, want the prefix 'fd failed'", err)
	}
	if !strings.Contains(err.Error(), "boom in fd") {
		t.Errorf("the error does not include fd's output: %q", err)
	}

	if _, err := scanBareReposWithFD(failing, root, 3); err == nil {
		t.Error("scanBareReposWithFD with failing fd should produce an error")
	} else if !strings.Contains(err.Error(), "fd (dirs) failed") {
		t.Errorf("err = %q, want the prefix of the second invocation", err)
	}
}

// An empty workspace is a valid result; treating it as an error would break vroom list on a freshly created directory.
func TestScanConFdQueNoEncuentraNadaDaUnaListaVacia(t *testing.T) {
	root := writeTree(t, map[string]string{"vacio.txt": "x"})
	empty := writeStr(t, filepath.Join(t.TempDir(), "fd-empty"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(empty, 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := scanWith(root, 3, empty)
	if err != nil {
		t.Fatalf("fd that finds nothing is not an error: %v", err)
	}
	if len(res.Projects) != 0 {
		t.Errorf("found %v with an fd that finds nothing", namesOf(res.Projects))
	}
	if !res.UsedFD {
		t.Error("UsedFD = false: fd was used, even if it found nothing")
	}
}

// This is the half of the scan that never runs on a machine with fd, and it has its own depth arithmetic.
func TestScanPorWalkConBareRepoYProfundidad(t *testing.T) {
	requireGit(t)
	root := t.TempDir()

	bare := filepath.Join(root, "repo.git")
	runGit(t, root, "init", "--bare", "-q", bare)

	// git worktree add, not git clone: a clone has its own .git directory, so it is not a worktree and the assertion would prove nothing.
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	writeStr(t, filepath.Join(repo, ".vroom.toml"), "name = \"repo\"\ncommand_start = \"./x\"\n")
	writeStr(t, filepath.Join(repo, "go.mod"), "module repo\n")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "init")
	wt := filepath.Join(root, "feature")
	runGit(t, repo, "worktree", "add", "-q", wt, "-b", "feature")

	res, err := scanWith(root, 4, "")
	if err != nil {
		t.Fatal(err)
	}
	var containers, worktrees int
	for _, p := range res.Projects {
		if p.IsBareContainer {
			containers++
		}
		if p.IsWorktree {
			worktrees++
		}
	}
	if containers != 1 {
		t.Errorf("there are %d container rows, want 1 (the bare repo)", containers)
	}
	if worktrees != 1 {
		t.Errorf("there are %d worktrees, want 1: the walk must recognize .git as a file", worktrees)
	}

	shallow, err := scanWith(root, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(shallow.Projects) != 0 {
		t.Errorf("with depth 0 %v appeared: the root alone is not inspected as a project", namesOf(shallow.Projects))
	}
}

// A partial list would show a workspace with fewer projects than the user has, and they would believe it.
func TestScanPorWalkFallaSiElWalkFalla(t *testing.T) {
	orig := walkDir
	t.Cleanup(func() { walkDir = orig })
	walkErr := errors.New("permission denied in a subdirectory")
	calls := 0
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		calls++
		// The error travels through the callback, which is how real WalkDir reports an unreadable directory.
		_ = fn(root, nil, walkErr)
		return walkErr
	}

	root := writeTree(t, map[string]string{"api/.vroom.toml": "name = \"api\"\n"})
	res, err := scanWith(root, 3, "")
	if calls != 1 {
		t.Errorf("the walk was called %d times, want 1: the scan must not retry", calls)
	}
	if err == nil {
		t.Fatal("a failed walk should produce an error, not a partial list")
	}
	if !strings.Contains(err.Error(), "error walking") {
		t.Errorf("err = %q, want the prefix 'error walking'", err)
	}
	if len(res.Projects) != 0 {
		t.Errorf("with an error a partial list must not be returned: %v", namesOf(res.Projects))
	}
}

// git init writes HEAD before config, so a scan landing in between would declare a half-built directory a repo.
func TestRepoKeyRechazaUnGitAMedioConstruir(t *testing.T) {
	medio := writeTree(t, map[string]string{
		"repo/.git/HEAD": "ref: refs/heads/main\n",
	})
	if got := repoKey(filepath.Join(medio, "repo")); got != "" {
		t.Errorf("repoKey = %q with a .git without config, want \"\"", got)
	}

	completo := writeTree(t, map[string]string{
		"repo/.git/HEAD":   "ref: refs/heads/main\n",
		"repo/.git/config": "[core]\n",
	})
	want := filepath.Clean(filepath.Join(completo, "repo", ".git"))
	if got := repoKey(filepath.Join(completo, "repo")); got != want {
		t.Errorf("repoKey = %q, want %q", got, want)
	}
}

// A .git file that is not a pointer is what a badly copied repo looks like; accepting it groups the project under an empty root.
func TestRepoKeyRechazaUnGitFicheroQueNoEsUnPuntero(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"repo/.git":        "this is not a pointer\n",
		"repo/.vroom.toml": "name = \"repo\"\ncommand_start = \"./x\"\n",
	})
	if got := repoKey(filepath.Join(dir, "repo")); got != "" {
		t.Errorf("repoKey = %q with a .git that is not a pointer, want \"\"", got)
	}
}

// Two rows for one service make stop leave the other claiming a PID.
func TestFinalizeNoDuplicaLaFilaDeUnBareQueTambienEsProyecto(t *testing.T) {
	dir := "/srv/double"
	projects := []Project{{Path: dir, Name: "double", Configured: true}}
	bare := []Project{{Path: dir, Name: "double", IsBareContainer: true}}

	got := finalize(projects, bare, "/srv")
	if len(got) != 1 {
		t.Fatalf("there are %d rows, want 1: the same path cannot appear twice", len(got))
	}
	// The manifest-scan row wins because it is the one carrying the Manifest.
	if !got[0].Configured {
		t.Error("the duplicated row kept the version without manifest: the service would look unmanageable")
	}

	dup := []Project{{Path: dir, IsBareContainer: true}, {Path: dir, IsBareContainer: true}}
	if got := finalize(nil, dup, "/srv"); len(got) != 1 {
		t.Errorf("there are %d rows with two bares of the same path, want 1", len(got))
	}
}

// Neither scan path repeats a path; finalize is where that is guaranteed.
func TestFinalizeDedupsProyectosRepetidos(t *testing.T) {
	dir := "/srv/double"
	dup := []Project{
		{Path: dir, Name: "double", Configured: true},
		{Path: dir, Name: "double", Configured: true},
	}
	if got := finalize(dup, nil, "/srv"); len(got) != 1 {
		t.Errorf("there are %d rows with two projects of the same path, want 1", len(got))
	}
}

func TestScanCortaElRecorridoCuandoSePasaDeProfundidad(t *testing.T) {
	root := writeTree(t, map[string]string{
		// The manifest sits 4 levels down and depth is 2, so it cannot show up.
		"a/b/c/d/.vroom.toml": "name = \"deep\"\ncommand_start = \"./x\"\n",
		"a/.vroom.toml":       "name = \"a\"\ncommand_start = \"./x\"\n",
	})

	res, err := scanWith(root, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	names := namesOf(res.Projects)
	if len(names) != 1 || names[0] != "a" {
		t.Errorf("found %v with depth 2, want only a: the traversal must be cut short", names)
	}

	zero, err := scanWith(root, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(zero.Projects) != 0 {
		t.Errorf("with depth 0 %v appeared: no child can be entered", namesOf(zero.Projects))
	}

	// The row name is the directory, not the manifest; both scan paths do that and findProject has a second pass.
	more, err := scanWith(root, 5, "")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(namesOf(more.Projects), "d") {
		t.Errorf("with depth 5 the 4-level project did not come out either: %v", namesOf(more.Projects))
	}
}

// Only relative roots are affected: filepath.Abs of an absolute path needs no CWD, and the CLI always passes an absolute root.
func TestScanConElCwdBorradoDaError(t *testing.T) {
	gone := t.TempDir()
	t.Chdir(gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { t.Chdir(t.TempDir()) })

	if _, err := scanWith("relativo", 3, ""); err == nil {
		t.Error("without CWD, a relative path cannot be resolved: it should produce an error, not scan another directory")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
