//go:build unix

package process

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Skips when the environment has no procfs, which would make every /proc assertion vacuous.
func requireProc(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/proc/self/task"); err != nil {
		t.Skip("no /proc en este entorno")
	}
}

func pidProcInfo(t *testing.T, pid int) (procInfo, bool) {
	t.Helper()
	info, err := procStatAt(filepath.Join(procRoot, strconv.Itoa(pid)))
	if err != nil || !info.running() {
		return procInfo{}, false
	}
	return info, true
}

// The test process itself owns the listener, which is what makes it a foreign process from Stop's point of view.
func listenPortAndHold(t *testing.T) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return port, func() { _ = ln.Close() }
}

func freePortForHelper(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout esperando: %s", desc)
}

func waitPortOpen(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, "puerto abierto", func() bool { return PortOpen(port) })
}

// Not a test: this binary is re-launched as a re-sid descendant, selected by VROOM_TEST_HELPER (listener, two-ports or churn) and VROOM_TEST_PORT.
func TestHelperListener(t *testing.T) {
	mode := os.Getenv("VROOM_TEST_HELPER")
	if mode != "listener" && mode != "two-ports" && mode != "churn" {
		t.Skip("proceso helper, no un test")
	}
	port := os.Getenv("VROOM_TEST_PORT")
	if mode == "two-ports" {
		twoPortsListener(port)
		return
	}
	if mode == "churn" {
		churnListener()
		return
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(3)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	time.Sleep(120 * time.Second)
}

// Opens a metrics listener before the main one, reproducing an app that exposes metrics first and its service port later.
func twoPortsListener(mainPort string) {
	metrics, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("VROOM_TEST_PORT2"))
	if err != nil {
		os.Exit(3)
	}
	go acceptLoop(metrics)
	time.Sleep(200 * time.Millisecond)

	ln, err := net.Listen("tcp", "127.0.0.1:"+mainPort)
	if err != nil {
		os.Exit(3)
	}
	defer func() { _ = ln.Close() }()
	go acceptLoop(ln)
	time.Sleep(120 * time.Second)
}

// The 80 ms cadence sits below discoverSettle (500 ms) on purpose: any slower and the set would hold still long enough for discovery to decide it.
func churnListener() {
	var previo net.Listener
	defer func() {
		if previo != nil {
			_ = previo.Close()
		}
	}()
	for {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			os.Exit(3)
		}
		go acceptLoop(ln)
		if previo != nil {
			_ = previo.Close()
		}
		previo = ln
		time.Sleep(80 * time.Millisecond)
	}
}

func acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_ = c.Close()
	}
}

func TestStopKillsResidDescendantAndFreesPort(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	port := freePortForHelper(t)
	t.Setenv("VROOM_TEST_HELPER", "listener")
	t.Setenv("VROOM_TEST_PORT", strconv.Itoa(port))

	m := newTestManager(t)
	dir := t.TempDir()
	// The child re-sids its own backend, so it lands in a different process group than the recorded PGID.
	res := startSleep(t, m, StartSpec{
		Command:    "setsid " + testBinary(t) + " -test.run=^TestHelperListener$ & sleep 300",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	t.Cleanup(func() {
		_ = syscall.Kill(-res.Pgid, syscall.SIGKILL)
		_ = m.Stop(StopSpec{Pgid: res.Pgid, Timeout: time.Second})
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
		t.Fatalf("precondición del test: el backend debe estar en otro pgid (tiene %d)", backend.pgid)
	}
	if backend.ppid != res.Pid {
		t.Fatalf("precondición del test: el backend debe ser descendiente directo (ppid=%d, root=%d)", backend.ppid, res.Pid)
	}

	lineageBefore := append([]int{res.Pid}, descendantsAt(procRoot, res.Pid)...)
	if len(lineageBefore) < 2 {
		t.Fatalf("precondición: se esperaba al menos un descendiente, linaje=%v", lineageBefore)
	}
	if !containsPid(lineageBefore, int(backendPID)) {
		t.Fatalf("precondición: el backend %d debe estar en el linaje %v", backendPID, lineageBefore)
	}

	var warns []string
	if err := m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 3 * time.Second, Warn: func(f string, a ...any) {
		warns = append(warns, fmt.Sprintf(f, a...))
	}}); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if _, alive := pidProcInfo(t, res.Pid); alive {
		t.Errorf("el proceso principal %d sigue vivo tras Stop", res.Pid)
	}
	if _, alive := pidProcInfo(t, int(backendPID)); alive {
		t.Errorf("el backend re-sid %d sigue vivo tras Stop: kill(-pgid) no alcanza al linaje", backendPID)
	}
	for _, pid := range lineageBefore {
		if _, alive := pidProcInfo(t, pid); alive {
			t.Errorf("el pid %d del linaje sobrevive al stop", pid)
		}
	}
	waitFor(t, 3*time.Second, "puerto liberado", func() bool { return !PortOpen(port) })
	if len(warns) != 0 {
		t.Errorf("stop limpio no debe emitir avisos: %v", warns)
	}
}

func TestStopSingleProcessGroupNoRegression(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	port := freePortForHelper(t)
	t.Setenv("VROOM_TEST_HELPER", "listener")
	t.Setenv("VROOM_TEST_PORT", strconv.Itoa(port))

	m := newTestManager(t)
	dir := t.TempDir()
	// No setsid: the helper shares the service PGID.
	res := startSleep(t, m, StartSpec{
		Command:    testBinary(t) + " -test.run=^TestHelperListener$ & sleep 300",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	t.Cleanup(func() {
		_ = syscall.Kill(-res.Pgid, syscall.SIGKILL)
		_ = m.Stop(StopSpec{Pgid: res.Pgid, Timeout: time.Second})
	})

	waitPortOpen(t, port, 5*time.Second)
	listenerPID := PortOwnerPID(port)
	if listenerPID <= 0 {
		t.Fatal("no se pudo determinar el PID del listener")
	}
	info, alive := pidProcInfo(t, int(listenerPID))
	if !alive || info.pgid != res.Pgid {
		t.Fatalf("precondición: el listener debe compartir el pgid %d (info=%+v alive=%v)", res.Pgid, info, alive)
	}

	if err := m.Stop(StopSpec{Pgid: res.Pgid, Timeout: 3 * time.Second}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := syscall.Kill(-res.Pgid, syscall.Signal(0)); err != syscall.ESRCH {
		t.Errorf("el process group %d sigue vivo tras Stop: %v", res.Pgid, err)
	}
	waitFor(t, 3*time.Second, "puerto liberado", func() bool { return !PortOpen(port) })
}

// An already-stopped service (PGID 0) whose recorded port is held by a twin in another worktree: Stop must not run fuser -k.
func TestStopDoesNotKillForeignPortHolder(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: proceso real en el puerto")
	}
	port, release := listenPortAndHold(t)
	defer release()

	meta := StopSpec{Pgid: 0, Port: port, Timeout: time.Second}
	var warns []string
	meta.Warn = func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) }

	m := newTestManager(t)
	if err := m.Stop(meta); err != nil {
		t.Fatalf("Stop de servicio ya parado no debe fallar: %v", err)
	}

	if !PortOpen(port) {
		t.Error("vroom cerró el puerto de un proceso ajeno")
	}
	if _, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 500*time.Millisecond); err != nil {
		t.Errorf("el puerto ajeno debe seguir aceptando conexiones: %v", err)
	}
	if len(warns) == 0 {
		t.Error("Stop debe avisar de que no pudo liberar el puerto")
	}
	if !strings.Contains(warns[0], strconv.Itoa(port)) {
		t.Errorf("el aviso debe nombrar el puerto: %q", warns[0])
	}
}

func TestStopUnknownPortOwnerFailsClosed(t *testing.T) {
	port, release := listenPortAndHold(t)
	defer release()

	var warns []string
	warn := func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) }

	// An injected resolver that knows nothing about the port.
	killPortHolderWith(port, os.Getpid(), nil, func(int) []int32 { return nil }, warn)

	if !PortOpen(port) {
		t.Error("con propietario desconocido no se debe tocar el puerto")
	}
	if len(warns) != 1 {
		t.Fatalf("se esperaba exactamente 1 aviso, got %d: %v", len(warns), warns)
	}
	if !strings.Contains(warns[0], strconv.Itoa(port)) {
		t.Errorf("el aviso debe nombrar el puerto: %q", warns[0])
	}
}

func TestStopAmbiguousPortOwnersFailsClosed(t *testing.T) {
	port, release := listenPortAndHold(t)
	defer release()

	var warns []string
	warn := func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) }

	owners := func(int) []int32 { return []int32{int32(os.Getpid()), 1} }
	killPortHolderWith(port, os.Getpid(), nil, owners, warn)

	if !PortOpen(port) {
		t.Error("con propietarios ambiguos no se debe tocar el puerto")
	}
	if len(warns) != 1 {
		t.Fatalf("se esperaba 1 aviso, got %d: %v", len(warns), warns)
	}
}

// The owner is a real child listener: killing the test process would be worse than the bug under test.
func TestStopKillsOwnedPortHolder(t *testing.T) {
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
		Command:    testBinary(t) + " -test.run=^TestHelperListener$ & sleep 300",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})
	t.Cleanup(func() {
		_ = syscall.Kill(-res.Pgid, syscall.SIGKILL)
		_ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second})
	})

	waitPortOpen(t, port, 5*time.Second)
	owner := PortOwnerPID(port)
	if owner <= 0 {
		t.Fatal("no se pudo determinar el PID del listener")
	}
	lineage := append([]int{res.Pid}, descendantsAt(procRoot, res.Pid)...)
	if !containsPid(lineage, int(owner)) {
		t.Fatalf("precondición: el owner %d debe estar en el linaje %v", owner, lineage)
	}

	var warns []string
	killPortHolderWith(port, res.Pid, lineage, func(int) []int32 { return []int32{owner} },
		func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) })

	if len(warns) != 0 {
		t.Errorf("dueño probado como propio no debe emitir avisos: %v", warns)
	}
	if PortOpen(port) {
		t.Errorf("el puerto %d debía liberarse: el dueño estaba en el linaje", port)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "sleep 300",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "stdout.log"),
		StderrPath: filepath.Join(dir, "stderr.log"),
	})

	if err := m.Stop(StopSpec{Pgid: res.Pgid, Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("primer Stop: %v", err)
	}
	// Second stop: the PGID is already gone and there is no port to free.
	var warns []string
	if err := m.Stop(StopSpec{Pgid: res.Pgid, Timeout: time.Second, Warn: func(f string, a ...any) {
		warns = append(warns, fmt.Sprintf(f, a...))
	}}); err != nil {
		t.Fatalf("Stop debe ser idempotente, no fallar: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("stop idempotente no debe emitir avisos: %v", warns)
	}
}

func writeProcFixture(t *testing.T, tree map[int]procInfo) string {
	t.Helper()
	root := t.TempDir()
	for pid, info := range tree {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// pid (comm) state ppid pgrp session ...
		stat := strconv.Itoa(pid) + " (proc" + strconv.Itoa(pid) + ") S " +
			strconv.Itoa(info.ppid) + " " + strconv.Itoa(info.pgid) + " " +
			strconv.Itoa(info.pgid) + " 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// PID 1 always exists in a real /proc and some readers assume it.
	if _, ok := tree[1]; !ok {
		dir := filepath.Join(root, "1")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		stat := "1 (init) S 0 1 1 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDescendantsAtFollowsResidChild(t *testing.T) {
	root := writeProcFixture(t, map[int]procInfo{
		100: {ppid: 1, pgid: 100},   // service root
		101: {ppid: 100, pgid: 100}, // child in the same group
		102: {ppid: 101, pgid: 102}, // grandchild that called setsid
		103: {ppid: 102, pgid: 102}, // great-grandchild of the re-sid
		200: {ppid: 1, pgid: 200},   // twin in another worktree
		201: {ppid: 200, pgid: 200},
	})
	got := descendantsAt(root, 100)
	want := []int{101, 102, 103}
	if len(got) != len(want) {
		t.Fatalf("descendants = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("descendants = %v, want %v (orden ascendente)", got, want)
		}
	}
}

func TestProcSnapshotDetectsResidPgid(t *testing.T) {
	root := writeProcFixture(t, map[int]procInfo{
		100: {ppid: 1, pgid: 100},
		102: {ppid: 100, pgid: 102},
	})
	snap, err := procSnapshotAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if snap[100].pgid != 100 || snap[102].pgid != 102 {
		t.Errorf("pgid mal leído: %+v", snap)
	}
	if snap[102].ppid != 100 {
		t.Errorf("ppid mal leído: %+v", snap[102])
	}
}

func TestProcStatAtCommWithParens(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "55")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := "55 (Web Content (tab)) S 7 55 55 0 -1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := procStatAt(filepath.Join(root, "55"))
	if err != nil {
		t.Fatal(err)
	}
	if info.ppid != 7 || info.pgid != 55 {
		t.Errorf("procInfo = %+v, want ppid=7 pgid=55", info)
	}
}

func TestProcSnapshotAtMissingRoot(t *testing.T) {
	if _, err := procSnapshotAt(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("una raíz /proc inexistente debe devolver error")
	}
}

func testBinary(t *testing.T) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return shellQuote(bin)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
