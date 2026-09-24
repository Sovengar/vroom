package gitinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Rama normal.
func TestBranchNormal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	if got := Branch(root); got != "main" {
		t.Errorf("got %q, want main", got)
	}
}

// Rama con slashes se muestra completa.
func TestBranchWithSlashes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/feature/dashboard\n")
	if got := Branch(root); got != "feature/dashboard" {
		t.Errorf("got %q", got)
	}
}

// Detached HEAD → sha corto.
func TestBranchDetached(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "e83c5163316f89bfbde7d9ab23ca2e25604af290\n")
	if got := Branch(root); got != "e83c516 (detached)" {
		t.Errorf("got %q", got)
	}
}

// Worktree con .git fichero (gitdir absoluto y relativo).
func TestBranchWorktree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git"), "gitdir: /tmp/elsewhere/.git/worktrees/w\n")
	// gitdir absoluto: HEAD bajo /tmp/elsewhere (creado como fixture)
	writeFile(t, filepath.Join("/tmp", "elsewhere", ".git", "worktrees", "w", "HEAD"), "ref: refs/heads/wt-branch\n")
	if got := Branch(root); got != "wt-branch" {
		t.Errorf("worktree absoluto: got %q", got)
	}

	root2 := t.TempDir()
	writeFile(t, filepath.Join(root2, ".git"), "gitdir: ../dotfiles/.git/worktrees/x\n")
	writeFile(t, filepath.Join(filepath.Dir(root2), "dotfiles", ".git", "worktrees", "x", "HEAD"), "ref: refs/heads/rel\n")
	if got := Branch(root2); got != "rel" {
		t.Errorf("worktree relativo: got %q", got)
	}
}

// Sin repo no hay fila ni error.
func TestBranchNoRepo(t *testing.T) {
	if got := Branch(t.TempDir()); got != "" {
		t.Errorf("got %q, want vacío", got)
	}
}

// HEAD malformado → vacío sin crashear.
func TestBranchMalformed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "garbage\n")
	if got := Branch(root); got != "" {
		t.Errorf("got %q", got)
	}
	root2 := t.TempDir()
	writeFile(t, filepath.Join(root2, ".git"), "not-a-gitdir-line\n")
	if got := Branch(root2); got != "" {
		t.Errorf("got %q", got)
	}
}
