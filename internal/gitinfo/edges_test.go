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
		t.Fatal("readHEAD = false en un repo normal con HEAD")
	}
	if head != "ref: refs/heads/main" {
		t.Errorf("head = %q, want el HEAD sin el salto final", head)
	}
}

// Without resolving the relative pointer the worktree branch would be empty and the portless route would fall back to the project name, the collision auto mode exists to prevent.
func TestReadHEADEnUnWorktreeConGitirRelativo(t *testing.T) {
	dir := t.TempDir()
	main := t.TempDir()
	write(t, filepath.Join(main, "HEAD"), "ref: refs/heads/feature/login\n")
	wt := filepath.Join(main, "worktrees", "feature")
	write(t, filepath.Join(wt, "HEAD"), "HEAD del worktree\n")
	write(t, filepath.Join(dir, ".git"), "gitdir: "+relativeTo(dir, wt)+"\n")

	head, ok := readHEAD(dir)
	if !ok {
		t.Fatal("readHEAD = false en un worktree con puntero relativo")
	}
	if head != "HEAD del worktree" {
		t.Errorf("head = %q: se leyó el HEAD equivocado", head)
	}
}

func TestReadHEADEnUnWorktreeConGitirAbsoluto(t *testing.T) {
	dir := t.TempDir()
	wt := t.TempDir()
	write(t, filepath.Join(wt, "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(dir, ".git"), "gitdir: "+wt+"\n")

	head, ok := readHEAD(dir)
	if !ok {
		t.Fatal("readHEAD = false con un puntero absoluto")
	}
	if head != "ref: refs/heads/main" {
		t.Errorf("head = %q", head)
	}
}

// An invented branch is worse than none, because route_mode auto concatenates the branch onto the project name to build the route name.
func TestReadHEADRechazaLoQueNoEsUnPunteroAGit(t *testing.T) {
	t.Run("sin .git", func(t *testing.T) {
		if _, ok := readHEAD(t.TempDir()); ok {
			t.Error("un directorio sin .git no es un repo")
		}
	})

	t.Run(".git sin el prefijo gitdir:", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, ".git"), "/otra/ruta\n")
		if _, ok := readHEAD(dir); ok {
			t.Error("un .git sin 'gitdir:' no es un puntero de worktree")
		}
	})

	t.Run("puntero a un gitdir sin HEAD", func(t *testing.T) {
		dir := t.TempDir()
		// The gitdir exists but has no HEAD, which is a half-created worktree.
		empty := t.TempDir()
		write(t, filepath.Join(dir, ".git"), "gitdir: "+empty+"\n")
		if _, ok := readHEAD(dir); ok {
			t.Error("un gitdir sin HEAD no tiene rama: un worktree a medio crear no puede inventarla")
		}
	})

	t.Run(".git como directorio sin HEAD", func(t *testing.T) {
		// git init creates the directory before writing HEAD, so claiming a branch until then would be inventing one.
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, ok := readHEAD(dir); ok {
			t.Error("un .git recién creado sin HEAD no tiene rama")
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
		{"rama", "ref: refs/heads/main", "main"},
		{"rama con barra", "ref: refs/heads/feature/login", "feature/login"},
		{"rama con espacios alrededor", "ref:   refs/heads/main  ", "main"},
		{"ref fuera de heads", "ref: refs/tags/v1", "refs/tags/v1"},
		{"sha completo detached", "abc1234def5678901234567890123456789012ab", "abc1234 (detached)"},
		{"sha corto detached", "abc1234", "abc1234 (detached)"},
		{"basura", "esto no es un HEAD", ""},
		{"vacío", "", ""},
		{"hex demasiado corto", "abc", ""},
		{"no hex", "zzzzzzz", ""},
		{"hex con mayusculas", "ABC1234", ""},
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
	if got := Branch(filepath.Join(t.TempDir(), "nada")); got != "" {
		t.Errorf("Branch de un path inexistente = %q, want cadena vacía", got)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Branch(dir); got != "" {
		t.Errorf("Branch de un .git sin HEAD = %q, want cadena vacía", got)
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
		t.Skip("root puede leer un fichero sin permiso: el caso no se puede provocar")
	}
	dir := t.TempDir()
	gitFile := filepath.Join(dir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: /alguna/ruta\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, ok := readHEAD(dir); ok {
		t.Error("un .git ilegible no puede dar una rama: sería inventada")
	}
}
