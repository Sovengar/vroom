package portless_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/portless"
)

// The claim ladder against a REAL portless behind a LIVE proxy. The seam cannot show any of it: it can fake a route_conflict, but not that the conflict comes out of the real routes.json, that `portless alias` would silently OVERWRITE the first worktree's route (so only vroom's pre-check protects that URL), that the fallback rung publishes its own verified URL, or that Retire deletes the abandoned name for real.
// The loop that walks the ladder lives in startsvc.applyRoute and stays seam-tested (internal/startsvc/route_ladder_test.go); what is proven here is that every step of that loop is accepted by the tool which has to execute it.
func TestLadderAgainstARealPortlessProxy(t *testing.T) {
	iso := integrationStateDir(t)
	bin := integrationBin(t)
	proxyPort := startIsolatedProxy(t, iso, bin)

	c := portless.New(
		portless.WithBinary(bin),
		portless.WithStateDir(iso),
		portless.WithTimeout(15*time.Second),
	)

	// Derived, never hardcoded: the rungs under test must be the ones a manifest with route_mode = "named_with_auto_fallback" really produces.
	cands, err := portless.RouteCandidates(manifest.URLGenByHostnameOrWorkspace, "vroom.ladder", "feature/x", "ladder")
	if err != nil {
		t.Fatalf("the ladder must derive: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("the ladder needs a stable rung and a fallback rung, got %v", cands)
	}
	stable, fallback := cands[0], cands[1]

	// Whoever starts first keeps the stable URL.
	alpha := startEchoBackend(t, 0, "alpha")
	first := c.Apply(stable, alpha, portless.Ownership{})
	if !first.Succeeded() {
		t.Fatalf("the first worktree must claim the stable name: %+v", first)
	}
	if !strings.Contains(first.Url, portless.Hostname(stable)) {
		t.Errorf("the stable URL must name the stable host, got %q", first.Url)
	}
	assertRouted(t, stable, proxyPort, "alpha")

	// The second worktree claims the same name and must be stopped BEFORE the write.
	beta := startEchoBackend(t, 0, "beta")
	second := c.Apply(stable, beta, portless.Ownership{})
	if second.Succeeded() {
		t.Fatalf("the stable name is held by another worktree and must not be taken over: %+v", second)
	}
	if second.Reason != portless.ReasonRouteConflict {
		t.Fatalf("only a pre-write conflict may advance the ladder, got reason %q", second.Reason)
	}
	if second.HeldPort != alpha {
		t.Errorf("the conflict must say who holds the stable name (port %d), got %d", alpha, second.HeldPort)
	}
	if second.Registered || second.Url != "" {
		t.Errorf("nothing was written, so nothing may be published: %+v", second)
	}
	// The fact only a real binary can show: `alias` upserts silently, so without this pre-check the first worktree's URL would already be pointing at somebody else's port.
	if port, found, lookupErr := c.Lookup(stable); lookupErr != nil || !found || port != alpha {
		t.Errorf("the stable route must still point at the first worktree: port=%d found=%v err=%v", port, found, lookupErr)
	}
	assertRouted(t, stable, proxyPort, "alpha")

	// The fallback rung: the second worktree gets its own address instead of a bare conflict.
	fb := c.Apply(fallback, beta, portless.Ownership{})
	if !fb.Succeeded() {
		t.Fatalf("the fallback rung must register: %+v", fb)
	}
	if !strings.Contains(fb.Url, portless.Hostname(fallback)) {
		t.Errorf("the fallback URL must name the fallback host, got %q", fb.Url)
	}
	assertRouted(t, fallback, proxyPort, "beta")
	// Both addresses answer at the same time, each on its own port: that is the whole promise of the fallback.
	assertRouted(t, stable, proxyPort, "alpha")

	// The first worktree stops: its route is revoked, so the stable name becomes claimable again.
	if !portless.Release(c, stable) {
		t.Fatal("stopping the first worktree must revoke its route")
	}
	owned := portless.Ownership{Owned: true, Port: beta}

	// The next start reconciles first, with the WHOLE candidate set: a persisted fallback that is still a candidate is a rung to reuse, not a branch rename, and deleting it would remove the route this start is about to claim.
	if warns := c.Reconcile(fallback, owned, cands...); len(warns) != 0 {
		t.Errorf("a persisted name that is still a candidate generates no warnings: %v", warns)
	}
	if _, found, lookupErr := c.Lookup(fallback); lookupErr != nil || !found {
		t.Fatalf("the fallback route must survive reconciliation: found=%v err=%v", found, lookupErr)
	}

	claimed := c.Apply(stable, beta, owned)
	if !claimed.Succeeded() {
		t.Fatalf("with the stable name free the upgrade must claim it: %+v", claimed)
	}
	// Retire the abandoned fallback: stop only revokes meta.RouteName, so a name left behind would block every future worktree falling back to it.
	if warns := c.Retire(fallback, owned); len(warns) != 0 {
		t.Errorf("the retirement must succeed against the real routes.json: %v", warns)
	}
	if _, found, _ := c.Lookup(fallback); found {
		t.Error("the abandoned fallback must be gone, or it would block the next worktree")
	}
	assertRouted(t, stable, proxyPort, "beta")
}

// assertRouted proves the URL a user would actually open answers with THIS backend: the status alone is 200 for either of them, so only the body tells the two worktrees apart. It waits because a fresh `alias` reaches the proxy asynchronously (fs.watch debounce when the watcher works, the 3s polling fallback when it does not), and a single immediate request can still see the previous backend.
func assertRouted(t *testing.T, name string, proxyPort int, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var status int
	var body string
	for {
		var err error
		status, body, err = routedBody(name, proxyPort)
		if err == nil && status == http.StatusOK && body == want {
			return
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("the proxy must serve %s from its own backend, got status=%d body=%q (want %q)",
		portless.Hostname(name), status, body, want)
}

// routedBody dials loopback and sends the Host header instead of resolving the name, exactly like ProbeOnce: *.localhost resolution is not guaranteed under a runner or a service manager, and the proxy routes on the header.
func routedBody(name string, proxyPort int) (int, string, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort))
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}

	host := portless.Hostname(name)
	resp, err := client.Get("http://" + host + "/")
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(data), nil
}
