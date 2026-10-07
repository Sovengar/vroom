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
	t.Errorf("%s %d is still alive after Stop", what, pid)
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
		t.Skip("integration: real spawn")
	}
	requireProc(t)

	res := startBareSpawned(t)
	if pidGone(t, res.Pid) {
		t.Fatalf("precondition: the process should be alive, got %d", res.Pid)
	}

	// The exact StopSpec the startsvc helpers use.
	if err := newTestManager(t).Stop(StopSpec{
		Pid: res.Pid, Pgid: 0, Port: 0, Timeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	assertDead(t, res.Pid, "the root process")
}

func TestStopWithPidOnlyKillsResidDescendant(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
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
		t.Fatal("could not determine the PID of the re-sid backend")
	}
	backend, alive := pidProcInfo(t, int(backendPID))
	if !alive {
		t.Fatalf("backend %d is not in /proc", backendPID)
	}
	if backend.pgid == res.Pgid {
		t.Fatalf("precondition: the backend must be in another pgid (has %d)", backend.pgid)
	}

	if err := m.Stop(StopSpec{Pid: res.Pid, Timeout: 3 * time.Second}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	assertDead(t, res.Pid, "the root")
	assertDead(t, int(backendPID), "the re-sid descendant")
	waitFor(t, 3*time.Second, "port freed", func() bool { return !PortOpen(port) })
}

func TestStopWithNonexistentPidIsClean(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: no spawn, but with /proc")
	}
	requireProc(t)

	done := make(chan error, 1)
	go func() {
		done <- newTestManager(t).Stop(StopSpec{
			Pid: 999999999, Timeout: time.Second,
			Warn: func(string, ...any) { t.Error("a nonexistent PID must not emit warnings") },
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop with a nonexistent PID must not fail: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop with a nonexistent PID has hung")
	}
}

func TestStopWithoutAnyRootIsNoOp(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	requireProc(t)

	res := startBareSpawned(t)

	var warns []string
	if err := newTestManager(t).Stop(StopSpec{
		Pid: 0, Pgid: 0, Port: 0, Timeout: time.Second,
		Warn: func(f string, a ...any) { warns = append(warns, f) },
	}); err != nil {
		t.Fatalf("Stop without a root must not fail: %v", err)
	}

	if len(warns) != 0 {
		t.Errorf("a stop without a root must not emit warnings: %v", warns)
	}
	if pidGone(t, res.Pid) {
		t.Fatal("a stop without PID or PGID must not kill anything, least of all a foreign process")
	}

	_ = newTestManager(t).Stop(StopSpec{Pid: res.Pid, Timeout: time.Second})
}
