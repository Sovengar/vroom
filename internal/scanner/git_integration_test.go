package scanner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit salta el test si el binario git no está disponible.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no disponible")
	}
}

// runGit ejecuta git con identidad inline y falla el test si falla.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	base := []string{"-c", "user.email=test@test", "-c", "user.name=test", "-c", "protocol.file.allow=always"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestScanRealGitWorktrees cubre con git real la anotación repo/worktree
// (main in-root, worktree linkeado y worktree detached).
func TestScanRealGitWorktrees(t *testing.T) {
	requireGit(t)
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

	wtDet := filepath.Join(root, "repo-wt-det")
	runGit(t, repo, "worktree", "add", "-q", "--detach", wtDet)

	result, err := Scan(root, 4)
	if err != nil {
		t.Fatal(err)
	}
	main := find(result.Projects, "repo")
	if main == nil || main.IsWorktree {
		t.Fatalf("main checkout mal anotado: %+v", main)
	}
	a := find(result.Projects, "repo-wt-a")
	if a == nil || !a.IsWorktree || a.RepoRoot != repo {
		t.Fatalf("worktree linkeado mal anotado: %+v", a)
	}
	det := find(result.Projects, "repo-wt-det")
	if det == nil || !det.IsWorktree || det.RepoRoot != repo {
		t.Fatalf("worktree detached mal anotado: %+v", det)
	}
}

// TestScanRealGitSubmoduleNotWorktree verifica que un submodule no se
// anida como worktree de su repo padre.
func TestScanRealGitSubmoduleNotWorktree(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(root, "sub")
	for _, d := range []string{repo, sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "init", "-q", "-b", "main", sub)
	runGit(t, sub, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, root, "init", "-q", "-b", "main", repo)
	if err := os.WriteFile(filepath.Join(repo, ".vroom.toml"), []byte("name = \"repo\"\ncommand_start = \"echo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "init")
	runGit(t, repo, "submodule", "add", "-q", sub, "sub")
	if err := os.WriteFile(filepath.Join(repo, "sub", ".vroom.toml"), []byte("name = \"sub\"\ncommand_start = \"echo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Scan(root, 4)
	if err != nil {
		t.Fatal(err)
	}
	p := find(result.Projects, "sub")
	if p == nil {
		t.Fatalf("submodule no escaneado: %v", projectNames(result.Projects))
	}
	if p.IsWorktree || p.RepoRoot != "" {
		t.Errorf("un submodule no debe tratarse como worktree: %+v", p)
	}
	if strings.Contains(p.RepoRoot, "modules") {
		t.Errorf("repo_root no debe apuntar al gitdir del submodule: %q", p.RepoRoot)
	}
}
