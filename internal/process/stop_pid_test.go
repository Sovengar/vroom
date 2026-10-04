//go:build unix

package process

import (
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// Two regressions pinned here: the kill block sat under "if spec.Pgid > 0" so a valid Pid with Pgid 0 made Stop a no-op, and an earlier test that called Stop(StopSpec{Pid: os.Getpid()}) killed the test binary mid-suite.

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

func TestStopWithPidAndNoPgidTerminates(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	res := startBareSpawned(t)
	if pidGone(t, res.Pid) {
		t.Fatalf("precondición: el proceso debería estar vivo, got %d", res.Pid)
	}

	// The exact StopSpec the startsvc helpers use.
	if err := newTestManager(t).Stop(StopSpec{
		Pid: res.Pid, Pgid: 0, Port: 0, Timeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	assertDead(t, res.Pid, "el proceso raíz")
}

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

	if err := m.Stop(StopSpec{Pid: res.Pid, Timeout: 3 * time.Second}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	assertDead(t, res.Pid, "la raíz")
	assertDead(t, int(backendPID), "el descendiente re-sid")
	waitFor(t, 3*time.Second, "puerto liberado", func() bool { return !PortOpen(port) })
}

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

func TestStopWithoutAnyRootIsNoOp(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

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
