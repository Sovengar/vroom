package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

// The candidate list is data, not branching logic: order decides, so the system path must precede the packaged one.
func TestFdInPathsDevuelveLaPrimeraQueExiste(t *testing.T) {
	dir := t.TempDir()
	segunda := filepath.Join(dir, "second")
	if err := os.WriteFile(segunda, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	primera := filepath.Join(dir, "first")
	if err := os.WriteFile(primera, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := fdInPaths([]string{primera, segunda}); got != primera {
		t.Errorf("fdInPaths = %q with both present, want %q: the order decides which one wins, and the "+
			"system one goes first", got, primera)
	}
	if got := fdInPaths([]string{filepath.Join(dir, "absent"), segunda}); got != segunda {
		t.Errorf("fdInPaths = %q with the first absent, want %q", got, segunda)
	}
}

// The list is a parameter, not a global, so the no-candidate branch stays reachable on a machine that has fd installed.
func TestFdInPathsDevuelveVacioSinNingunaRutaUsable(t *testing.T) {
	dir := t.TempDir()

	if got := fdInPaths(nil); got != "" {
		t.Errorf("fdInPaths(nil) = %q, want \"\"", got)
	}
	if got := fdInPaths([]string{}); got != "" {
		t.Errorf("fdInPaths([]) = %q, want \"\"", got)
	}
	if got := fdInPaths([]string{filepath.Join(dir, "no-existe")}); got != "" {
		t.Errorf("fdInPaths with a nonexistent path = %q, want \"\": without a candidate there is no fd", got)
	}

	// A directory named fd is not a candidate: exec fails with EISDIR, and a mere err == nil check made Scan surface that instead of falling back to the walk.
	sub := filepath.Join(dir, "fd-directorio")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := fdInPaths([]string{sub}); got != "" {
		t.Errorf("fdInPaths = %q with a directory, want \"\": it cannot be executed", got)
	}
}

// Reporting UsedFD true while falling back would mean claiming a tool that cannot be exec'd.
func TestScanCaeAlWalkSinFdEnElPath(t *testing.T) {
	// MEASURED: LookPath always fails on an empty PATH, so the fallback branch is reached without touching /usr.
	t.Setenv("PATH", "")

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", ".vroom.toml"),
		[]byte("name = \"app\"\ncommands.start.run = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Scan(root, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Projects) != 1 {
		t.Fatalf("the walk found %d projects, want 1", len(res.Projects))
	}
	// With fd absent there is no baseline to diff against, so only the well-formed project is checked; fd/walk parity lives in paths_test.go.
	p := res.Projects[0]
	if !p.Configured || p.Manifest == nil || p.Manifest.Name != "app" {
		t.Errorf("project = %+v, want Configured with Manifest.Name=app: the fallback to walk has "+
			"to return exactly the same as the fd path", p)
	}
}
