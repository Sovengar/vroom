//go:build unix

package process

import (
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// StopSpec.Pid está documentado como "raíz del linaje", pero como objetivo de
// muerte era decorativo: todo el bloque de kill estaba bajo `if spec.Pgid > 0`,
// así que con un PID válido y PGID 0 Stop no hacía nada. Eso es lo que dejó
// helpers vivos reteniendo puertos del rango de reserva.

// pidGone dice si el proceso ya no está corriendo (un zombie cuenta como
// muerto: no ejecuta ni puede retener un puerto).
func pidGone(t *testing.T, pid int) bool {
	t.Helper()
	info, err := procStatAt(filepath.Join(procRoot, strconv.Itoa(pid)))
	if err != nil || !info.running() {
		return true
	}
	return false
}

func syscallKill(pid int) error    { return syscall.Kill(pid, syscall.SIGKILL) }
func syscallKillGroup(g int) error { return syscall.Kill(-g, syscall.SIGKILL) }

// assertDead falla si el proceso sigue vivo tras Stop.
func assertDead(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pidGone(t, pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("%s %d sigue vivo tras Stop", what, pid)
}

// startBareSpawned arranca un proceso real fuera de todo grupo de vroom, para
// que Stop sólo pueda alcanzarlo por PID.
func startBareSpawned(t *testing.T) StartResult {
	t.Helper()
	dir := t.TempDir()
	res := startSleep(t, newTestManager(t), StartSpec{
		Command:    "sleep 300",
		WorkDir:    dir,
		StdoutPath: dir + "/out.log",
		StderrPath: dir + "/err.log",
	})
	t.Cleanup(func() {
		_ = syscallKillGroup(res.Pgid)
		_ = syscallKill(res.Pid)
	})
	return res
}

// Con PID y sin PGID, Stop tiene que matar. Antes: no-op completo.
func TestStopWithPidAndNoPgidTerminates(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	res := startBareSpawned(t)
	if pidGone(t, res.Pid) {
		t.Fatalf("precondición: el proceso debería estar vivo, got %d", res.Pid)
	}

	// El spec exacto que usan los helpers de startsvc.
	if err := newTestManager(t).Stop(StopSpec{
		Pid: res.Pid, Pgid: 0, Port: 0, Timeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	assertDead(t, res.Pid, "el proceso raíz")
}

// La vía del PID también tiene que alcanzar a un descendiente re-sid, que
// vive en otro process group y no comparte señales con la raíz.
func TestStopWithPidOnlyKillsResidDescendant(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	port := freePortForHelper(t)
	t.Setenv("VROOM_TEST_HELPER", "listener")
	t.Setenv("VROOM_TEST_PORT", strconv.Itoa(port))

	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "setsid " + testBinary(t) + " -test.run=^TestHelperListener$ & sleep 300",
		WorkDir:    dir,
		StdoutPath: dir + "/out.log",
		StderrPath: dir + "/err.log",
	})
	t.Cleanup(func() {
		_ = syscallKillGroup(res.Pgid)
		_ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second})
	})

	waitPortOpen(t, port, 5*time.Second)
	backendPID := PortOwnerPID(port)
	if backendPID <= 0 {
		t.Fatal("no se pudo determinar el PID del backend re-sid")
	}
	backend, alive := pidProcInfo(t, int(backendPID))
	if !alive {
		t.Fatalf("el backend %d no está en /proc", backendPID)
	}
	if backend.pgid == res.Pgid {
		t.Fatalf("precondición: el backend debe estar en otro pgid (tiene %d)", backend.pgid)
	}

	// Sólo el PID de la raíz, sin PGID.
	if err := m.Stop(StopSpec{Pid: res.Pid, Timeout: 3 * time.Second}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	assertDead(t, res.Pid, "la raíz")
	assertDead(t, int(backendPID), "el descendiente re-sid")
	waitFor(t, 3*time.Second, "puerto liberado", func() bool { return !PortOpen(port) })
}

// Un PID inexistente no puede hacer paniquear ni colgarse.
func TestStopWithNonexistentPidIsClean(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: sin spawn, pero con /proc")
	}
	requireProc(t)

	done := make(chan error, 1)
	go func() {
		done <- newTestManager(t).Stop(StopSpec{
			Pid: 999999999, Timeout: time.Second,
			Warn: func(string, ...any) { t.Error("un PID inexistente no debe emitir avisos") },
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop con PID inexistente no debe fallar: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop con un PID inexistente se ha colgado")
	}
}

// Sin PID ni PGID no hay raíz creíble: no-op, y no se señala nada.
func TestStopWithoutAnyRootIsNoOp(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	// Un proceso ajeno que debe seguir intacto.
	res := startBareSpawned(t)

	var warns []string
	if err := newTestManager(t).Stop(StopSpec{
		Pid: 0, Pgid: 0, Port: 0, Timeout: time.Second,
		Warn: func(f string, a ...any) { warns = append(warns, f) },
	}); err != nil {
		t.Fatalf("Stop sin raíz no debe fallar: %v", err)
	}

	if len(warns) != 0 {
		t.Errorf("un stop sin raíz no debe emitir avisos: %v", warns)
	}
	if pidGone(t, res.Pid) {
		t.Fatal("un stop sin PID ni PGID no debe matar nada, y menos a un proceso ajeno")
	}

	_ = newTestManager(t).Stop(StopSpec{Pid: res.Pid, Timeout: time.Second})
}
