//go:build unix

package process

import (
	"os"
	"path/filepath"
	"testing"
)

// readMetricsAt lee RSS, FDs y ticks desde una raíz /proc simulada.
func TestReadMetricsAt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "1234")
	for _, sub := range []string{"fd", "task"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// stat: campos tras ')' son state + 10 valores + utime(100) + stime(50).
	stat := "1234 (proc) S 1 1 1 0 -1 0 0 0 0 0 100 50 0 0 0 0 0 0 0\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	status := "Name:\tproc\nVmRSS:\t   2048 kB\n"
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fd", "0"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := readMetricsAt(root, 1234)
	if err != nil {
		t.Fatal(err)
	}
	if m.Ticks != 150 {
		t.Errorf("Ticks = %d, want 150", m.Ticks)
	}
	if m.RSSKB != 2048 {
		t.Errorf("RSSKB = %d, want 2048", m.RSSKB)
	}
	if m.FDs != 1 {
		t.Errorf("FDs = %d, want 1", m.FDs)
	}
}

// Un PID inexistente devuelve error (la UI muestra placeholder).
func TestReadMetricsMissingPID(t *testing.T) {
	if _, err := readMetricsAt(t.TempDir(), 999999); err == nil {
		t.Error("se esperaba error con un PID inexistente")
	}
}

// readEnvironAt parte el contenido por NUL y descarta entradas vacías.
func TestReadEnvironAt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "42")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), []byte("A=1\x00B=2\x00\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	vars, err := readEnvironAt(root, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 2 || vars[0] != "A=1" || vars[1] != "B=2" {
		t.Errorf("environ = %v", vars)
	}
}
