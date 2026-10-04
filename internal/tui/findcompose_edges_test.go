package tui

import (
	"os"
	"path/filepath"
	"testing"

	"vroom/internal/orchestrate"
	"vroom/internal/scanner"
)

// MEDIDO: a project outside root never satisfies dir == root, so the walk truly reaches "/" and only the parent == dir guard (with seen) ends it.
func TestFindComposeFileTerminaEnLaRaizDelSistema(t *testing.T) {
	// Fixture: a real project dir holding a compose, plus a root that is not its ancestor, so the walk has to climb past root.
	proyecto := t.TempDir()
	if err := os.WriteFile(filepath.Join(proyecto, orchestrate.ComposeFileName),
		[]byte("primary_group = \"lejos\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(t.TempDir(), "raiz-que-no-es-antepasado")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	cf, err := findComposeFile(root, []scanner.Project{{Path: proyecto, Configured: true}})
	if err != nil {
		t.Fatalf("findComposeFile: %v", err)
	}
	if cf == nil {
		t.Fatal("no encontró el compose que está en el propio directorio del proyecto")
	}

	// Without the guard this test never returns, so the go test timeout is the detector, which is why it uses a real dir and no mock.
	vacio := t.TempDir()
	// Stray compose files above the temp dir are removed first, so the miss is the guard's doing and not a leftover on disk.
	for d := filepath.Dir(vacio); d != "/" && d != "."; d = filepath.Dir(d) {
		_ = os.Remove(filepath.Join(d, orchestrate.ComposeFileName))
	}

	_, err = findComposeFile(root, []scanner.Project{{Path: vacio, Configured: true}})
	if err == nil {
		t.Error("findComposeFile encontró un compose subiendo hasta la raíz del sistema")
	}
}
