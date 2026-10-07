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

func TestStartDaemonizesProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
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
		t.Fatalf("unexpected pid/pgid: %+v", res)
	}
	if res.CreationTimeMs <= 0 {
		t.Fatal("creation_time_ms not captured")
	}
	if !Alive(res.Pid, res.CreationTimeMs) {
		t.Fatal("the process should be alive right after start")
	}
}

func TestEvaluateLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "sleep 30",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})

	if got := m.Evaluate(EvalSpec{Pid: res.Pid, CreationTimeMs: res.CreationTimeMs}); got != StatusRunning {
		t.Errorf("status = %s, want running", got)
	}

	// A different creation_time means a recycled pid.
	if got := m.Evaluate(EvalSpec{Pid: res.Pid, CreationTimeMs: res.CreationTimeMs + 1}); got != StatusStopped {
		t.Errorf("ctime mismatch: status = %s, want stopped", got)
	}

	if got := m.Evaluate(EvalSpec{
		Pid:            res.Pid,
		CreationTimeMs: res.CreationTimeMs,
		Port:           freeTCPPort(t),
		ProcessPattern: "does-not-exist-this-process-xyz",
	}); got != StatusUnknown {
		t.Errorf("status = %s, want unknown", got)
	}

	if err := m.Stop(StopSpec{Pgid: res.Pgid, Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := m.Evaluate(EvalSpec{Pid: res.Pid, CreationTimeMs: res.CreationTimeMs}); got != StatusStopped {
		t.Errorf("after stop: status = %s, want stopped", got)
	}
}

// S-T3: a service that exits immediately is stopped on the next cycle.
func TestEvaluateCrashedImmediately(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "true",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	time.Sleep(300 * time.Millisecond) // let it die and be reaped
	if got := m.Evaluate(EvalSpec{Pid: res.Pid, CreationTimeMs: res.CreationTimeMs}); got != StatusStopped {
		t.Errorf("status = %s, want stopped (crashed process)", got)
	}
}

func TestEvaluateDeadPIDPortOpenByOther(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	m := newTestManager(t)

	// A listener standing in for a foreign process.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	if got := m.Evaluate(EvalSpec{Pid: 999999999, CreationTimeMs: 0, Port: port}); got != StatusStopped {
		t.Errorf("dead PID + foreign port: status = %s, want stopped", got)
	}
}

func TestEvaluateDeadPIDPortOpenSameService(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	m := newTestManager(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	ownerPID := PortOwnerPID(port)
	if ownerPID == 0 {
		t.Fatal("could not get the PID of the listener")
	}
	p, err := gopsprocess.NewProcess(int32(ownerPID))
	if err != nil {
		t.Fatalf("gopsutil: %v", err)
	}
	ct, err := p.CreateTime()
	if err != nil {
		t.Fatalf("gopsutil CreateTime: %v", err)
	}

	if got := m.Evaluate(EvalSpec{Pid: int(ownerPID), CreationTimeMs: ct, Port: port}); got != StatusRunning {
		t.Errorf("live PID + own port: status = %s, want running", got)
	}
}

func TestEvaluateDeadPIDPatternMatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
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

	if got := m.Evaluate(EvalSpec{Pid: 999999999, CreationTimeMs: 0, ProcessPattern: "sleep"}); got != StatusRunning {
		t.Errorf("dead PID + pattern match: status = %s, want running", got)
	}
}

func TestEvaluateDeadPIDNothingMatches(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	m := newTestManager(t)
	if got := m.Evaluate(EvalSpec{Pid: 999999999, CreationTimeMs: 0, Port: freeTCPPort(t), ProcessPattern: "does-not-exist-xyz"}); got != StatusStopped {
		t.Errorf("dead PID + nothing matches: status = %s, want stopped", got)
	}
}

func TestEvaluateDeadPIDNoChecks(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	m := newTestManager(t)
	if got := m.Evaluate(EvalSpec{Pid: 999999999, CreationTimeMs: 0}); got != StatusStopped {
		t.Errorf("dead PID + no checks: status = %s, want stopped", got)
	}
}

func TestStopKillsProcessGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		// sh -c with a background child: both share the group PGID.
		Command:    "sleep 300 & sleep 300",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pgid: res.Pgid, Timeout: time.Second}) })

	time.Sleep(200 * time.Millisecond) // let the children start

	if err := m.Stop(StopSpec{Pgid: res.Pgid, Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := syscall.Kill(-res.Pgid, syscall.Signal(0)); err != syscall.ESRCH {
		t.Errorf("group %d is still alive after Stop: %v", res.Pgid, err)
	}
}

func TestStopAlreadyDead(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	m := newTestManager(t)
	if err := m.Stop(StopSpec{Pgid: 999999999, Timeout: time.Second}); err != nil {
		t.Errorf("stop of an already stopped service must not fail: %v", err)
	}
	if err := m.Stop(StopSpec{Pgid: 0, Timeout: time.Second}); err != nil {
		t.Errorf("stop with pgid 0 must not fail: %v", err)
	}
}

func TestPortOpen(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	if !PortOpen(port) {
		t.Errorf("open port %d should be reported as open", port)
	}
	if PortOpen(freeTCPPort(t)) {
		t.Error("free port should be reported as closed")
	}
}

func TestPortOwnerPID(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	pid := PortOwnerPID(port)
	if pid <= 0 {
		t.Errorf("PortOwnerPID(%d) = %d, want > 0", port, pid)
	}
	if int(pid) != os.Getpid() {
		t.Errorf("PortOwnerPID(%d) = %d, want %d (self)", port, pid, os.Getpid())
	}

	freePort := freeTCPPort(t)
	if pid := PortOwnerPID(freePort); pid != 0 {
		t.Errorf("PortOwnerPID(%d) = %d, want 0 (free port)", freePort, pid)
	}
}

func TestStartLogsCaptured(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
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

// Best-effort: the listener closes before the caller binds, so a collision is possible.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}
