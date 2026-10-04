//go:build unix

package process

import (
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReservePortInRange(t *testing.T) {
	for i := 0; i < 5; i++ {
		port, err := ReservePort()
		if err != nil {
			t.Fatalf("ReservePort: %v", err)
		}
		if port < DynamicPortLow || port > DynamicPortHigh {
			t.Fatalf("puerto %d fuera del rango %d-%d", port, DynamicPortLow, DynamicPortHigh)
		}
	}
}

func TestMergeEnvKeepsParent(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "HOME=/home/u", "PORT=8080"}

	merged := mergeEnv(parent, []string{"PORT=41501", "HOST=127.0.0.1"})

	want := map[string]string{
		"PATH": "/usr/bin", // the parent survives
		"HOME": "/home/u",
		"PORT": "41501", // the spec overrides
		"HOST": "127.0.0.1",
	}
	got := map[string]string{}
	for _, kv := range merged {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (merged=%v)", k, got[k], v, merged)
		}
	}
	if len(merged) != len(want) {
		t.Errorf("merged = %v, want %d entradas sin duplicados", merged, len(want))
	}
}

func TestMergeEnvNilWhenNothingToInject(t *testing.T) {
	if got := mergeEnv([]string{"PATH=/usr/bin"}, nil); got != nil {
		t.Errorf("sin spec.Env debe quedar nil (herencia), got %v", got)
	}
}

func TestLineageListenersExcludesForeignSockets(t *testing.T) {
	requireProc(t)

	// The test process owns this listener.
	port := freePortForHelper(t)
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	res := startSleep(t, newTestManager(t), StartSpec{
		Command:    "sleep 60",
		WorkDir:    t.TempDir(),
		StdoutPath: t.TempDir() + "/out.log",
		StderrPath: t.TempDir() + "/err.log",
	})
	t.Cleanup(func() { _ = newTestManager(t).Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second}) })

	if got := lineageListenersAt(procRoot, res.Pid); len(got) != 0 {
		t.Errorf("un sleep no tiene listeners, got %v", got)
	}
	if got := lineageListenersAt(procRoot, os.Getpid()); !containsPid(got, port) {
		t.Errorf("el listener %d del proceso de test debe aparecer en su linaje, got %v", port, got)
	}
}

func TestDiscoverPortFindsListenerInLineage(t *testing.T) {
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
		Command:    testBinary(t) + " -test.run=^TestHelperListener$ & sleep 60",
		WorkDir:    dir,
		StdoutPath: dir + "/out.log",
		StderrPath: dir + "/err.log",
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second}) })

	waitPortOpen(t, port, 5*time.Second)

	d := DiscoverPort(res.Pid, port, "/health", 3*time.Second)
	if d.Port != port {
		t.Errorf("DiscoverPort = %+v, want el puerto %d del listener", d, port)
	}
	if !d.HonoredReserved {
		t.Error("un listener igual al reservado es R1: determinista y sin heurística")
	}
}

func TestDiscoverPortFailsFastOnDeadLineage(t *testing.T) {
	requireProc(t)

	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "true", // dies immediately
		WorkDir:    dir,
		StdoutPath: dir + "/out.log",
		StderrPath: dir + "/err.log",
	})

	start := time.Now()
	d := DiscoverPort(res.Pid, 0, "/health", 30*time.Second)
	elapsed := time.Since(start)

	if !d.LineageDead {
		t.Errorf("un linaje muerto debe abortar el discovery: %+v", d)
	}
	if elapsed > 3*time.Second {
		t.Errorf("el fallo rápido debe ser del orden de 1s, tardó %s", elapsed)
	}
}

func TestDiscoverPortNoPortIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}
	requireProc(t)

	m := newTestManager(t)
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    "sleep 60",
		WorkDir:    dir,
		StdoutPath: dir + "/out.log",
		StderrPath: dir + "/err.log",
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second}) })

	start := time.Now()
	d := DiscoverPort(res.Pid, 0, "/health", 1*time.Second)
	elapsed := time.Since(start)

	if d.Port != 0 || len(d.All) != 0 {
		t.Errorf("sin listeners: %+v", d)
	}
	if d.LineageDead {
		t.Error("el linaje está vivo: no es un fallo de arranque")
	}
	if d.Unresolved {
		t.Error("sin un solo listener en toda la ventana no es 'sin resolver', es 'sin puerto'")
	}
	budget := 1*time.Second + DefaultDynamicUnresolvedGrace
	if elapsed > budget+3*time.Second {
		t.Errorf("el discovery debe respetar plazo+gracia (%s): tardó %s", budget, elapsed)
	}
}

func TestSocketInode(t *testing.T) {
	if ino, ok := socketInode("socket:[4242]"); !ok || ino != "4242" {
		t.Errorf("socketInode = %q, %v", ino, ok)
	}
	if _, ok := socketInode("/dev/null"); ok {
		t.Error("un fd que no es socket debe rechazarse")
	}
	if _, ok := socketInode("pipe:[3]"); ok {
		t.Error("una pipe no es un socket de red")
	}
}

// Asserts on the set size, not on a later reservation succeeding, because that cannot tell "no collision" from "the range ran out".
func TestReservePortIsConcurrencySafe(t *testing.T) {
	const workers = 400

	baseline := ReservedPortCount()

	type result struct {
		port int
		err  error
	}
	results := make([]result, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			port, err := ReservePort()
			results[i] = result{port: port, err: err}
		}(i)
	}
	close(start)
	wg.Wait()

	seen := make(map[int]int, workers)
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("reserva %d falló: %v", i, r.err)
		}
		if first, dup := seen[r.port]; dup {
			t.Fatalf("reservas %d y %d devolvieron el puerto %d", first, i, r.port)
		}
		seen[r.port] = i
	}
	if got := ReservedPortCount() - baseline; got != workers {
		t.Fatalf("el set creció en %d, want %d: una reserva o liberada o duplicada",
			got, workers)
	}

	// Returning them all must leave the set as it was: the set has to be given back, not only written to.
	ports := make([]int, 0, workers)
	for port := range seen {
		ports = append(ports, port)
	}
	for _, port := range ports {
		ReleasePort(port)
	}
	if got := ReservedPortCount(); got != baseline {
		t.Errorf("tras devolver %d puertos el set mide %d, want %d", len(ports), got, baseline)
	}
}

// M-B: an earlier version of this test had no assertion in the loop body and passed even if ReleasePort was a no-op.
func TestReleasePortShrinksTheSet(t *testing.T) {
	baseline := ReservedPortCount()

	port, err := ReservePort()
	if err != nil {
		t.Fatalf("ReservePort: %v", err)
	}
	if got := ReservedPortCount(); got != baseline+1 {
		t.Fatalf("tras reservar el set mide %d, want %d", got, baseline+1)
	}

	ReleasePort(port)

	if got := ReservedPortCount(); got != baseline {
		t.Errorf("tras liberar el set mide %d, want %d: la reserva no volvió",
			got, baseline)
	}
}

func TestReleasePortIgnoresUnreservedAndNonPositive(t *testing.T) {
	baseline := ReservedPortCount()

	ReleasePort(0)
	ReleasePort(-1)
	ReleasePort(DynamicPortHigh + 1)

	if got := ReservedPortCount(); got != baseline {
		t.Errorf("liberar puertos sin reservar cambió el set: %d != %d", got, baseline)
	}
}
