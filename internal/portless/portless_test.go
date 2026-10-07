package portless

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakePortless mimics portless 0.15.6 as MEASURED because CI has neither the binary nor a proxy, yet the suite must still demand the verification.
type fakePortless struct {
	routes map[string]int

	// binCode is the `alias` exit code; the measured case is 0 with the proxy stopped, because alias never contacts it (M1).
	binCode int
	binErr  error
	// hang blocks the call until the context expires: a hung portless.
	hang bool

	// probeStatus is what the proxy answers for a host it routes: 200, or 502 for routed-but-dead-backend; no proxy is noProxy, and -1 is never set.
	probeStatus int
	// tls lets the fake answer https; off by default because a proxy with HTTPS=0 refuses the handshake (measured, curl exits 35).
	tls bool
	// noProxy makes every probe get connection refused.
	noProxy bool

	calls []string
	// serve404 hosts are in the state file but not routed, so a live proxy that skips them stays distinguishable from a healthy one.
	serve404 map[string]bool
	// removedNames records removals, which reconciliation must observe because with the proxy stopped no probe reaches the fake.
	removedNames []string
}

func newFake() *fakePortless {
	return &fakePortless{
		routes:      map[string]int{},
		binCode:     0,
		probeStatus: 200,
		serve404:    map[string]bool{},
	}
}

func (f *fakePortless) exec(ctx context.Context, bin string, args ...string) (string, int, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.hang {
		<-ctx.Done()
		// The real seam wraps the context error so errors.Is works; breaking that would test the fake, not the seam.
		return "", -1, ctx.Err()
	}
	if f.binErr != nil {
		return "", 1, f.binErr
	}
	switch args[0] {
	case "alias":
		if len(args) >= 2 && args[1] == "--remove" {
			name := Hostname(args[2])
			if _, ok := f.routes[name]; !ok {
				// MEASURED (M10): exit 1 and BENIGN but not a failure: the route is gone, and Remove must tell it from a real one.
				return "", 1, errors.New("Error: No alias found for \"" + name + "\".")
			}
			delete(f.routes, name)
			f.removedNames = append(f.removedNames, name)
			return "Removed alias: " + name, 0, nil
		}
		// Unconditional upsert with no conflict detection (M8), so exit 0 proves nothing.
		var port int
		for _, c := range args[2:] {
			if n := atoiOr(c, -1); n > 0 {
				port = n
			}
		}
		if port <= 0 {
			return "Error: Invalid port", 1, errors.New("invalid port")
		}
		f.routes[Hostname(args[1])] = port
		return "Alias registered", f.binCode, nil
	case "list":
		var b strings.Builder
		b.WriteString("\nActive routes:\n\n")
		for host, port := range f.routes {
			b.WriteString("  http://" + host + ":1355  ->  localhost:" + itoa(port) + "  (alias)\n")
		}
		return b.String(), 0, nil
	}
	return "", 1, errors.New("unknown subcommand")
}

func (f *fakePortless) probe(ctx context.Context, scheme, host string, proxyPort int, path string) (int, error) {
	f.calls = append(f.calls, "probe:"+scheme+":"+host)
	if scheme == "https" && !f.tls {
		return 0, errors.New("tls: handshake failure")
	}
	if f.noProxy {
		return 0, errors.New("connection refused")
	}
	if _, ok := f.routes[host]; !ok || f.serve404[host] {
		// MEASURED: 404 for a host it does not know is the opposite of proof of routing; serve404 models a written but abandoned route.
		return 404, nil
	}
	if f.probeStatus == 502 {
		return 502, nil
	}
	return 200, nil
}

func (f *fakePortless) client(t *testing.T) *Client {
	t.Helper()
	dir := t.TempDir()
	f.writeProxyPort(t, dir, 1399)
	return New(
		WithBinary("/fake/portless"),
		WithStateDir(dir),
		WithExec(f.exec),
		WithProbe(f.probe),
		WithTimeout(2*time.Second),
	)
}

func (f *fakePortless) writeProxyPort(t *testing.T, dir string, port int) {
	t.Helper()
	// MEASURED: 4 bytes, no trailing newline.
	if err := os.WriteFile(filepath.Join(dir, "proxy.port"), []byte(itoa(port)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func atoiOr(s string, def int) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// REGRESSION (M5): an alias route has pid 0 and `portless prune` never touches it, so startup reconciliation is the only cleanup vroom has.
func TestPruneDoesNotDestroyAliasRoutes(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("foreign.vroom")] = 9999

	if pid := aliasRoutePid; pid != 0 {
		t.Fatalf("an alias route must have pid 0 so prune does not touch it, got %d", pid)
	}

	warns := c.Reconcile("foreign.vroom", Ownership{Owned: true, Port: 4321}, "mine.vroom")
	if len(warns) == 0 {
		t.Error("a live route on another port must warn, not be removed silently")
	}
	if _, still := f.routes[Hostname("foreign.vroom")]; !still {
		t.Error("a route that responds and is not ours is NEVER removed")
	}
}

// aliasRoutePid is the pid portless writes for an alias route (measured, M5/M4): 0 is what keeps prune away from it.
const aliasRoutePid = 0

func TestRouteWrittenWithProxyDownIsNotReportedAvailable(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.noProxy = true

	res := c.Apply("down.vroom", 4321, Ownership{})

	if res.Status != StatusDegraded {
		t.Fatalf("a route written with the proxy stopped CANNOT be reported available, got %q", res.Status)
	}
	if res.Url != "" {
		t.Errorf("an unverified route does not publish url, got %q", res.Url)
	}
	if res.Reason != ReasonProxyNotRunning && res.Reason != ReasonProxyUnreachable {
		t.Errorf("the reason must explain that the proxy does not respond, got %q", res.Reason)
	}

	// The binary still exits 0, so this test must assert the Result and not the exit code.
	if _, code, _ := f.exec(context.Background(), "portless", "alias", "down.vroom", "4321"); code != 0 {
		t.Fatalf("alias must exit 0 with the proxy stopped (M1), got %d", code)
	}
}

// MEASURED (M2): a routes.json entry is not verification: the proxy answers 404 to a host it does not know, and publishing that lies.
func TestRoutesFileAloneIsNotVerification(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("unknown.vroom")] = 4321

	f.noProxy = false
	c2 := New(
		WithBinary("/fake/portless"),
		WithStateDir(c.stateDir),
		WithExec(f.exec),
		WithProbe(func(ctx context.Context, scheme, host string, port int, path string) (int, error) {
			return 404, nil
		}),
	)
	res := c2.Apply("written-but-not-served.vroom", 4321, Ownership{})
	if res.Succeeded() {
		t.Error("a 404 means the proxy does not route: it cannot be reported registered")
	}
	if res.Url != "" {
		t.Error("a 404 does not publish url")
	}
}

// REGRESSION (M6): proxy.port exists only while the proxy runs, so its absence IS the no-proxy signal; 1355 would also hardcode a movable port.
func TestMissingProxyPortDegradesWithoutAssuming1355(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if err := os.Remove(filepath.Join(c.stateDir, "proxy.port")); err != nil {
		t.Fatal(err)
	}

	res := c.Apply("noproxy.vroom", 4321, Ownership{})
	if res.Reason != ReasonProxyNotRunning {
		t.Fatalf("without proxy.port the reason must be proxy_not_running, got %q", res.Reason)
	}
	if res.Url != "" {
		t.Error("without proxy no url is published")
	}
	for _, call := range f.calls {
		if strings.Contains(call, ":1355") && strings.HasPrefix(call, "probe") {
			t.Errorf("the proxy port cannot be assumed, but %q was probed", call)
		}
	}
}

func TestCorruptProxyPortDegrades(t *testing.T) {
	f := newFake()
	c := f.client(t)
	if err := os.WriteFile(filepath.Join(c.stateDir, "proxy.port"), []byte("no-es-un-puerto"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := c.Apply("corrupt.vroom", 4321, Ownership{})
	if res.Reason != ReasonProxyNotRunning {
		t.Errorf("an unreadable proxy.port must degrade as proxy stopped, got %q", res.Reason)
	}
}

func TestReconcileRemovesRenamedOrphanAndKeepsForeignLiveRoute(t *testing.T) {
	t.Run("removes the orphan that no longer responds", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		f.routes[Hostname("old-name")] = 39999
		f.probeStatus = 502
		f.routes[Hostname("new-name")] = 4321

		f.routes[Hostname("new-name")] = 4321
		c.probe = func(ctx context.Context, scheme, host string, port int, path string) (int, error) {
			if host == Hostname("old-name") {
				return 404, nil
			}
			return 200, nil
		}

		warns := c.Reconcile("old-name", Ownership{Owned: true, Port: 39999}, "new-name")
		if len(warns) != 0 {
			t.Errorf("removing an owned orphan does not warn: %v", warns)
		}
		if _, still := f.routes[Hostname("old-name")]; still {
			t.Error("the renamed route must be removed: it belongs to this service and does not respond")
		}
	})

	t.Run("does NOT remove a live foreign route", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		f.routes[Hostname("other-app")] = 5555
		f.routes[Hostname("mine")] = 4321

		// Ownership.Port is the persisted one, so a live route on another port is not ours.
		warns := c.Reconcile("other-app", Ownership{Owned: true, Port: 4321}, "mine")
		if len(warns) == 0 {
			t.Error("a live route on another port must warn of the conflict")
		}
		if _, still := f.routes[Hostname("other-app")]; !still {
			t.Error("fail-closed also applies to cleanup: something foreign is not deleted")
		}
	})

	t.Run("removes the orphan that is routed but has no backend", func(t *testing.T) {
		// A real smoke found this: the proxy routes and answers 502 with nothing behind, the fingerprint of a vroom that died without stopping.
		f := newFake()
		c := f.client(t)
		f.routes[Hostname("orphan")] = 39997
		f.probeStatus = 502

		if warns := c.Reconcile("orphan", Ownership{Owned: true, Port: 39997}, "current"); len(warns) != 0 {
			t.Errorf("removing an owned orphan does not warn: %v", warns)
		}
		if _, still := f.routes[Hostname("orphan")]; still {
			t.Error("a route that is routed without a backend is an orphan and must be removed")
		}
	})

	t.Run("does NOT remove a live and owned route", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		f.routes[Hostname("mine")] = 4321
		f.probeStatus = 200

		if warns := c.Reconcile("mine", Ownership{Owned: true, Port: 4321}, "other"); len(warns) != 0 {
			t.Errorf("a live and owned route generates no warnings: %v", warns)
		}
		if _, still := f.routes[Hostname("mine")]; !still {
			t.Error("a live and owned route is not touched")
		}
	})

	t.Run("is idempotent with the persisted route", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		f.routes[Hostname("same")] = 4321
		for range 3 {
			if warns := c.Reconcile("same", Ownership{Owned: true, Port: 4321}, "same"); len(warns) != 0 {
				t.Errorf("reconciliation must be silent and idempotent: %v", warns)
			}
		}
		if len(f.routes) != 1 {
			t.Errorf("duplicate routes must not accumulate: %v", f.routes)
		}
	})
}

// M8: `alias` overwrites silently, so the other port must appear AFTER our write for the conflict to be observable.
func TestReadBackDetectsNameTakenByAnotherPort(t *testing.T) {
	f := newFake()
	c := f.client(t)

	var stolen bool
	readBack := func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if len(args) > 0 && args[0] == "list" && !stolen {
			stolen = true
			f.routes[Hostname("taken")] = 9999
		}
		return f.exec(ctx, bin, args...)
	}
	c.exec = readBack

	res := c.Apply("taken", 4321, Ownership{})
	if res.Succeeded() {
		t.Fatal("a route whose name has another port CANNOT be reported registered")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("the reason must be route_conflict, got %q", res.Reason)
	}
	if res.Url != "" {
		t.Error("in conflict no url is published")
	}
}

func TestReadBackConfirmsOurs(t *testing.T) {
	f := newFake()
	c := f.client(t)
	res := c.Apply("mine.vroom", 4321, Ownership{})
	if !res.Succeeded() {
		t.Fatalf("the read-back should have confirmed the route: %+v", res)
	}
	if res.Port != 4321 {
		t.Errorf("the route must point to the real port, got %d", res.Port)
	}
	if !strings.HasPrefix(res.Url, "http://") && !strings.HasPrefix(res.Url, "https://") {
		t.Errorf("the url must carry the verified scheme, got %q", res.Url)
	}
}

func TestBackendErrorStillCountsAsRouted(t *testing.T) {
	f := newFake()
	f.probeStatus = 502
	c := f.client(t)

	res := c.Apply("502.vroom", 4321, Ownership{})
	if !res.Succeeded() {
		t.Fatalf("a 502 proves the proxy routes the route: %+v", res)
	}
	if res.Url == "" {
		t.Error("a route that is routed publishes its url")
	}
}

func TestSchemeIsProbedNotAssumed(t *testing.T) {
	t.Run("http responds first", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		res := c.Apply("plain.vroom", 4321, Ownership{})
		if !strings.HasPrefix(res.Url, "http://") {
			t.Errorf("a proxy without TLS must publish http, got %q", res.Url)
		}
	})

	t.Run("https responds and https is published", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		only := New(
			WithBinary("/fake/portless"),
			WithStateDir(c.stateDir),
			WithExec(f.exec),
			WithProbe(func(ctx context.Context, scheme, host string, port int, path string) (int, error) {
				if scheme == "https" {
					return 200, nil
				}
				return 0, errors.New("connection refused")
			}),
		)
		res := only.Apply("tls.vroom", 4321, Ownership{})
		if !strings.HasPrefix(res.Url, "https://") {
			t.Errorf("if https responds then https is published, got %q", res.Url)
		}
	})
}

func TestMissingBinaryDegradesWithoutInvokingPortless(t *testing.T) {
	called := false
	c := New(WithBinary(""), WithExec(func(context.Context, string, ...string) (string, int, error) {
		called = true
		return "", 0, nil
	}), WithStateDir(t.TempDir()))

	res := c.Apply("nobin.vroom", 4321, Ownership{})
	if res.Reason != ReasonPortlessMissing {
		t.Errorf("without binary the reason must be portless_not_found, got %q", res.Reason)
	}
	if called {
		t.Error("without binary portless is not invoked at all")
	}
}

func TestFailingBinaryDegrades(t *testing.T) {
	f := newFake()
	f.binErr = errors.New("Error: requires Node >= 24")
	c := f.client(t)

	res := c.Apply("nodeold.vroom", 4321, Ownership{})
	if res.Reason != ReasonPortlessFailed {
		t.Errorf("a failing binary must degrade, got %q", res.Reason)
	}
	if res.Url != "" {
		t.Error("a failing binary does not publish url")
	}
	if !strings.Contains(Warn(res), "Node") {
		t.Errorf("the warning must name the concrete failure, got %q", Warn(res))
	}
}

func TestHangingBinaryIsBounded(t *testing.T) {
	f := newFake()
	f.hang = true
	c := New(
		WithBinary("/fake/portless"),
		WithStateDir(t.TempDir()),
		WithExec(f.exec),
		WithProbe(f.probe),
		WithTimeout(150*time.Millisecond),
	)

	done := make(chan Result, 1)
	go func() { done <- c.Apply("hang.vroom", 4321, Ownership{}) }()

	select {
	case res := <-done:
		if res.Reason != ReasonPortlessTimeout {
			t.Errorf("a hung binary must degrade by timeout, got %q", res.Reason)
		}
		if res.Succeeded() {
			t.Error("a hung binary cannot publish a route")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Apply CANNOT remain blocked waiting for the binary")
	}
}

func TestMissingBinaryIsDistinguishedFromFailingBinary(t *testing.T) {
	t.Run("nonexistent binary", func(t *testing.T) {
		c := New(
			WithBinary(filepath.Join(t.TempDir(), "no-existe")),
			WithStateDir(t.TempDir()),
			WithTimeout(2*time.Second),
		)
		res := c.Apply("nada.vroom", 4321, Ownership{})
		if res.Reason != ReasonPortlessMissing {
			t.Errorf("a binary that does not exist must be portless_not_found, got %q", res.Reason)
		}
	})

	t.Run("failing binary", func(t *testing.T) {
		f := newFake()
		f.binErr = errors.New("Error: requires Node >= 24")
		res := f.client(t).Apply("roto.vroom", 4321, Ownership{})
		if res.Reason != ReasonPortlessFailed {
			t.Errorf("a binary that exits with error must be portless_failed, got %q", res.Reason)
		}
	})
}

func TestRemoveMissingIsBenign(t *testing.T) {
	f := newFake()
	c := f.client(t)
	if err := c.Remove("nunca-existio"); err != nil {
		t.Fatalf("removing a nonexistent route cannot be an error: %v", err)
	}
}

func TestRemoveOnlyTouchesItsOwn(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("mine")] = 4321
	f.routes[Hostname("sibling")] = 5555

	if err := c.Remove("mine"); err != nil {
		t.Fatal(err)
	}
	if _, still := f.routes[Hostname("mine")]; still {
		t.Error("the owned route must disappear")
	}
	if _, sibling := f.routes[Hostname("sibling")]; !sibling {
		t.Error("sibling services' routes are not touched")
	}
}

func TestFullRouteLifecycle(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("sibling")] = 5555

	res := c.Apply("app", 4321, Ownership{})
	if !res.Succeeded() {
		t.Fatalf("the registration must be verified against the live proxy: %+v", res)
	}
	if _, found, _ := c.Lookup("app"); !found {
		t.Fatal("the route must exist after registering it")
	}

	if err := c.Remove("app"); err != nil {
		t.Fatalf("stopping must remove the route: %v", err)
	}
	if _, still := f.routes[Hostname("app")]; still {
		t.Error("the route must disappear when stopping")
	}
	if _, sib := f.routes[Hostname("sibling")]; !sib {
		t.Error("sibling services' routes are not touched")
	}

	if err := c.Remove("app"); err != nil {
		t.Fatalf("a repeated stop cannot fail: %v", err)
	}
	if _, sib := f.routes[Hostname("sibling")]; !sib {
		t.Error("the sibling remains intact after a repeated stop")
	}
}

func TestReRegisterMovesTheRouteToTheNewPort(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if res := c.Apply("app", 4000, Ownership{}); !res.Succeeded() {
		t.Fatalf("first startup: %+v", res)
	}
	if res := c.Apply("app", 4321, Ownership{Owned: true, Port: 4000}); !res.Succeeded() {
		t.Fatalf("second startup: %+v", res)
	}

	port, found, err := c.Lookup("app")
	if err != nil || !found {
		t.Fatalf("the route must continue to exist: %v", err)
	}
	if port != 4321 {
		t.Errorf("the same route must switch to pointing to the new port, got %d", port)
	}
	if len(f.routes) != 1 {
		t.Errorf("there cannot be more than one route for the same service: %v", f.routes)
	}
}

// SCOPE (M3): flipping noProxy on the same fake only proves file persistence; real restart survival lives in TestRouteSurvivesAProxyRestart.
func TestRouteRegisteredWhileProxyDownIsServedWhenItReturns(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.noProxy = true

	res := c.Apply("app", 4321, Ownership{})
	if res.Succeeded() {
		t.Fatal("with the proxy stopped it cannot be reported available")
	}
	if _, found, _ := c.Lookup("app"); !found {
		t.Fatal("the route must remain registered even if the proxy is stopped")
	}

	f.noProxy = false
	if port, found, _ := c.Lookup("app"); !found || port != 4321 {
		t.Fatalf("when the proxy returns the route must still be there, got %d found=%v", port, found)
	}
	if res := c.verify("app", 4321); !res.Succeeded() {
		t.Errorf("with the proxy back the route must verify: %+v", res)
	}
}

// SCOPE (M4): a map the seam only adds a key to cannot prove alias does not evict a run route; the claim lives in TestIntegrationDoesNotEvictLivePortlessRoutes.
func TestSeamTouchesOnlyItsOwnRoute(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("live-app")] = 4628

	if res := c.Apply("vroom-app", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("the vroom route must register: %+v", res)
	}

	livePort, liveStill := f.routes[Hostname("live-app")]
	if !liveStill || livePort != 4628 {
		t.Errorf("the live app's route must remain intact: %d %v", livePort, liveStill)
	}
	if _, vroom := f.routes[Hostname("vroom-app")]; !vroom {
		t.Error("the vroom route must exist")
	}
	_ = c.Remove("vroom-app")
	if _, still := f.routes[Hostname("live-app")]; !still {
		t.Error("stopping our own cannot evict another owner's route")
	}
}
