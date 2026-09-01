package process

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func newTestManager(t *testing.T) Manager {
	t.Helper()
	return NewManager()
}

func startSleep(t *testing.T, m Manager, spec StartSpec) StartResult {
	t.Helper()
	res, err := m.Start(spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return res
}

// S7.1: el servicio daemonizado sobrevive... (aquí: queda vivo tras Start
// y es independiente; el reaper evita zombies).
func TestStartDaemonizesProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "sleep 30",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pgid: res.Pgid, Timeout: time.Second}) })

	if res.Pid <= 0 || res.Pgid != res.Pid {
		t.Fatalf("pid/pgid inesperados: %+v", res)
	}
	if res.CreationTimeMs <= 0 {
		t.Fatal("creation_time_ms no capturado")
	}
	if !Alive(res.Pid, res.CreationTimeMs) {
		t.Fatal("el proceso debería estar vivo justo tras el start")
	}
}

func TestEvaluateLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "sleep 30",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})

	// running
	if got := m.Evaluate(EvalSpec{Pid: res.Pid, CreationTimeMs: res.CreationTimeMs}); got != StatusRunning {
		t.Errorf("estado = %s, want running", got)
	}

	// S9.1: creation_time distinto → PID reciclado → stopped
	if got := m.Evaluate(EvalSpec{Pid: res.Pid, CreationTimeMs: res.CreationTimeMs + 1}); got != StatusStopped {
		t.Errorf("ctime mismatch: estado = %s, want stopped", got)
	}

	// S9.3: PID vivo, puerto y pattern configurados que fallan → unknown
	if got := m.Evaluate(EvalSpec{
		Pid:            res.Pid,
		CreationTimeMs: res.CreationTimeMs,
		Port:           freeTCPPort(t),
		ProcessPattern: "no-existe-este-proceso-xyz",
	}); got != StatusUnknown {
		t.Errorf("estado = %s, want unknown", got)
	}

	// stop → stopped
	if err := m.Stop(StopSpec{Pgid: res.Pgid, Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := m.Evaluate(EvalSpec{Pid: res.Pid, CreationTimeMs: res.CreationTimeMs}); got != StatusStopped {
		t.Errorf("tras stop: estado = %s, want stopped", got)
	}
}

// S-T3: servicio que termina inmediatamente → stopped en el siguiente ciclo.
func TestEvaluateCrashedImmediately(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "true",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	time.Sleep(300 * time.Millisecond) // dejar morir y reapear
	if got := m.Evaluate(EvalSpec{Pid: res.Pid, CreationTimeMs: res.CreationTimeMs}); got != StatusStopped {
		t.Errorf("estado = %s, want stopped (proceso crasheado)", got)
	}
}

// S8.2/R8: SIGTERM mata el grupo completo, incluyendo hijos forked.
func TestStopKillsProcessGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		// sh -c con hijos en background: todos comparten el PGID del grupo.
		Command:    "sleep 300 & sleep 300",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pgid: res.Pgid, Timeout: time.Second}) })

	time.Sleep(200 * time.Millisecond) // dejar que los hijos arranquen

	if err := m.Stop(StopSpec{Pgid: res.Pgid, Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// El grupo entero debe haber desaparecido (kill(-pgid, 0) → ESRCH).
	if err := syscall.Kill(-res.Pgid, syscall.Signal(0)); err != syscall.ESRCH {
		t.Errorf("grupo %d sigue vivo tras Stop: %v", res.Pgid, err)
	}
}

// S8.3: stop de un grupo ya muerto no es error ni envía señales raras.
func TestStopAlreadyDead(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	m := newTestManager(t)
	if err := m.Stop(StopSpec{Pgid: 999999999, Timeout: time.Second}); err != nil {
		t.Errorf("stop de servicio ya detenido no debe fallar: %v", err)
	}
	if err := m.Stop(StopSpec{Pgid: 0, Timeout: time.Second}); err != nil {
		t.Errorf("stop con pgid 0 no debe fallar: %v", err)
	}
}

// S9.2: PortOpen contra un listener real.
func TestPortOpen(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if !PortOpen(port) {
		t.Errorf("puerto %d abierto debería reportarse como abierto", port)
	}
	if PortOpen(freeTCPPort(t)) {
		t.Error("puerto libre debería reportarse como cerrado")
	}
}

func TestStartLogsCaptured(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "echo hola-stdout; echo hola-stderr 1>&2",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pgid: res.Pgid, Timeout: time.Second}) })

	deadline := time.Now().Add(3 * time.Second)
	var out, errb []byte
	for time.Now().Before(deadline) {
		out, _ = os.ReadFile(filepath.Join(dir, "stdout.log"))
		errb, _ = os.ReadFile(filepath.Join(dir, "stderr.log"))
		if len(out) > 0 && len(errb) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if string(out) != "hola-stdout\n" {
		t.Errorf("stdout.log = %q", out)
	}
	if string(errb) != "hola-stderr\n" {
		t.Errorf("stderr.log = %q", errb)
	}
}

// freeTCPPort encuentra un puerto libre (best-effort, sin garantías).
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
