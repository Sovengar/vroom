package startsvc

import (
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/state"
)

func listen(port int) (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
}

func TestDynamicStartResolvesRealPort(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("dynamic start: %v", err)
	}
	f.cleanup(t, out)

	if out.Port < process.DynamicPortLow || out.Port > process.DynamicPortHigh {
		t.Errorf("the port must fall in %d-%d, got %d", process.DynamicPortLow, process.DynamicPortHigh, out.Port)
	}
	if !process.PortOpen(out.Port) {
		t.Errorf("the process does not listen on the resolved port %d", out.Port)
	}
	if got := f.helperEnv(t)["PORT_SEEN"]; got != strconv.Itoa(out.Port) {
		t.Errorf("PORT in the child = %q, want %d", got, out.Port)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("an app that honors PORT must not generate warnings: %v", out.Warnings)
	}
}

func TestDynamicStartPersistsBeforeReturning(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("dynamic start: %v", err)
	}
	f.cleanup(t, out)

	meta, err := f.store.LoadMeta(f.dir)
	if err != nil {
		t.Fatalf("meta.json must exist when returning from Start: %v", err)
	}
	if meta.Port != out.Port {
		t.Errorf("meta.Port = %d, want the real port %d", meta.Port, out.Port)
	}
	if meta.State != state.StateRunning {
		t.Errorf("meta.State = %q, want running", meta.State)
	}
	if !meta.PortVerified {
		t.Error("a confirmed listener must be marked verified")
	}
	if meta.Pid != out.Pid {
		t.Errorf("meta.Pid = %d, want %d", meta.Pid, out.Pid)
	}
}

func TestDynamicPortIsTheSingleSourceOfTruth(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("dynamic start: %v", err)
	}
	f.cleanup(t, out)

	meta, err := f.store.LoadMeta(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Port == f.manifest.Port {
		t.Fatal("the real port must differ from the declared one for the test to mean something")
	}
	// The health probe must gate on meta.Port, never on the manifest.
	if !process.PortOpen(meta.Port) {
		t.Errorf("the probe must point to %d, which is open", meta.Port)
	}
	if process.PortOpen(f.manifest.Port) {
		t.Error("nobody should be listening on the declared port")
	}
}

func TestChildEnvIsNotTruncated(t *testing.T) {
	f := newFixture(t)
	t.Setenv("PATH", os.Getenv("PATH"))
	f.command(t, "honors-port")
	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("dynamic start: %v", err)
	}
	f.cleanup(t, out)

	env := f.helperEnv(t)
	if env["PATH_SEEN"] == "" {
		t.Error("PATH did not reach the child: cmd.Env replaced os.Environ()")
	}
	if env["HOME_SEEN"] == "" {
		t.Error("HOME did not reach the child")
	}
	if env["PORT_SEEN"] == "" {
		t.Error("PORT did not reach the child")
	}
	if env["HOST_SEEN"] != "127.0.0.1" {
		t.Errorf("HOST = %q, want 127.0.0.1", env["HOST_SEEN"])
	}
}

func TestChildResolvesCommandsByPath(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port")
	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("dynamic start: %v", err)
	}
	f.cleanup(t, out)

	if got := f.helperEnv(t)["SH_RESOLVED"]; got != "ok" {
		t.Errorf("the child could not resolve sh through its PATH: %q", got)
	}
}

func TestAppIgnoringPortIsWarningNotError(t *testing.T) {
	f := newFixture(t)
	own := freePort(t)
	f.command(t, "fixed-port", "VROOM_HELPER_PORT="+strconv.Itoa(own))
	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("ignoring PORT must not fail the start: %v", err)
	}
	f.cleanup(t, out)

	if out.Port != own {
		t.Errorf("the discovered real port must be the app's (%d), got %d", own, out.Port)
	}
	if len(out.Warnings) == 0 {
		t.Fatal("a visible warning must be emitted that the app ignored the port")
	}
	if !strings.Contains(out.Warnings[0], strconv.Itoa(own)) {
		t.Errorf("the warning must name the real port: %q", out.Warnings[0])
	}
	if out.Meta.State != state.StateRunning {
		t.Errorf("the service remains operable, state = %q", out.Meta.State)
	}
}

func TestSlowBindKeepsItsPort(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port", "VROOM_HELPER_DELAY=3s")
	out, err := f.start(t, 10*time.Second)
	if err != nil {
		t.Fatalf("dynamic start: %v", err)
	}
	f.cleanup(t, out)

	if out.Port == 0 {
		t.Fatal("a slow bind must not be reported as without a port")
	}
	if out.Meta.State != state.StateRunning {
		t.Errorf("state = %q, want running", out.Meta.State)
	}
	if !process.PortOpen(out.Port) {
		t.Errorf("port %d should be open", out.Port)
	}
}

func TestNoTCPPortIsRecordedNotHung(t *testing.T) {
	f := newFixture(t)
	f.command(t, "udp-only")

	start := time.Now()
	out, err := f.start(t, 700*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a service without a TCP port is not a start failure: %v", err)
	}
	f.cleanup(t, out)

	if elapsed > 20*time.Second {
		t.Errorf("the start must not hang: took %s", elapsed)
	}
	if out.Meta.State != state.StateNoPort {
		t.Errorf("state = %q, want no_port", out.Meta.State)
	}
	if out.Meta.Port != 0 {
		t.Errorf("a service without a port must persist 0, got %d", out.Meta.Port)
	}
	if len(out.Warnings) == 0 {
		t.Error("it must be explicitly recorded that there is no port")
	}
	if out.Pid <= 0 {
		t.Error("the service must remain equally operable")
	}
}

func TestDeadAtStartupFailsFast(t *testing.T) {
	f := newFixture(t)
	f.command(t, "die")

	start := time.Now()
	out, err := f.start(t, 30*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a service that dies must be reported as a start failure")
	}
	if elapsed > 5*time.Second {
		t.Errorf("the failure must be on the order of 1s, took %s (exhausted the discovery timeout)", elapsed)
	}
	// The helper exits 1 on its own, so there is nothing to stop and no cleanup; the package hygiene guard verifies that assumption at the end.
	if out.Pid != 0 {
		t.Errorf("a failed start must not return a process that someone has to stop: %d", out.Pid)
	}
}

func TestFixedModeBehavesExactlyAsBefore(t *testing.T) {
	f := newFixture(t)
	f.manifest.PortMode = "" // an existing manifest, without the new field
	own := freePort(t)
	f.command(t, "fixed-port", "VROOM_HELPER_PORT="+strconv.Itoa(own))

	out, err := f.start(t, 30*time.Second)
	if err != nil {
		t.Fatalf("fixed start: %v", err)
	}
	f.cleanup(t, out)

	meta, _ := f.store.LoadMeta(f.dir)
	if meta.Port != f.manifest.Port {
		t.Errorf("in fixed mode meta.Port must be the declared one (%d), got %d", f.manifest.Port, meta.Port)
	}
	if len(out.Warnings) != 0 {
		t.Errorf("fixed mode must not emit new warnings: %v", out.Warnings)
	}
	if env := f.helperEnv(t); env["PORT_SEEN"] != "" {
		t.Errorf("in fixed mode PORT is not injected, got %q", env["PORT_SEEN"])
	}
}

func TestNoneModeStartsWithoutPort(t *testing.T) {
	f := newFixture(t)
	f.manifest.PortMode = manifest.PortModeNone
	f.manifest.Port = 0
	f.command(t, "udp-only")

	out, err := f.start(t, 30*time.Second)
	if err != nil {
		t.Fatalf("none start: %v", err)
	}
	f.cleanup(t, out)

	if out.Port != 0 {
		t.Errorf("none mode must not have a port, got %d", out.Port)
	}
	if env := f.helperEnv(t); env["PORT_SEEN"] != "" {
		t.Errorf("none mode does not inject PORT, got %q", env["PORT_SEEN"])
	}
	if out.Meta.State == state.StatePortPending {
		t.Error("none mode is never pending for a port")
	}
}

func TestStartWindowNeverReportsDeadProcessAsStopped(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honors-port", "VROOM_HELPER_DELAY=2s")

	done := make(chan Result, 1)
	go func() {
		out, err := f.start(t, 10*time.Second)
		if err != nil {
			t.Errorf("start: %v", err)
		}
		done <- out
	}()

	deadline := time.Now().Add(2 * time.Second)
	sawPending := false
	for time.Now().Before(deadline) {
		meta, err := f.store.LoadMeta(f.dir)
		if err == nil && meta.Pid > 0 {
			if meta.State != state.StatePortPending {
				t.Fatalf("during the window the state must be port_pending, is %q", meta.State)
			}
			sawPending = true
		}
		time.Sleep(50 * time.Millisecond)
	}
	out := <-done
	f.cleanup(t, out)

	if !sawPending {
		t.Skip("discovery resolved before the window could be observed")
	}
	if out.Meta.State != state.StateRunning {
		t.Errorf("when the port resolves the state becomes alive, is %q", out.Meta.State)
	}
}

// H1: the sequential test below cannot catch this by construction, since it waits for A to resolve while A's listener holds the port; production starts in parallel via tea.Batch.
func TestConcurrentDynamicStartsGetDistinctPorts(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}

	const n = 6 // above the number of concurrent helpers that runs without flakiness
	fixtures := make([]*fixture, n)
	for i := range fixtures {
		fixtures[i] = newFixture(t)
		fixtures[i].command(t, "honors-port")
	}

	results := make([]Result, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range fixtures {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // all at once: the race is the point, not the sequence
			results[i], errs[i] = fixtures[i].start(t, 8*time.Second)
		}(i)
	}
	close(start)
	wg.Wait()

	for i := range fixtures {
		if errs[i] != nil {
			t.Fatalf("start %d: %v", i, errs[i])
		}
		fixtures[i].cleanup(t, results[i])
	}

	seen := map[int]int{}
	for i, r := range results {
		if r.Port == 0 {
			t.Errorf("start %d did not resolve a port: %+v", i, r)
		}
		if first, dup := seen[r.Port]; dup {
			t.Fatalf("starts %d and %d share port %d", first, i, r.Port)
		}
		seen[r.Port] = i
	}
}

// Sequential is the feature premise (two worktrees at once) and holds because A's port is not returned to the set while its process lives.
func TestTwoDynamicStartsGetDistinctPorts(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	a, b := newFixture(t), newFixture(t)
	a.command(t, "honors-port")
	b.command(t, "honors-port")

	outA, err := a.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("start A: %v", err)
	}
	a.cleanup(t, outA)
	outB, err := b.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("start B: %v", err)
	}
	b.cleanup(t, outB)

	if outA.Port == outB.Port {
		t.Errorf("two simultaneous services share port %d", outA.Port)
	}
}

// The old version only asserted 40 failed starts, which still holds once the range is exhausted, so the assertion is on set size instead.
func TestFailedAttemptReleasesItsReservedPort(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	f := newFixture(t)
	f.command(t, "die") // dies before binding

	for i := 0; i < 40; i++ {
		baseline := process.ReservedPortCount()
		out, err := f.start(t, 5*time.Second)
		if err == nil {
			t.Fatal("a service that dies must fail the start")
		}
		if out.Pid != 0 {
			t.Errorf("attempt %d returned pid %d without stopping anything", i, out.Pid)
		}
		if got := process.ReservedPortCount(); got != baseline {
			t.Fatalf("attempt %d left the reservation in the set: %d != %d", i, got, baseline)
		}
	}
}

// M-B: without this the set grows every start/stop cycle until a long-lived process has no ports left to offer.
func TestStopReleasesTheReservation(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	f := newFixture(t)
	f.command(t, "honors-port")

	baseline := process.ReservedPortCount()

	out, err := f.start(t, 5*time.Second)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	f.cleanup(t, out)

	// ReservedPort is persisted apart from the real port, which is what lets stop know which reservation to return.
	meta, err := f.store.LoadMeta(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ReservedPort == 0 {
		t.Fatal("meta.ReservedPort was not persisted: stop would have nothing to release")
	}
	if got := process.ReservedPortCount(); got != baseline+1 {
		t.Fatalf("the set measures %d after starting, want %d", got, baseline+1)
	}

	if err := process.NewManager().Stop(process.StopSpec{
		Pid: out.Pid, Pgid: out.Meta.Pgid, Port: out.Meta.Port, Timeout: 2 * time.Second,
	}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	process.ReleasePort(meta.ReservedPort)

	if got := process.ReservedPortCount(); got != baseline {
		t.Errorf("after stopping, the set measures %d, want %d: the reservation did not return", got, baseline)
	}
}

// dynamic needs a default port because the app contract is PORT=${PORT:-N}.
func TestDynamicRequiresDefaultPort(t *testing.T) {
	m := &manifest.Manifest{Name: "x", Command: "true", PortMode: manifest.PortModeDynamic}
	if err := m.Validate(); err == nil {
		t.Error("dynamic without a default port must be rejected in validation")
	}
}

func TestR2HealthPathDecidesMainPort(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	f := newFixture(t)
	f.manifest.HealthPath = "/health"
	f.command(t, "two-http-ports",
		"VROOM_HELPER_PORT_A="+strconv.Itoa(freePort(t)),
		"VROOM_HELPER_GOOD="+strconv.Itoa(freePort(t)))

	out, err := f.start(t, 10*time.Second)
	if err != nil {
		t.Fatalf("dynamic start: %v", err)
	}
	f.cleanup(t, out)

	env := f.helperEnv(t)
	good := mustAtoiT(t, env["GOOD_PORT"])
	if out.Port != good {
		t.Errorf("R2 must choose the listener that responds on health_path (%d), chose %d", good, out.Port)
	}
	if !out.Meta.PortVerified {
		t.Error("R2 verifies the port: someone responded")
	}
}

func TestR3NonHTTPPicksLowestAndMarksUnverified(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	f := newFixture(t)
	f.manifest.HealthPath = "/health"
	f.command(t, "two-raw-ports",
		"VROOM_HELPER_PORT_A="+strconv.Itoa(freePort(t)),
		"VROOM_HELPER_PORT_B="+strconv.Itoa(freePort(t)))

	out, err := f.start(t, 10*time.Second)
	if err != nil {
		t.Fatalf("dynamic start: %v", err)
	}
	f.cleanup(t, out)

	env := f.helperEnv(t)
	low := minPort(mustAtoiT(t, env["PORT_A"]), mustAtoiT(t, env["PORT_B"]))
	if out.Port != low {
		t.Errorf("R3 must choose the lowest port %d, chose %d", low, out.Port)
	}
	if out.Meta.PortVerified {
		t.Error("without an HTTP response the port is not verified")
	}
	found := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "unverified") {
			found = true
		}
	}
	if !found {
		t.Errorf("it must be declared that it is impossible to know which is the main one: %v", out.Warnings)
	}

	// Deterministic across runs given the same set of listeners.
	again := newFixture(t)
	again.manifest.HealthPath = "/health"
	again.command(t, "two-raw-ports",
		"VROOM_HELPER_PORT_A="+strconv.Itoa(freePort(t)),
		"VROOM_HELPER_PORT_B="+strconv.Itoa(freePort(t)))
	out2, err := again.start(t, 10*time.Second)
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	again.cleanup(t, out2)
	env2 := again.helperEnv(t)
	low2 := minPort(mustAtoiT(t, env2["PORT_A"]), mustAtoiT(t, env2["PORT_B"]))
	if out2.Port != low2 {
		t.Errorf("R3 must be deterministic: %d vs %d", out2.Port, low2)
	}
}

// M2: deadline expiry does not prove absence (a 12s Next.js and a UDP-only worker look alike for 12s), hence the second recovery window; TestSlowBindKeepsItsPort only covered the happy path.
func TestBindsAfterDeadlineIsRecoveredNotNoPort(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	f := newFixture(t)
	// the bind lands 3s in, discovery gives up at 700ms.
	f.command(t, "honors-port", "VROOM_HELPER_DELAY=3s")

	out, err := f.start(t, 700*time.Millisecond)
	if err != nil {
		t.Fatalf("deadline expired is not a start failure: %v", err)
	}
	f.cleanup(t, out)

	if out.Meta.State == state.StateNoPort {
		t.Fatal("a service that binds after the deadline is not a service without a port")
	}
	if out.Meta.State != state.StateRunning {
		t.Fatalf("State = %q, want running (the grace window must recover the port)", out.Meta.State)
	}
	if out.Port == 0 {
		t.Fatal("the grace window should have resolved the port")
	}
	if !process.PortOpen(out.Port) {
		t.Errorf("the recovered port %d should be listening", out.Port)
	}
	if !out.Meta.PortVerified {
		t.Error("the recovered port is verified against a real listener")
	}
}

func TestGenuinelyNoPortIsStillNoPort(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	f := newFixture(t)
	f.command(t, "udp-only") // alive and never opens a TCP port

	out, err := f.start(t, 700*time.Millisecond)
	if err != nil {
		t.Fatalf("a service without a TCP port is not a failure: %v", err)
	}
	f.cleanup(t, out)

	if out.Meta.State != state.StateNoPort {
		t.Errorf("State = %q, want no_port (alive and without a TCP listener throughout the window)", out.Meta.State)
	}
	if out.Meta.Port != 0 {
		t.Errorf("meta.Port = %d, want 0", out.Meta.Port)
	}
}

// A third case distinct from both no_port and all-fine: listeners exist but none can be called main, so it is named without deciding and no port is invented.
func TestChurningListenersEndUnresolved(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}
	f := newFixture(t)
	f.command(t, "churn") // new listeners every 100ms, an unstable set

	out, err := f.start(t, 700*time.Millisecond)
	if err != nil {
		t.Fatalf("undecided port is not a start failure: %v", err)
	}
	f.cleanup(t, out)

	if out.Meta.State == state.StateNoPort {
		t.Fatal("a service with listeners is not a service without a port")
	}
	if out.Meta.State != state.StatePortUnresolved {
		t.Fatalf("State = %q, want port_unresolved", out.Meta.State)
	}
	if out.Meta.Port != 0 {
		t.Errorf("no port was decided, meta.Port must remain 0, is %d", out.Meta.Port)
	}
	if out.Meta.PortVerified {
		t.Error("an undecided port cannot be marked verified")
	}
	if len(out.Warnings) == 0 {
		t.Error("the user must be informed that the port was not resolved")
	}
}
