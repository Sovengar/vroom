package process

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v3/process"
)

// The other tests all passed the reserved port, so they exited by R1 without waiting and never reached the discoverSettle countdown.
func TestDiscoverPortEsperaAQueElConjuntoDeListenersSeEstabilice(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	requireProc(t)

	port := freePortForHelper(t)
	t.Setenv("VROOM_TEST_HELPER", "listener")
	t.Setenv("VROOM_TEST_PORT", strconv.Itoa(port))

	m := newTestManager(t)
	dir := t.TempDir()
	// reserved = 0 on purpose: without the R1 shortcut the only path is to settle.
	res := startSleep(t, m, StartSpec{
		Command:    "setsid " + testBinary(t) + " -test.run=^TestHelperListener$ & sleep 120",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "out.log"),
		StderrPath: filepath.Join(dir, "err.log"),
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second}) })

	waitPortOpen(t, port, 5*time.Second)

	inicio := time.Now()
	d := DiscoverPort(res.Pid, 0, "/health", 5*time.Second)
	esperado := time.Since(inicio)

	if d.Port != port {
		t.Errorf("DiscoverPort = %+v, want port %d of the only listener", d, port)
	}
	if !d.Verified {
		t.Error("Verified is false with a single stabilized listener: the UI cannot claim that " +
			"the route works and the service appears without a URL")
	}
	// It must have WAITED: accepting the first sample is exactly the bug the settle window prevents.
	if esperado < discoverSettle {
		t.Errorf("decided in %s, want at least %s: accepted the first sample without waiting for the "+
			"set to stop growing, which is exactly the bug the window prevents",
			esperado, discoverSettle)
	}
	if d.Unresolved {
		t.Error("Unresolved with a single listener that doesn't change: there is nothing that cannot be decided")
	}
}

// MEASURED: Unresolved yields port_unresolved while an empty result yields no_port, which asserts something never checked - that the service exposes no TCP port at all.
func TestDiscoverPortNoDecideUnConjuntoQueNoSeEstabiliza(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	requireProc(t)

	t.Setenv("VROOM_TEST_HELPER", "churn")

	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "setsid " + testBinary(t) + " -test.run=^TestHelperListener$ & sleep 120",
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "out.log"),
		StderrPath: filepath.Join(dir, "err.log"),
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second}) })

	// Short deadline on purpose: reaching it with listeners present is enough.
	d := DiscoverPort(res.Pid, 0, "/health", 900*time.Millisecond)

	if !d.Unresolved {
		t.Errorf("DiscoverPort = %+v, want Unresolved: there are listeners and the deadline expired without the "+
			"set stabilizing, and that must be reported instead of inventing a port", d)
	}
	if d.Port != 0 {
		t.Errorf("Port = %d with Unresolved, want 0: an undecided port is worse than none, "+
			"because the UI publishes it", d.Port)
	}
	if d.Verified {
		t.Error("Verified is true without deciding: it would claim the route works without having verified it")
	}
	if d.LineageDead {
		t.Error("LineageDead with the helper running: without deciding the port, the process is still there")
	}
}

func TestDescendantsDeUnProcRootInexistenteNoDevuelveNada(t *testing.T) {
	if got := descendantsAt("/proc/definitely-not-here", os.Getpid()); len(got) != 0 {
		t.Errorf("descendantsAt with a nonexistent /proc returned %v, want nothing", got)
	}
	if got := lineageListenersAt("/proc/definitely-not-here", os.Getpid()); got != nil {
		t.Errorf("lineageListenersAt with a nonexistent /proc returned %v, want nil", got)
	}
}

func TestDescendantsFromTerminaAnteUnCicloEnElArbol(t *testing.T) {
	// 1 -> 2 -> 3 -> 1: the root reappears as a child of its own descendant.
	snap := map[int]procInfo{
		1: {pid: 1, ppid: 3},
		2: {pid: 2, ppid: 1},
		3: {pid: 3, ppid: 3}, // parent of itself: skipped when building `children`
		4: {pid: 4, ppid: 2},
	}

	// Without the guard this never returns and hangs the test binary, which is the clearest proof that the hang is real.
	got := descendantsFrom(snap, 1)

	if len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Errorf("descendantsFrom = %v, want [2 4]: it must traverse the cycle once and "+
			"stop, without including the root or repeating", got)
	}
	for _, pid := range got {
		if pid == 1 {
			t.Error("the root appears as its own descendant: Stop by lineage would kill itself")
		}
	}
}

// The kernel keeps a dying process's ppid, pid 1 has ppid 0, and a zombie is not guaranteed to keep its parent, so children[p] can contain p.
func TestDescendantsFromIgnoraUnProcesoQueEsSuPropioPadre(t *testing.T) {
	snap := map[int]procInfo{
		1: {pid: 1, ppid: 0},
		2: {pid: 2, ppid: 1},
		3: {pid: 3, ppid: 3}, // its own parent
	}

	got := descendantsFrom(snap, 1)
	for _, pid := range got {
		if pid == 3 {
			t.Errorf("descendantsFrom = %v includes 3, which is its own parent: it would hang by itself", got)
		}
	}
	if len(got) != 1 || got[0] != 2 {
		t.Errorf("descendantsFrom = %v, want [2]", got)
	}
}

// A real stat can hold one good field and one garbage one, but never a tick count with a field skipped, because a silent 0 makes a saturated thread look idle.
func TestParseThreadStatConUnStimeNoNumericoEsUnStatInvalido(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stat")

	// 13 fields after the ')': utime in rest[11] is good, stime in rest[12] is garbage.
	escribirStat(t, path, "R 0 0 0 0 0 0 0 0 0 0 1234 not-a-number")
	if _, _, err := parseThreadStat(path); err == nil {
		t.Fatal("a non-numeric stime must be an invalid stat, not ticks 1234")
	}

	// The good case too, so this tests parsing and not only rejection.
	escribirStat(t, path, "R 0 0 0 0 0 0 0 0 0 0 100 200")
	state, ticks, err := parseThreadStat(path)
	if err != nil {
		t.Fatalf("a valid stat returned error: %v", err)
	}
	if state != "R" {
		t.Errorf("state = %q, want R", state)
	}
	if ticks != 300 {
		t.Errorf("ticks = %d, want 300 (utime 100 + stime 200)", ticks)
	}
}

func escribirStat(t *testing.T, path, rest string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("42 (a name with (parentheses) "+rest+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A service restarted outside vroom leaves a meta with a dead pid but an open port, so answering running would vouch for whoever opened it.

func TestEvaluateConUnPuertoDePropietarioAmbiguoDegradaAIndeterminado(t *testing.T) {
	port := freePortForHelper(t)

	v4, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v4.Close() }()

	v6, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port))
	if err != nil {
		t.Skipf("this machine cannot listen on ::1 with the same port number: %v", err)
	}
	defer func() { _ = v6.Close() }()

	// MEASURED: both entries belong to the SAME process and there are still two, so ambiguity needs no second process - one process on both address families is enough.
	if pids := PortOwnerPIDs(port); len(pids) != 2 {
		t.Skipf("this machine does not give two owners for the same port number (got %v): "+
			"the test condition is not met", pids)
	}

	got := NewManager().Evaluate(EvalSpec{
		Pid:  0, // the meta PID is dead: this is the external fallback
		Port: port,
	})
	if got != StatusUnknown {
		t.Errorf("Evaluate = %v, want unknown with an ambiguous owner: claiming running would "+
			"report as healthy a service for which there is no proof, which is exactly the twin of another worktree", got)
	}
}

// The listener is opened in the test process so the creation time compared is the test binary's, read from the same source production reads.
func TestEvaluateConElPuertoDeUnProcesoVivoLoDaPorRunningConPruebaDePropiedad(t *testing.T) {
	port := freePortForHelper(t)
	ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	self, err := gopsprocess.NewProcess(int32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := self.CreateTime()
	if err != nil {
		t.Fatal(err)
	}

	got := NewManager().Evaluate(EvalSpec{
		Pid:            0, // the meta has no PID: decided by port
		Port:           port,
		CreationTimeMs: ct,
	})
	if got != StatusRunning {
		t.Errorf("Evaluate = %v, want running: there is a single live owner and its creation time matches the meta's", got)
	}

	otro := NewManager().Evaluate(EvalSpec{
		Pid:            0,
		Port:           port,
		CreationTimeMs: ct + 1,
	})
	if otro != StatusStopped {
		t.Errorf("Evaluate = %v with a foreign creation time, want stopped: the port is held by someone "+
			"who is not the meta's service", otro)
	}
}

// The in-memory set and the system are two sources of truth, and a foreign holder means take the next port rather than fail: the range belongs to the kernel, not to vroom.
func TestReservePortSaltaLosPuertosQueElSistemaYaTieneOcupados(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real socket")
	}

	// Occupies the FIRST port of the range, exactly where the loop starts, so the first iteration must fail and continue.
	bloqueo, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", DynamicPortLow))
	if err != nil {
		t.Skipf("port %d in the range is occupied by something else: %v", DynamicPortLow, err)
	}
	defer func() { _ = bloqueo.Close() }()

	p, err := ReservePort()
	if err != nil {
		t.Fatalf("ReservePort: %v", err)
	}
	t.Cleanup(func() { ReleasePort(p) })

	if p == DynamicPortLow {
		t.Errorf("ReservePort returned %d, which is occupied: the pool must skip to the next "+
			"in the range, not return a port that no one can open", DynamicPortLow)
	}
	if p < DynamicPortLow || p > DynamicPortHigh {
		t.Errorf("ReservePort = %d, outside the range [%d, %d]", p, DynamicPortLow, DynamicPortHigh)
	}
}
