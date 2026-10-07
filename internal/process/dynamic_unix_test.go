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
			t.Fatalf("port %d out of range %d-%d", port, DynamicPortLow, DynamicPortHigh)
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
		t.Errorf("merged = %v, want %d entries without duplicates", merged, len(want))
	}
}

func TestMergeEnvNilWhenNothingToInject(t *testing.T) {
	if got := mergeEnv([]string{"PATH=/usr/bin"}, nil); got != nil {
		t.Errorf("without spec.Env it should remain nil (inheritance), got %v", got)
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
		t.Errorf("a sleep has no listeners, got %v", got)
	}
	if got := lineageListenersAt(procRoot, os.Getpid()); !containsPid(got, port) {
		t.Errorf("the listener %d of the test process must appear in its lineage, got %v", port, got)
	}
}

func TestDiscoverPortFindsListenerInLineage(t *testing.T) {
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
		Command:    testBinary(t) + " -test.run=^TestHelperListener$ & sleep 60",
		WorkDir:    dir,
		StdoutPath: dir + "/out.log",
		StderrPath: dir + "/err.log",
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: time.Second}) })

	waitPortOpen(t, port, 5*time.Second)

	d := DiscoverPort(res.Pid, port, "/health", 3*time.Second)
	if d.Port != port {
		t.Errorf("DiscoverPort = %+v, want port %d of the listener", d, port)
	}
	if !d.HonoredReserved {
		t.Error("a listener equal to the reserved one is R1: deterministic and without heuristics")
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
		t.Errorf("a dead lineage must abort discovery: %+v", d)
	}
	if elapsed > 3*time.Second {
		t.Errorf("the fast failure should be on the order of 1s, took %s", elapsed)
	}
}

func TestDiscoverPortNoPortIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
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
		t.Errorf("no listeners: %+v", d)
	}
	if d.LineageDead {
		t.Error("the lineage is alive: it is not a startup failure")
	}
	if d.Unresolved {
		t.Error("without a single listener in the entire window it is not 'unresolved', it is 'no port'")
	}
	budget := 1*time.Second + DefaultDynamicUnresolvedGrace
	if elapsed > budget+3*time.Second {
		t.Errorf("discovery must respect deadline+grace (%s): took %s", budget, elapsed)
	}
}

func TestSocketInode(t *testing.T) {
	if ino, ok := socketInode("socket:[4242]"); !ok || ino != "4242" {
		t.Errorf("socketInode = %q, %v", ino, ok)
	}
	if _, ok := socketInode("/dev/null"); ok {
		t.Error("an fd that is not a socket must be rejected")
	}
	if _, ok := socketInode("pipe:[3]"); ok {
		t.Error("a pipe is not a network socket")
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
			t.Fatalf("reservation %d failed: %v", i, r.err)
		}
		if first, dup := seen[r.port]; dup {
			t.Fatalf("reservations %d and %d returned port %d", first, i, r.port)
		}
		seen[r.port] = i
	}
	if got := ReservedPortCount() - baseline; got != workers {
		t.Fatalf("the set grew by %d, want %d: a reservation either released or duplicated",
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
		t.Errorf("after returning %d ports the set measures %d, want %d", len(ports), got, baseline)
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
		t.Fatalf("after reserving the set measures %d, want %d", got, baseline+1)
	}

	ReleasePort(port)

	if got := ReservedPortCount(); got != baseline {
		t.Errorf("after releasing the set measures %d, want %d: the reservation did not return",
			got, baseline)
	}
}

func TestReleasePortIgnoresUnreservedAndNonPositive(t *testing.T) {
	baseline := ReservedPortCount()

	ReleasePort(0)
	ReleasePort(-1)
	ReleasePort(DynamicPortHigh + 1)

	if got := ReservedPortCount(); got != baseline {
		t.Errorf("releasing unreserved ports changed the set: %d != %d", got, baseline)
	}
}
