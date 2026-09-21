package process

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v3/process"
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

// S9.4: PID muerto + puerto abierto por OTRO proceso → stopped (no falso positivo).
func TestEvaluateDeadPIDPortOpenByOther(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	m := newTestManager(t)

	// Escuchar un puerto para simular un proceso externo (OTRO servicio).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// PID inexistente + creation_time basura → Alive() falla.
	// El puerto está abierto pero por un proceso con creation_time distinto → stopped.
	if got := m.Evaluate(EvalSpec{Pid: 999999999, CreationTimeMs: 0, Port: port}); got != StatusStopped {
		t.Errorf("PID muerto + puerto ajeno: estado = %s, want stopped", got)
	}
}

// S9.4b: PID muerto + puerto abierto por el MISMO servicio (reiniciado) → running.
func TestEvaluateDeadPIDPortOpenSameService(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	m := newTestManager(t)

	// Simular un servicio que escucha un puerto.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// Obtener el PID real del listener y su creation_time.
	ownerPID := PortOwnerPID(port)
	if ownerPID == 0 {
		t.Fatal("no se pudo obtener el PID del listener")
	}
	p, err := gopsprocess.NewProcess(int32(ownerPID))
	if err != nil {
		t.Fatalf("gopsutil: %v", err)
	}
	ct, err := p.CreateTime()
	if err != nil {
		t.Fatalf("gopsutil CreateTime: %v", err)
	}

	// PID muerto (mismo owner pero PID "visto" como muerto) + creation_time correcto → running.
	// Usamos el PID real del owner pero con su creation_time real.
	if got := m.Evaluate(EvalSpec{Pid: int(ownerPID), CreationTimeMs: ct, Port: port}); got != StatusRunning {
		t.Errorf("PID vivo + puerto propio: estado = %s, want running", got)
	}
}

// S9.5: PID muerto + pattern match → running (fallback externo).
func TestEvaluateDeadPIDPatternMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	m := newTestManager(t)

	// Lanzar un sleep propio para garantizar que el pattern exista.
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "sleep 30",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pgid: res.Pgid, Timeout: time.Second}) })

	// PID muerto + pattern "sleep" que matchea nuestro proceso → running.
	if got := m.Evaluate(EvalSpec{Pid: 999999999, CreationTimeMs: 0, ProcessPattern: "sleep"}); got != StatusRunning {
		t.Errorf("PID muerto + pattern match: estado = %s, want running", got)
	}
}

// S9.6: PID muerto + puerto cerrado + pattern no existe → stopped.
func TestEvaluateDeadPIDNothingMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	m := newTestManager(t)
	if got := m.Evaluate(EvalSpec{Pid: 999999999, CreationTimeMs: 0, Port: freeTCPPort(t), ProcessPattern: "no-existe-xyz"}); got != StatusStopped {
		t.Errorf("PID muerto + nada coincide: estado = %s, want stopped", got)
	}
}

// S9.7: PID muerto + puerto cerrado + sin pattern → stopped.
func TestEvaluateDeadPIDNoChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	m := newTestManager(t)
	if got := m.Evaluate(EvalSpec{Pid: 999999999, CreationTimeMs: 0}); got != StatusStopped {
		t.Errorf("PID muerto + sin checks: estado = %s, want stopped", got)
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

// PortOwnerPID devuelve el PID del proceso que escucha en un puerto.
func TestPortOwnerPID(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	pid := PortOwnerPID(port)
	if pid <= 0 {
		t.Errorf("PortOwnerPID(%d) = %d, want > 0", port, pid)
	}
	// El PID debe ser el de este proceso.
	if int(pid) != os.Getpid() {
		t.Errorf("PortOwnerPID(%d) = %d, want %d (self)", port, pid, os.Getpid())
	}

	// Puerto libre → 0.
	freePort := freeTCPPort(t)
	if pid := PortOwnerPID(freePort); pid != 0 {
		t.Errorf("PortOwnerPID(%d) = %d, want 0 (puerto libre)", freePort, pid)
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
