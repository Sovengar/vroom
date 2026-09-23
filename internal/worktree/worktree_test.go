package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// fakeGit escribe un script git falso que emite output y sale con code,
// y devuelve el PATH con ese directorio al frente (para que LookPath
// encuentre el falso y no el git real).
func fakeGit(t *testing.T, output string, code int) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'EOF'\n" + output + "EOF\nexit " + itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}

// ParsePorcelain reconoce main + worktrees, detached, bare y prunable.
func TestParsePorcelain(t *testing.T) {
	out := `worktree /repo
HEAD aaaa000000000000000000000000000000000000
branch refs/heads/main

worktree /repo-wt/a
HEAD bbbb000000000000000000000000000000000000
branch refs/heads/feature

worktree /repo-wt/det
HEAD cccc000000000000000000000000000000000000
detached

worktree /repo-wt/gone
HEAD dddd000000000000000000000000000000000000
detached
prunable gitdir file points to non-existent location
`
	wts, err := ParsePorcelain(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 4 {
		t.Fatalf("worktrees = %d, want 4: %+v", len(wts), wts)
	}
	if wts[0].Path != "/repo" || wts[0].Branch != "main" || wts[0].Detached {
		t.Errorf("main entry mal parseada: %+v", wts[0])
	}
	if wts[1].Branch != "feature" {
		t.Errorf("branch = %q, want feature", wts[1].Branch)
	}
	if !wts[2].Detached || wts[2].Branch != "" {
		t.Errorf("detached mal parseado: %+v", wts[2])
	}
	if !wts[3].Prunable {
		t.Errorf("prunable no detectado: %+v", wts[3])
	}
}

// Un bare repo se reporta con la marca bare.
func TestParsePorcelainBare(t *testing.T) {
	out := "worktree /bare\nHEAD eeee000000000000000000000000000000000000\nbare\n\n"
	wts, err := ParsePorcelain(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 1 || !wts[0].Bare {
		t.Fatalf("esperaba 1 bare, got %+v", wts)
	}
}

// Salida vacía = sin worktrees, sin error.
func TestParsePorcelainEmpty(t *testing.T) {
	wts, err := ParsePorcelain("")
	if err != nil || len(wts) != 0 {
		t.Fatalf("vacío debe dar lista vacía sin error, got %v %v", wts, err)
	}
}

// Salida no vacía sin bloque worktree = inválida.
func TestParsePorcelainMalformed(t *testing.T) {
	if _, err := ParsePorcelain("garbage without blocks\n"); err == nil {
		t.Fatal("salida malformada debe devolver error")
	}
}

// IsBareRepo: conjunción completa + marcador core.bare = true.
func TestIsBareRepo(t *testing.T) {
	bare := t.TempDir()
	writeFile(t, filepath.Join(bare, "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(bare, "config"), "[core]\n\tbare = true\n")
	if err := os.MkdirAll(filepath.Join(bare, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bare, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !IsBareRepo(bare) {
		t.Error("bare repo con marcador debe detectarse")
	}

	// Sin marcador core.bare = true no es bare (falso positivo evitado).
	noMarker := t.TempDir()
	writeFile(t, filepath.Join(noMarker, "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(noMarker, "config"), "[core]\n\tbare = false\n")
	if err := os.MkdirAll(filepath.Join(noMarker, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(noMarker, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if IsBareRepo(noMarker) {
		t.Error("sin marcador bare no debe detectarse")
	}

	// Un repo normal con .git (dir) nunca es bare.
	normal := t.TempDir()
	writeFile(t, filepath.Join(normal, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(normal, "HEAD"), "x\n")
	writeFile(t, filepath.Join(normal, "config"), "[core]\n\tbare = true\n")
	if err := os.MkdirAll(filepath.Join(normal, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(normal, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if IsBareRepo(normal) {
		t.Error("un dir con .git no debe tratarse como bare")
	}

	// Un worktree con .git file tampoco.
	wt := t.TempDir()
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: /somewhere/.git/worktrees/wt\n")
	writeFile(t, filepath.Join(wt, "HEAD"), "x\n")
	writeFile(t, filepath.Join(wt, "config"), "[core]\n\tbare = true\n")
	if err := os.MkdirAll(filepath.Join(wt, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wt, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if IsBareRepo(wt) {
		t.Error("un dir con .git file no debe tratarse como bare")
	}
}

// List sin git en PATH devuelve ErrGitUnavailable.
func TestListGitUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := List(t.TempDir()); err != ErrGitUnavailable {
		t.Fatalf("err = %v, want ErrGitUnavailable", err)
	}
}

// List con exit != 0 devuelve error.
func TestListGitFailure(t *testing.T) {
	t.Setenv("PATH", fakeGit(t, "boom\n", 1))
	if _, err := List(t.TempDir()); err == nil {
		t.Fatal("exit != 0 debe devolver error")
	}
}

// List con salida válida parsea los worktrees.
func TestListFakeGit(t *testing.T) {
	out := "worktree /repo\nHEAD aaaa000000000000000000000000000000000000\nbranch refs/heads/main\n\n"
	t.Setenv("PATH", fakeGit(t, out, 0))
	wts, err := List("/whatever")
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 1 || wts[0].Path != "/repo" || wts[0].Branch != "main" {
		t.Fatalf("worktrees inesperados: %+v", wts)
	}
}

// M4: ante un git que cuelga (dejando un descendiente con el pipe
// abierto) la llamada retorna dentro de un límite acotado, con el error de
// degradación correcto.
func TestListTimeoutIsBounded(t *testing.T) {
	oldTimeout, oldDelay := listTimeout, listWaitDelay
	listTimeout, listWaitDelay = 200*time.Millisecond, 100*time.Millisecond
	defer func() { listTimeout, listWaitDelay = oldTimeout, oldDelay }()

	dir := t.TempDir()
	// El shell lanza `sleep` y muere al cancelarse el contexto; el sleep
	// huérfano conserva el pipe abierto.
	script := "#!/bin/sh\nsleep 3\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	_, err := List(t.TempDir())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("el timeout debe devolver error de degradación")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("timeout no acotado: %v", elapsed)
	}
}
