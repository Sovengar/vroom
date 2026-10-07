package gitinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadHEADEnUnRepoNormal(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")

	head, ok := readHEAD(dir)
	if !ok {
		t.Fatal("readHEAD = false in a normal repo with HEAD")
	}
	if head != "ref: refs/heads/main" {
		t.Errorf("head = %q, want HEAD without the trailing newline", head)
	}
}

// Without resolving the relative pointer the worktree branch would be empty and the portless route would fall back to the project name, the collision auto mode exists to prevent.
func TestReadHEADEnUnWorktreeConGitirRelativo(t *testing.T) {
	dir := t.TempDir()
	main := t.TempDir()
	write(t, filepath.Join(main, "HEAD"), "ref: refs/heads/feature/login\n")
	wt := filepath.Join(main, "worktrees", "feature")
	write(t, filepath.Join(wt, "HEAD"), "HEAD of the worktree\n")
	write(t, filepath.Join(dir, ".git"), "gitdir: "+relativeTo(dir, wt)+"\n")

	head, ok := readHEAD(dir)
	if !ok {
		t.Fatal("readHEAD = false in a worktree with a relative pointer")
	}
	if head != "HEAD of the worktree" {
		t.Errorf("head = %q: the wrong HEAD was read", head)
	}
}

func TestReadHEADEnUnWorktreeConGitirAbsoluto(t *testing.T) {
	dir := t.TempDir()
	wt := t.TempDir()
	write(t, filepath.Join(wt, "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(dir, ".git"), "gitdir: "+wt+"\n")

	head, ok := readHEAD(dir)
	if !ok {
		t.Fatal("readHEAD = false with an absolute pointer")
	}
	if head != "ref: refs/heads/main" {
		t.Errorf("head = %q", head)
	}
}

// An invented branch is worse than none, because route_mode auto concatenates the branch onto the project name to build the route name.
func TestReadHEADRechazaLoQueNoEsUnPunteroAGit(t *testing.T) {
	t.Run("no .git", func(t *testing.T) {
		if _, ok := readHEAD(t.TempDir()); ok {
			t.Error("a directory without .git is not a repo")
		}
	})

	t.Run(".git without the gitdir: prefix", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, ".git"), "/other/path\n")
		if _, ok := readHEAD(dir); ok {
			t.Error("a .git without 'gitdir:' is not a worktree pointer")
		}
	})

	t.Run("pointer to a gitdir without HEAD", func(t *testing.T) {
		dir := t.TempDir()
		// The gitdir exists but has no HEAD, which is a half-created worktree.
		empty := t.TempDir()
		write(t, filepath.Join(dir, ".git"), "gitdir: "+empty+"\n")
		if _, ok := readHEAD(dir); ok {
			t.Error("a gitdir without HEAD has no branch: a half-created worktree cannot invent one")
		}
	})

	t.Run(".git as a directory without HEAD", func(t *testing.T) {
		// git init creates the directory before writing HEAD, so claiming a branch until then would be inventing one.
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, ok := readHEAD(dir); ok {
			t.Error("a freshly created .git without HEAD has no branch")
		}
	})
}

// The detached form matters because git checkout <sha> leaves the repo there, and the TUI must show that as different from being on a branch.
func TestParseHEADInterpretaLasTresFormasYRechazaElResto(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"branch", "ref: refs/heads/main", "main"},
		{"branch with slash", "ref: refs/heads/feature/login", "feature/login"},
		{"branch with surrounding spaces", "ref:   refs/heads/main  ", "main"},
		{"ref outside heads", "ref: refs/tags/v1", "refs/tags/v1"},
		{"full detached sha", "abc1234def5678901234567890123456789012ab", "abc1234 (detached)"},
		{"short detached sha", "abc1234", "abc1234 (detached)"},
		{"garbage", "this is not a HEAD", ""},
		{"empty", "", ""},
		{"hex too short", "abc", ""},
		{"not hex", "zzzzzzz", ""},
		{"hex with uppercase", "ABC1234", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseHEAD(tt.in); got != tt.want {
				t.Errorf("parseHEAD(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// The TUI calls Branch for every scanned row, including non-repos and paths that just disappeared from the tree.
func TestBranchConEntradaRaraNoRevienta(t *testing.T) {
	if got := Branch(filepath.Join(t.TempDir(), "nothing")); got != "" {
		t.Errorf("Branch of a nonexistent path = %q, want empty string", got)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Branch(dir); got != "" {
		t.Errorf("Branch of a .git without HEAD = %q, want empty string", got)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func relativeTo(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}

// readHEAD's contract is a bool, so an unreadable .git and a missing .git are the same case; accepting it would invent the branch.
func TestReadHEADConUnGitFileIlegible(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a file without permission: the case cannot be triggered")
	}
	dir := t.TempDir()
	gitFile := filepath.Join(dir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /some/path\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, ok := readHEAD(dir); ok {
		t.Error("an unreadable .git cannot yield a branch: it would be invented")
	}
}
