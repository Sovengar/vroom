//go:build unix

package process

import (
	"os"
	"path/filepath"
	"testing"
)

// buildProcFixture crea un /proc sintético con un proceso 123 de dos hilos.
func buildProcFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// state ppid pgrp session tty tpgid flags minflt cminflt majflt cmajflt utime stime
	statMain := "123 (main) S 1 123 123 0 -1 4194560 100 0 0 0 1200 800 "
	statThr := "124 (http-nio-8084-exec-1) S 123 123 123 0 -1 4194560 20 0 0 0 50 50 "
	for _, tid := range []string{"123", "124"} {
		stat := statMain
		if tid == "124" {
			stat = statThr
		}
		// relleno hasta el campo 52 como el kernel real
		write("123/task/"+tid+"/stat", stat+"0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n")
		write("123/task/"+tid+"/comm", map[string]string{"123": "main", "124": "http-nio-8084-exec-1"}[tid]+"\n")
	}
	return root
}

// Tabla completa con nombre/TID/estado/ticks.
func TestListThreadsBasic(t *testing.T) {
	root := buildProcFixture(t)
	threads, err := listThreadsAt(root, 123)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 {
		t.Fatalf("esperaba 2 hilos, got %d", len(threads))
	}
	// Orden determinista por TID.
	if threads[0].TID != 123 || threads[1].TID != 124 {
		t.Errorf("orden por TID: %+v", threads)
	}
	main := threads[0]
	if main.Name != "main" || main.State != "S" || main.Ticks != 2000 {
		t.Errorf("hilo main: %+v", main)
	}
	thr := threads[1]
	if thr.Name != "http-nio-8084-exec-1" || thr.State != "S" || thr.Ticks != 100 {
		t.Errorf("hilo 124: %+v", thr)
	}
}

// comm con espacios y paréntesis no rompe el parseo de stat.
func TestListThreadsCommWithSpaces(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "9", "task", "9")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := "9 (My Thread (worker)) R 1 9 9 0 -1 0 0 0 0 0 7 3 "
	for i := 0; i < 37; i++ {
		stat += "0 "
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	threads, err := listThreadsAt(root, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].Name != "My Thread (worker)" || threads[0].State != "R" || threads[0].Ticks != 10 {
		t.Errorf("threads = %+v, err = %v", threads, err)
	}
}

// Proceso muerto entre ticks → error controlado.
func TestListThreadsMissingProcess(t *testing.T) {
	if _, err := listThreadsAt(t.TempDir(), 999999); err == nil {
		t.Error("proceso inexistente debe devolver error")
	}
}

// Integración contra /proc real: el propio proceso de test debe listar
// al menos un hilo con nombre y estado parseables.
func TestListThreadsRealProc(t *testing.T) {
	if _, err := os.Stat("/proc/self/task"); err != nil {
		t.Skip("no /proc en este entorno")
	}
	threads, err := ListThreads(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) < 1 {
		t.Fatal("el proceso de test debe tener al menos un hilo")
	}
	for _, th := range threads {
		if th.TID <= 0 || th.Name == "" || th.State == "" {
			t.Errorf("hilo mal parseado: %+v", th)
		}
	}
}

// Delta de ticks → CPU%. 100 ticks en 2s con USER_HZ=100 = 50%.
func TestCPUPercent(t *testing.T) {
	if got := CPUPercent(100, 2); got != 50 {
		t.Errorf("CPUPercent(100, 2) = %v, want 50", got)
	}
	if got := CPUPercent(0, 2); got != 0 {
		t.Errorf("sin ticks = %v, want 0", got)
	}
	if got := CPUPercent(100, 0); got != 0 {
		t.Errorf("elapsed 0 no debe dividir por cero, got %v", got)
	}
	// Sobrecarga >100% es válida (varios cores).
	if got := CPUPercent(400, 1); got != 400 {
		t.Errorf("multinúcleo: got %v, want 400", got)
	}
}
