package portless

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A real server, not a double: what these tests check is whether verify tells 404 from 502, which needs a real status.
func newHTTPServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeFileIn(t *testing.T, dir, name, content string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// Declares proxyPort in a real state dir and keeps the real httpProbe: the behaviour of verify, not a double the test decides.
func newClientWithProxy(t *testing.T, proxyPort int, exec func(context.Context, string, ...string) (string, int, error)) *Client {
	t.Helper()
	dir := t.TempDir()
	if proxyPort > 0 {
		if err := writeFileIn(t, dir, proxyPortFile, itoa(proxyPort)); err != nil {
			t.Fatal(err)
		}
	}
	return New(
		WithBinary("/fake/portless"),
		WithStateDir(dir),
		WithExec(exec),
		WithTimeout(2*time.Second),
		// verifyWait 0: these tests assert the classification, not the propagation window, so the immediate probe keeps them fast and deterministic.
		WithVerifyWait(0),
	)
}

func okExec(t *testing.T) func(context.Context, string, ...string) (string, int, error) {
	t.Helper()
	f := newFake()
	return f.exec
}

func srvPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port of %q not numeric: %v", srv.URL, err)
	}
	return n
}

func TestVerifyPublishesWhenProxyActuallyRoutes(t *testing.T) {
	srv := newHTTPServer(t, http.StatusOK)
	c := newClientWithProxy(t, srvPort(t, srv), okExec(t))

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusRegistered {
		t.Fatalf("Status = %q (reason %q): with the proxy routing it should publish. %s", r.Status, r.Reason, srvCalls(r))
	}
	if !strings.HasPrefix(r.Url, "http://") {
		t.Errorf("Url = %q: without TLS the published scheme must be http", r.Url)
	}
	if !strings.Contains(r.Url, Hostname("svc")) {
		t.Errorf("Url = %q does not contain the route hostname", r.Url)
	}
}

// The window must never cost the COMMON case: a route the proxy already serves is answered on the first probe, so Apply returns without entering the wait. Asserting the elapsed time stays well under the window (not just that it eventually publishes) is what would go red if a regression started sleeping before probing.
func TestVerifyPublishesAServedRouteWithoutPayingTheWindow(t *testing.T) {
	srv := newHTTPServer(t, http.StatusOK)
	dir := t.TempDir()
	if err := writeFileIn(t, dir, proxyPortFile, itoa(srvPort(t, srv))); err != nil {
		t.Fatal(err)
	}
	c := New(
		WithBinary("/fake/portless"),
		WithStateDir(dir),
		WithExec(okExec(t)),
		WithTimeout(2*time.Second),
		WithVerifyWait(2*time.Second),
	)

	start := time.Now()
	r := c.Apply("svc", 8080, Ownership{})
	elapsed := time.Since(start)

	if r.Status != StatusRegistered || r.Url == "" {
		t.Fatalf("Status = %q url = %q (reason %q): a served route must publish on the first probe", r.Status, r.Url, r.Reason)
	}
	if elapsed >= c.verifyWait {
		t.Errorf("elapsed %v >= the window %v: the first probe served the route, the window must not be paid", elapsed, c.verifyWait)
	}
}

// verify must degrade BEFORE probing (M6): probing without a proxy means a request to an assumed port, the very constant this seam must not introduce.
func TestVerifyDoesNotProbeWithProxyDown(t *testing.T) {
	f := newFake()
	c := New(
		WithBinary("/fake/portless"),
		WithStateDir(t.TempDir()),
		WithExec(f.exec),
		WithProbe(f.probe),
		WithTimeout(2*time.Second),
	)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Reason != ReasonProxyNotRunning {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonProxyNotRunning)
	}
	if r.Url != "" {
		t.Errorf("Url = %q without proxy running", r.Url)
	}
	if !r.Registered {
		t.Error("Registered was lost: the registration happened anyway (M1)")
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "probe:") {
			t.Errorf("probed without knowing there is a proxy: %v", f.calls)
			break
		}
	}
}

// MEASURED: 404 means "I do not know this host", the opposite of routed, so publishing there asserts an address the proxy does not serve.
func TestVerifyDegradesWithoutPublishingURLWhenProxyDoesNotServeRoute(t *testing.T) {
	srv := newHTTPServer(t, http.StatusNotFound)
	c := newClientWithProxy(t, srvPort(t, srv), okExec(t))

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degraded with a proxy that responds 404", r.Status)
	}
	if r.Reason != ReasonRouteNotServed {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonRouteNotServed)
	}
	if r.Url != "" {
		t.Errorf("Url = %q: 404 proves the proxy does NOT route the route", r.Url)
	}
}

// The propagation window: portless exposes a freshly written route to its proxy asynchronously, so the first probes can see 404 while the route is already in routes.json. Apply must keep probing and publish the url once the proxy catches up, instead of degrading on the first stale read (the CI-only failure: an inotify-starved runner falls back to portless's 3s polling).
func TestVerifyWaitsForTheProxyToPickUpAFreshlyWrittenRoute(t *testing.T) {
	f := newFake()
	dir := t.TempDir()
	if err := writeFileIn(t, dir, proxyPortFile, itoa(1399)); err != nil {
		t.Fatal(err)
	}
	c := New(
		WithBinary("/fake/portless"),
		WithStateDir(dir),
		WithExec(f.exec),
		WithTimeout(500*time.Millisecond),
		WithVerifyWait(2*time.Second),
	)
	probes := 0
	c.probe = func(context.Context, string, string, int, string) (int, error) {
		probes++
		if probes < 3 {
			return http.StatusNotFound, nil
		}
		return http.StatusOK, nil
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusRegistered {
		t.Fatalf("Status = %q (reason %q): the proxy catches up inside the window, so the url must publish", r.Status, r.Reason)
	}
	if r.Url == "" {
		t.Error("Url must be published once the proxy serves the route")
	}
	if probes < 3 {
		t.Errorf("probes = %d: the 404 must be retried, not taken as final", probes)
	}
}

// The other side of the contract: when the proxy never picks the route up, the wait must end and the degradation must be the same route_not_served as before, so a genuinely unserved route cannot hang Apply.
func TestVerifyDegradesAfterThePropagationWindowCloses(t *testing.T) {
	f := newFake()
	dir := t.TempDir()
	if err := writeFileIn(t, dir, proxyPortFile, itoa(1399)); err != nil {
		t.Fatal(err)
	}
	c := New(
		WithBinary("/fake/portless"),
		WithStateDir(dir),
		WithExec(f.exec),
		WithTimeout(500*time.Millisecond),
		WithVerifyWait(120*time.Millisecond),
	)
	probes := 0
	c.probe = func(context.Context, string, string, int, string) (int, error) {
		probes++
		return http.StatusNotFound, nil
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degraded once the window closes", r.Status)
	}
	if r.Reason != ReasonRouteNotServed {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonRouteNotServed)
	}
	if probes < 2 {
		t.Errorf("probes = %d: a persistent 404 must be retried before it is declared final", probes)
	}
}

func TestVerifyAccepts502AsProofOfRouting(t *testing.T) {
	srv := newHTTPServer(t, http.StatusBadGateway)
	c := newClientWithProxy(t, srvPort(t, srv), okExec(t))

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusRegistered {
		t.Errorf("Status = %q (reason %q): a 502 proves the proxy routes. %s", r.Status, r.Reason, srvCalls(r))
	}
	if r.Url == "" {
		t.Error("a 502 must not prevent publishing the URL: the route is routed")
	}
}

// A declared port nobody listens on is a different failure from "the proxy answers and does not know the route", so they degrade with different reasons.
func TestVerifyDegradesWhenDeclaredPortDoesNotAcceptConnections(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	f := newFake()
	f.noProxy = true
	c := newClientWithProxy(t, deadPort, f.exec)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degraded", r.Status)
	}
	if r.Reason != ReasonProxyUnreachable {
		t.Errorf("Reason = %q, want %q: the declared port does not accept connections", r.Reason, ReasonProxyUnreachable)
	}
	if !r.Registered {
		t.Error("Registered was lost: the write happened before probing")
	}
}

// The other half of the dead-port contract: because a port nobody listens on can never serve the route, verify must return proxy_unreachable WITHOUT paying the whole window (the early break), not after it. With WithVerifyWait(0) the immediate break and the deadline break are indistinguishable; this test pins a window far larger than the connection-refused latency and asserts the call stays well under it, so dropping the early break (sleeping the window before degrading) goes red.
func TestVerifyDoesNotPayTheWindowForADeclaredPortThatRefusesConnections(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	f := newFake()
	f.noProxy = true
	dir := t.TempDir()
	if err := writeFileIn(t, dir, proxyPortFile, itoa(deadPort)); err != nil {
		t.Fatal(err)
	}
	c := New(
		WithBinary("/fake/portless"),
		WithStateDir(dir),
		WithExec(f.exec),
		WithTimeout(200*time.Millisecond),
		WithVerifyWait(2*time.Second),
	)

	start := time.Now()
	r := c.Apply("svc", 8080, Ownership{})
	elapsed := time.Since(start)

	if r.Reason != ReasonProxyUnreachable {
		t.Fatalf("Reason = %q, want %q: the declared port accepts no connections", r.Reason, ReasonProxyUnreachable)
	}
	if !r.Registered {
		t.Error("Registered was lost: the write happened before probing")
	}
	if elapsed >= c.verifyWait {
		t.Errorf("elapsed %v >= the window %v: a port that cannot serve must degrade at once, not after the wait", elapsed, c.verifyWait)
	}
}

// Both cases end in "no scheme answered" and are separated only by acceptsConnections; without it the user cannot tell a stopped portless from a missing route.
func TestVerifyDistinguishesAliveProxyNotServingFromDeadPort(t *testing.T) {
	srv := newHTTPServer(t, http.StatusNotFound)

	f := newFake()
	f.serve404[Hostname("svc")] = true
	c := newClientWithProxy(t, srvPort(t, srv), f.exec)

	r := c.Apply("svc", 8080, Ownership{})

	// acceptsConnections is only consulted when no scheme answered, so a 404 never reaches it.
	if r.Reason != ReasonRouteNotServed {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonRouteNotServed)
	}
	if r.Url != "" {
		t.Errorf("Url = %q without proven routing", r.Url)
	}
}

// prev not authorising that port is what makes M8 observable: if it did authorise it, the route is ours and rewriting it is legitimate.
func TestApplyReportsConflictWhenNameIsOwnedByAnotherPort(t *testing.T) {
	t.Run("without prior authorization: conflict and no write", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		if err := c.Register("svc", 9999); err != nil {
			t.Fatal(err)
		}
		before := f.routes[Hostname("svc")]

		r := c.Apply("svc", 8080, Ownership{})

		if r.Reason != ReasonRouteConflict {
			t.Errorf("Reason = %q, want %q", r.Reason, ReasonRouteConflict)
		}
		if r.Registered {
			t.Error("Registered=true in a conflict: it would claim a write that did not happen")
		}
		if got := f.routes[Hostname("svc")]; got != before {
			t.Errorf("the foreign port changed from %d to %d: another's route must not be touched", before, got)
		}
	})

	t.Run("with prior authorization of the same port: it is rewritten", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		if err := c.Register("svc", 8080); err != nil {
			t.Fatal(err)
		}

		r := c.Apply("svc", 8080, Ownership{Owned: true, Port: 8080})

		if r.Reason == ReasonRouteConflict {
			t.Error("a route that is already ours is not a conflict with itself")
		}
		if !r.Registered {
			t.Errorf("Reason = %q and it was not published: %v", r.Reason, srvCalls(r))
		}
	})
}

// The prior lookup decides whether the write is legitimate: writing without it is exactly the harm M8 allows, taking another owner's name unknowingly.
func TestApplyDoesNotWriteIfPriorStateCannotBeRead(t *testing.T) {
	f := newFake()
	c := f.client(t)
	if err := c.Register("svc", 9999); err != nil {
		t.Fatal(err)
	}
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			return "", 0, errors.New("portless list: routes.json unreadable")
		}
		return f.exec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Registered {
		t.Error("Registered=true: it was written without being able to read against what")
	}
	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degraded", r.Status)
	}
	if got := f.routes[Hostname("svc")]; got != 9999 {
		t.Errorf("the foreign port stayed at %d, want 9999 untouched: it should not have been written", got)
	}
}

// The faithful fake cannot express this, its list sees what its alias wrote, so the exec writes but lists something else: the real failure to catch.
func TestApplyDegradesIfReadBackDoesNotSeeIt(t *testing.T) {
	f := newFake()
	c := f.client(t)
	realExec := f.exec
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			return "\nActive routes:\n\n", 0, nil
		}
		return realExec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Reason != ReasonRouteNotServed {
		t.Errorf("Reason = %q, want %q: the registration went well but the route does not appear", r.Reason, ReasonRouteNotServed)
	}
	if r.Url != "" {
		t.Errorf("Url = %q: a route that is not seen cannot be published", r.Url)
	}
	if !r.Registered {
		t.Error("Registered was lost: the alias exited 0, so the write happened")
	}
}

// M8 live: the prior lookup sees a free name, another actor takes it in between, and reading back after writing would be a tautology.
func TestApplyDetectsOwnershipChangeBetweenQueryAndRegistration(t *testing.T) {
	f := newFake()
	c := f.client(t)
	realExec := f.exec
	calls := 0
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			calls++
			if calls == 1 {
				// Prior lookup: the name is free.
				return "\nActive routes:\n\n", 0, nil
			}
			// Read-back: another actor took it.
			host := Hostname("svc")
			return "  http://" + host + ":1355  ->  localhost:9999  (alias)\n", 0, nil
		}
		return realExec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Reason != ReasonRouteConflict {
		t.Errorf("Reason = %q, want %q: the name changed ownership in between", r.Reason, ReasonRouteConflict)
	}
	if r.Url != "" {
		t.Errorf("Url = %q with an unresolved conflict", r.Url)
	}
	// Degrading the status cannot undo the write: without Registered the route stays orphan and reconciliation cannot clean it.
	if !r.Registered {
		t.Error("Registered was lost: the registration happened (unconditional upsert, M8)")
	}
}

func TestApplyPropagatesRegistrationFailureWithoutClaimingAnything(t *testing.T) {
	f := newFake()
	c := f.client(t)
	realExec := f.exec
	// Only alias fails: if exec failed everywhere the failure would belong to the prior lookup and the test would never reach the write.
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "alias" {
			return "", 1, errors.New("Error: requires Node >= 24")
		}
		return realExec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degraded", r.Status)
	}
	if r.Registered {
		t.Error("Registered=true with failed registration: it would claim a write that did not happen")
	}
	if r.Reason == "" {
		t.Error("Empty Reason after a registration failure: the user would not know what happened")
	}
	if r.Url != "" {
		t.Errorf("Url = %q with failed registration", r.Url)
	}
}

// The r-nil branch builds the real client, which production stop paths use and cannot be exercised against a real portless, so it is checked with no resolvable binary.
func TestReleaseWithNilReleaserBuildsRealClient(t *testing.T) {
	t.Setenv("PORTLESS_BIN", "")
	t.Setenv("PATH", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PORTLESS_STATE_DIR", t.TempDir())

	if !Release(nil, "") {
		t.Error("empty name must revoke without building client")
	}

	// With a name but no portless, all that matters is that the real client does not blow up.
	got := Release(nil, "my-route")
	if !got {
		t.Log("without portless nothing can be claimed about the route: it does not revoke, and the handle is kept for reconciliation")
	}
}

// The Result as text for the failure message: tests in this file must fail with the reason, not with a bare "want registered".
func srvCalls(r Result) string {
	return "reason=" + r.Reason + " url=" + r.Url
}
