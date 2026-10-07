package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func fakeGit(t *testing.T, output string, code int) string {
	t.Helper()
	return fakeGitStreams(t, output, "", code)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}

func fakeGitStreams(t *testing.T, stdout, stderr string, code int) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat <<'EOF'\n" + stdout + "EOF\ncat >&2 <<'EOF'\n" + stderr + "EOF\nexit " + itoa(code) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

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
		t.Errorf("main entry badly parsed: %+v", wts[0])
	}
	if wts[1].Branch != "feature" {
		t.Errorf("branch = %q, want feature", wts[1].Branch)
	}
	if !wts[2].Detached || wts[2].Branch != "" {
		t.Errorf("detached badly parsed: %+v", wts[2])
	}
	if !wts[3].Prunable {
		t.Errorf("prunable not detected: %+v", wts[3])
	}
}

func TestParsePorcelainBare(t *testing.T) {
	out := "worktree /bare\nHEAD eeee000000000000000000000000000000000000\nbare\n\n"
	wts, err := ParsePorcelain(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 1 || !wts[0].Bare {
		t.Fatalf("expected 1 bare, got %+v", wts)
	}
}

func TestParsePorcelainEmpty(t *testing.T) {
	wts, err := ParsePorcelain("")
	if err != nil || len(wts) != 0 {
		t.Fatalf("empty must yield an empty list without error, got %v %v", wts, err)
	}
}

func TestParsePorcelainMalformed(t *testing.T) {
	if _, err := ParsePorcelain("garbage without blocks\n"); err == nil {
		t.Fatal("malformed output must return an error")
	}
}

func TestParsePorcelainIgnoresEmptyWorktreePath(t *testing.T) {
	out := "worktree /repo\nHEAD aaaa000000000000000000000000000000000000\nbranch refs/heads/main\n\nworktree \nHEAD bbbb000000000000000000000000000000000000\n\n"
	wts, err := ParsePorcelain(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 1 {
		t.Fatalf("worktrees = %d, want 1 (block without path ignored): %+v", len(wts), wts)
	}
	for _, wt := range wts {
		if wt.Path == "" {
			t.Errorf("there must be no entries with empty Path: %+v", wts)
		}
	}
}

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
		t.Error("bare repo with marker must be detected")
	}

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
		t.Error("without bare marker it must not be detected")
	}

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
		t.Error("a dir with .git must not be treated as bare")
	}

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
		t.Error("a dir with a .git file must not be treated as bare")
	}
}

func TestIsBareRepoScopesMarkerToCore(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(dir, "config"), "[remote \"origin\"]\n\tbare = true\n")
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if IsBareRepo(dir) {
		t.Error("bare outside [core] must not be detected as a bare repo")
	}

	writeFile(t, filepath.Join(dir, "config"), "[core]\n\tbare = true # bare repo\n")
	if !IsBareRepo(dir) {
		t.Error("bare = true inside [core] must be detected")
	}
}

func TestListGitUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := List(t.TempDir()); err != ErrGitUnavailable {
		t.Fatalf("err = %v, want ErrGitUnavailable", err)
	}
}

func TestListGitFailure(t *testing.T) {
	t.Setenv("PATH", fakeGit(t, "boom\n", 1))
	if _, err := List(t.TempDir()); err == nil {
		t.Fatal("exit != 0 must return an error")
	}
}

func TestListFakeGit(t *testing.T) {
	out := "worktree /repo\nHEAD aaaa000000000000000000000000000000000000\nbranch refs/heads/main\n\n"
	t.Setenv("PATH", fakeGit(t, out, 0))
	wts, err := List("/whatever")
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 1 || wts[0].Path != "/repo" || wts[0].Branch != "main" {
		t.Fatalf("unexpected worktrees: %+v", wts)
	}
}

func TestListSeparatesStdoutFromStderr(t *testing.T) {
	out := "worktree /repo\nHEAD aaaa000000000000000000000000000000000000\nbranch refs/heads/main\n\n"
	t.Setenv("PATH", fakeGitStreams(t, out, "warning: something on stderr\n", 0))
	wts, err := List("/whatever")
	if err != nil {
		t.Fatalf("stderr must not break parsing: %v", err)
	}
	if len(wts) != 1 || wts[0].Path != "/repo" {
		t.Fatalf("unexpected worktrees: %+v", wts)
	}
}

func TestListFailureIncludesStderr(t *testing.T) {
	t.Setenv("PATH", fakeGitStreams(t, "", "fatal: not a git repository\n", 1))
	_, err := List("/whatever")
	if err == nil {
		t.Fatal("exit != 0 must return an error")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("the error must include stderr: %v", err)
	}
}

// M4: a hanging git must return within a bounded time with context.DeadlineExceeded instead of blocking the scan.
func TestListTimeoutIsBounded(t *testing.T) {
	oldTimeout, oldDelay := listTimeout, listWaitDelay
	listTimeout, listWaitDelay = 200*time.Millisecond, 100*time.Millisecond
	defer func() { listTimeout, listWaitDelay = oldTimeout, oldDelay }()

	dir := t.TempDir()
	// The shell spawns sleep and dies when the context is cancelled, so the orphan sleep keeps the pipe open.
	script := "#!/bin/sh\nsleep 3\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	_, err := List(t.TempDir())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("the timeout must return a degradation error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("unbounded timeout: %v", elapsed)
	}
}
