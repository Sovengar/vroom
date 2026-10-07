package scanner

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		t.Fatalf("myapp project not detected: %+v", result.Projects)
	}
	if !p.Configured {
		t.Error("project with valid manifest must be configured")
	}
}

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
		t.Errorf("must ignore dirs without .vroom.toml, got %+v", result.Projects)
	}
}

func TestScanDepth2(t *testing.T) {
	tr := newTree(t).
		file("a/b/.vroom.toml", "name = \"b\"\ncommand_start = \"echo hi\"\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 1 || result.Projects[0].Name != "b" {
		t.Errorf("project at depth 2 must be detected, got %+v", result.Projects)
	}
}

func TestScanDepth3IgnoredWithDepth2(t *testing.T) {
	tr := newTree(t).
		file("a/b/c/.vroom.toml", "name = \"too-deep\"\ncommand_start = \"echo\"\n")
	result, err := Scan(tr.path(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 0 {
		t.Errorf("project at depth 3 must be ignored with depth=2, got %+v", result.Projects)
	}
}

func TestScanDepth3DetectedWithDepth4(t *testing.T) {
	tr := newTree(t).
		file("a/b/c/.vroom.toml", "name = \"c\"\ncommand_start = \"echo\"\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 1 || result.Projects[0].Name != "c" {
		t.Errorf("project at depth 3 must be detected with depth=4, got %+v", result.Projects)
	}
}

func TestScanMalformedManifest(t *testing.T) {
	tr := newTree(t).
		file("broken/.vroom.toml", "name = [broken toml")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "broken")
	if p == nil {
		t.Fatal("project with malformed manifest must remain visible")
	}
	if p.Configured {
		t.Error("malformed manifest must not be marked configured")
	}
	if p.ManifestErr == "" {
		t.Error("must report the parse error")
	}
}

func TestScanFindsVroomTomlInHiddenDirs(t *testing.T) {
	tr := newTree(t).
		file(".hidden/.vroom.toml", "name = \"h\"\ncommand_start = \"echo\"\n").
		file("real/.vroom.toml", "name = \"real\"\ncommand_start = \"echo\"\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 2 {
		t.Errorf("expected 2 projects (including .hidden), got %d: %v", len(result.Projects), projectNames(result.Projects))
	}
}

func TestScanSortsByPath(t *testing.T) {
	tr := newTree(t).
		file("zebra/.vroom.toml", "name = \"zebra\"\ncommand_start = \"echo z\"\n").
		file("alpha/.vroom.toml", "name = \"alpha\"\ncommand_start = \"echo a\"\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(result.Projects))
	}
	if result.Projects[0].Name != "alpha" || result.Projects[1].Name != "zebra" {
		t.Errorf("unexpected order: %s, %s", result.Projects[0].Name, result.Projects[1].Name)
	}
}

func TestScanManifestWithGroups(t *testing.T) {
	tr := newTree(t).
		file("api/.vroom.toml", "name = \"api\"\nprimary_group = \"shop\"\nsecondary_group = \"backend\"\ncommand_start = \"go run .\"\nport = 8080\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 1 {
		t.Fatalf("expected 1 project, got %+v", result.Projects)
	}
	p := result.Projects[0]
	if !p.Configured || p.Manifest == nil {
		t.Error("must be configured")
	}
	if p.Manifest.PrimaryGroup != "shop" || p.Manifest.SecondaryGroup != "backend" {
		t.Errorf("groups = %q/%q, want shop/backend", p.Manifest.PrimaryGroup, p.Manifest.SecondaryGroup)
	}
	if p.Manifest.Port != 8080 {
		t.Errorf("port = %d, want 8080", p.Manifest.Port)
	}
}

func TestScanPlaygroundFixture(t *testing.T) {
	result, err := Scan(filepath.Join("..", "..", "playground"), 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Projects) != 8 {
		t.Fatalf("expected 8 projects, got %d: %v", len(result.Projects), projectNames(result.Projects))
	}
	for _, p := range result.Projects {
		if !p.Configured || p.Manifest == nil {
			t.Errorf("%s: must be configured", p.Name)
		}
	}
}

func TestScanResultIndicatesMethod(t *testing.T) {
	tr := newTree(t).
		file("proj/.vroom.toml", "name = \"proj\"\ncommand_start = \"echo\"\n")
	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if fdPath() != "" && !result.UsedFD {
		t.Error("fd available but UsedFD=false")
	}
	if fdPath() == "" && result.UsedFD {
		t.Error("fd not available but UsedFD=true")
	}
}

func projectNames(projects []Project) string {
	var names []string
	for _, p := range projects {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

// The fake git keeps the degradation and topology paths testable without a real repo.

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

func porcelainRepo(main string, worktrees ...string) string {
	var b strings.Builder
	b.WriteString("worktree " + main + "\nHEAD aaaa000000000000000000000000000000000000\nbranch refs/heads/main\n\n")
	for i, wt := range worktrees {
		b.WriteString("worktree " + wt + "\nHEAD bbbb000000000000000000000000000000000000\nbranch refs/heads/wt" + string(rune('a'+i)) + "\n\n")
	}
	return b.String()
}

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
		t.Fatalf("worktree repo-wt-a not detected: %v", projectNames(result.Projects))
	}
	if !p.IsWorktree || p.RepoRoot != main {
		t.Errorf("annotation = worktree:%v repoRoot:%q, want true %q", p.IsWorktree, p.RepoRoot, main)
	}
	if r := find(result.Projects, "repo"); r == nil || r.IsWorktree {
		t.Errorf("the main checkout must not be marked worktree: %+v", r)
	}
}

func TestScanSynthesizesUnconfiguredWorktree(t *testing.T) {
	tr := newTree(t).
		file("repo/.vroom.toml", "name = \"repo\"\ncommand_start = \"echo\"\n").
		file("repo/.git/config", "[core]\n\tbare = false\n").
		mkdir("repo-wt-no-mf")
	main := filepath.Join(tr.path(), "repo")
	wt := filepath.Join(tr.path(), "repo-wt-no-mf")
	t.Setenv("PATH", fakeGitPATH(t, porcelainRepo(main, wt), 0))

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "repo-wt-no-mf")
	if p == nil {
		t.Fatalf("worktree without manifest not synthesized: %v", projectNames(result.Projects))
	}
	if p.Configured || !p.IsWorktree || p.RepoRoot != main {
		t.Errorf("unexpected synthesized row: %+v", p)
	}
}

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
		t.Errorf("prunable worktree must not appear: %+v", p)
	}
}

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
		t.Errorf("prunable with existing directory must be annotated as worktree: %+v", p)
	}
}

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
		t.Fatalf("bare repo not detected: %v", projectNames(result.Projects))
	}
	if !b.IsBareContainer || b.Configured || b.Manifest != nil {
		t.Errorf("unexpected bare container: %+v", b)
	}
	if n := find(result.Projects, "normal"); n == nil || n.IsBareContainer {
		t.Errorf("dir with .git must not be a container: %+v", n)
	}
}

func TestFinalizeDedupsBareContainer(t *testing.T) {
	dir := t.TempDir()
	project := Project{Path: dir, Name: "bare", Configured: true}
	bare := Project{Path: dir, Name: "bare", IsBareContainer: true}
	got := finalize([]Project{project}, []Project{bare}, t.TempDir())
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d: %+v", len(got), got)
	}
	if got[0].IsBareContainer || !got[0].Configured {
		t.Errorf("the project must be kept, not the container: %+v", got[0])
	}
}

// H1: a bare repo is queried through git even without a .git, so its in-root manifestless worktrees are discovered and synthesized.
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
		t.Fatalf("bare container not detected: %+v", b)
	}
	p := find(result.Projects, "bare-wt-a")
	if p == nil {
		t.Fatalf("worktree without manifest of the bare not synthesized: %v", projectNames(result.Projects))
	}
	if !p.IsWorktree || p.RepoRoot != bare || p.Configured {
		t.Errorf("worktree of the bare badly annotated: %+v", p)
	}
}

func TestScanDegradesWhenGitUnavailable(t *testing.T) {
	tr := newTree(t).
		file("repo/.vroom.toml", "name = \"repo\"\ncommand_start = \"echo\"\n").
		file("repo/.git/config", "[core]\n\tbare = false\n")
	t.Setenv("PATH", t.TempDir()) // empty PATH means neither git nor fd is reachable

	result, err := Scan(tr.path(), 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "repo")
	if p == nil {
		t.Fatal("the project must remain visible without git")
	}
	if p.WorktreeErr == "" {
		t.Error("must record the reason for the degradation")
	}
}

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
		t.Errorf("must record the topology error: %+v", p)
	}
}

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
		t.Errorf("malformed output must record error: %+v", p)
	}
}

// RepoRoot may point outside the root; the container row for it is the TUI's job, not the scanner's.
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
		t.Fatalf("worktree outside root badly annotated: %+v", p)
	}
}

// M2: the WalkDir path finds bare repos in a single pass, with no second walk.
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
		t.Errorf("walkDir invoked %d times, want 1 (no extra walk)", calls)
	}
	if find(projects, "proj") == nil {
		t.Error("project not detected in the walk")
	}
	if find(bare, "bare") == nil {
		t.Error("bare repo not detected in the walk")
	}
}

// M2: the fd path finds bare repos without touching WalkDir at all.
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
		t.Errorf("scanWithFD must not use WalkDir, got %d invocations", calls)
	}
	if find(projects, "proj") == nil {
		t.Error("project not detected by fd")
	}
	if find(bare, "bare") == nil {
		t.Error("bare repo not detected by fd")
	}
}

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

// M3: a repo with several worktrees in one scan invokes git once, not once per worktree.
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
		t.Errorf("git invoked %d times, want 1 (one query per repo)", calls)
	}
	for _, name := range []string{"repo-wt-a", "repo-wt-b"} {
		p := find(result.Projects, name)
		if p == nil || !p.IsWorktree || p.RepoRoot != repo {
			t.Errorf("%s badly annotated: %+v", name, p)
		}
	}
}
