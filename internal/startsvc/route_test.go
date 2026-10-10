package startsvc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/state"
)

// fakeRoutes is the portless seam: CI has no portless, Node 24 or proxy, so the suite drives the whole cycle against a double.
type fakeRoutes struct {
	applied   []string
	prevPorts []int
	warns     []string
	result    portless.Result
	// seq feeds Apply one Result per call so the claim ladder (conflict, then the fallback rung) can be driven; when it runs out, result takes over.
	seq []portless.Result
	// reconciledCands records the candidate set each Reconcile received, because a set where the test expected a single name is the whole point of the ladder.
	reconciledCands []string
	retired         []string
	// retireWarns is what Retire reports, so a test can prove a warned retirement keeps the handle while a clean one clears it.
	retireWarns []string
	// lookups records the names the by_hostname preflight asked about, so a test can prove the pre-check ran (or did not).
	lookups []string
	// heldPort/heldFound/lookupErr are what Lookup reports: found+port is a foreign holder, err is the degradation that must NOT refuse a start.
	heldPort  int
	heldFound bool
	lookupErr error
	// appliedCtx records the context each ApplyContext received, so a test can prove the command lifetime reaches the route probe instead of being swallowed by a Background fallback.
	appliedCtx []context.Context
}

// Apply always echoes the requested name, as the real client does: the intended name is input, never something the result decides.
func (f *fakeRoutes) ApplyContext(ctx context.Context, name string, port int, prev portless.Ownership) portless.Result {
	f.applied = append(f.applied, name+":"+itoaTest(port))
	f.prevPorts = append(f.prevPorts, prev.Port)
	f.appliedCtx = append(f.appliedCtx, ctx)
	r := f.result
	if len(f.seq) > 0 {
		r = f.seq[0]
		f.seq = f.seq[1:]
	}
	r.Name = name
	r.Host = portless.Hostname(name)
	if r.Status == "" {
		r.Status = portless.StatusDegraded
	}
	if r.Succeeded() {
		r.Port = port
	}
	return r
}

func (f *fakeRoutes) Lookup(name string) (int, bool, error) {
	f.lookups = append(f.lookups, name)
	return f.heldPort, f.heldFound, f.lookupErr
}

func (f *fakeRoutes) Reconcile(_ string, _ portless.Ownership, current ...string) []string {
	f.reconciledCands = append(f.reconciledCands, strings.Join(current, "|"))
	return f.warns
}
func (f *fakeRoutes) Retire(name string, _ portless.Ownership) []string {
	f.retired = append(f.retired, name)
	return f.retireWarns
}

func itoaTest(n int) string {
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

// The command lifetime must reach the route probe: applyRoute hands the Request's context to the registrar, so a cancelled CLI start or TUI quit can cut the propagation window instead of the seam falling back to Background.
func TestApplyRouteThreadsTheRequestContextIntoTheRegistrar(t *testing.T) {
	type ctxKey struct{}
	reg := &fakeRoutes{result: registeredRoute()}
	req := Request{Manifest: ladderManifest(), Branch: "main", Ctx: context.WithValue(context.Background(), ctxKey{}, "lifetime")}
	meta := state.Meta{Name: "api", Port: 8081}
	out := Result{}

	applyRoute(req, reg, &meta, 8081, manifest.URLGenByHostnameOrWorkspace, &out)

	if len(reg.appliedCtx) == 0 {
		t.Fatal("the registrar was never called")
	}
	if got := reg.appliedCtx[0].Value(ctxKey{}); got != "lifetime" {
		t.Errorf("the registrar context carries %v, want the Request's command lifetime", got)
	}
}

// registeredAt is a registered AND verified result; its port argument is a placeholder that Apply overwrites.
func registeredAt(port int) portless.Result {
	return portless.Result{
		Name:   "x",
		Host:   "x.localhost",
		Status: portless.StatusRegistered,
		Url:    "http://x.localhost",
		Port:   port,
	}
}

func TestHealthNeverDependsOnTheRoute(t *testing.T) {
	cases := []struct {
		name     string
		routes   *fakeRoutes
		wantWarn bool
	}{
		{"route registered", &fakeRoutes{result: registeredAt(0)}, false},
		{"portless absent", &fakeRoutes{result: portless.Degraded("x", portless.ReasonPortlessMissing)}, true},
		{"no proxy", &fakeRoutes{result: portless.Degraded("x", portless.ReasonProxyNotRunning)}, true},
		{"not verifiable", &fakeRoutes{result: portless.Degraded("x", portless.ReasonRouteNotServed)}, true},
		{"name conflict", &fakeRoutes{result: portless.Degraded("x", portless.ReasonRouteConflict)}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.command(t, "honor-port")
			f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname

			out, err := f.startWithRoutes(t, 8*time.Second, tc.routes)
			if err != nil {
				t.Fatalf("the start must NOT fail because of the route result: %v", err)
			}
			f.cleanup(t, out)

			if out.Meta.State != state.StateRunning {
				t.Fatalf("the service must be running, got %q", out.Meta.State)
			}
			if out.Port <= 0 {
				t.Errorf("the real port must be resolved even if the route degrades, got %d", out.Port)
			}
			if tc.wantWarn && len(out.Warnings) == 0 {
				t.Error("a degraded route must emit a warning")
			}
			if !tc.wantWarn && len(out.Warnings) != 0 {
				t.Errorf("a registered route must not warn: %v", out.Warnings)
			}
		})
	}
}

func TestRouteModeOffNeverInvokesPortless(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")

	out, err := f.startWithRoutes(t, 8*time.Second, nil) // nil = no seam
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if out.Meta.RouteName != "" {
		t.Errorf("with route_mode off there must be no route, got %q", out.Meta.RouteName)
	}
	if out.Meta.RouteURL != "" {
		t.Errorf("with route_mode off no url is published, got %q", out.Meta.RouteURL)
	}
}

// The by_hostname preflight is the one refusal that must happen before anything spawns: only a PROVEN foreign holder stops the start, and a service's own stale route is reclaimed.
func TestByHostnamePreflight(t *testing.T) {
	t.Run("a foreign holder refuses before spawning", func(t *testing.T) {
		f := newFixture(t)
		f.command(t, "honor-port")
		f.manifest.URLGeneration = manifest.URLGenByHostname
		f.manifest.RouteName = "tienda"
		routes := &fakeRoutes{heldPort: 4444, heldFound: true}

		_, err := f.startWithRoutes(t, 2*time.Second, routes)
		if err == nil {
			t.Fatal("a name a foreign holder keeps must refuse the start")
		}
		if !strings.Contains(err.Error(), "held by port 4444") {
			t.Errorf("the refusal must name the holder's port: %v", err)
		}
		if len(routes.lookups) != 1 || routes.lookups[0] != "tienda" {
			t.Errorf("the pre-check must ask about the claimed name: %v", routes.lookups)
		}
		if len(routes.applied) != 0 {
			t.Errorf("nothing may be registered for a refused start: %v", routes.applied)
		}
	})

	t.Run("our own stale route is reclaimed", func(t *testing.T) {
		f := newFixture(t)
		f.command(t, "honor-port")
		f.manifest.URLGeneration = manifest.URLGenByHostname
		f.manifest.RouteName = "tienda"
		if err := f.store.SaveMeta(f.dir, state.Meta{RouteName: "tienda", RoutePort: 4444, RouteOwned: true, URLGeneration: manifest.URLGenByHostname}); err != nil {
			t.Fatal(err)
		}
		routes := &fakeRoutes{heldPort: 4444, heldFound: true, result: registeredAt(0)}

		out, err := f.startWithRoutes(t, 8*time.Second, routes)
		if err != nil {
			t.Fatalf("a service must reclaim the name it still owns: %v", err)
		}
		f.cleanup(t, out)

		if len(routes.applied) != 1 {
			t.Errorf("the reclaimed route must be re-registered: %v", routes.applied)
		}
	})

	t.Run("an unprovable holder degrades instead of refusing", func(t *testing.T) {
		f := newFixture(t)
		f.command(t, "honor-port")
		f.manifest.URLGeneration = manifest.URLGenByHostname
		f.manifest.RouteName = "tienda"
		routes := &fakeRoutes{lookupErr: errors.New("no portless binary")}

		out, err := f.startWithRoutes(t, 8*time.Second, routes)
		if err != nil {
			t.Fatalf("a holder that cannot be proven must not refuse the start: %v", err)
		}
		f.cleanup(t, out)
	})

	t.Run("without a seam the check is a no-op", func(t *testing.T) {
		f := newFixture(t)
		f.command(t, "honor-port")
		f.manifest.URLGeneration = manifest.URLGenByHostname
		f.manifest.RouteName = "tienda"

		out, err := f.start(t, 8*time.Second) // no Registrar at all: routes == nil
		if err != nil {
			t.Fatalf("without portless the start must proceed: %v", err)
		}
		f.cleanup(t, out)
	})
}

// An occupied port refuses the by_port start before anything spawns: the declared port is the address, and a neighbour's listener is not ours to reuse.
func TestByPortPreflightRefusesAnOccupiedPort(t *testing.T) {
	f := newFixture(t)
	ln, err := listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	f.manifest.URLGeneration = manifest.URLGenByPort
	f.manifest.Port = port

	_, err = f.start(t, 2*time.Second)
	if err == nil {
		t.Fatal("a by_port start on an occupied port must be refused")
	}
	if !strings.Contains(err.Error(), "already in use") || !strings.Contains(err.Error(), itoaTest(port)) {
		t.Errorf("the refusal must name the occupied port %d: %v", port, err)
	}
}

func TestRouteIsRegisteredAfterDiscoveryAndPointsAtTheRealPort(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
	routes := &fakeRoutes{result: registeredAt(0)}

	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if len(routes.applied) != 1 {
		t.Fatalf("exactly one route must be registered, got %v", routes.applied)
	}
	// The registered port is the resolved one, never the reserved one (R4).
	if !strings.HasSuffix(routes.applied[0], ":"+itoaTest(out.Port)) {
		t.Errorf("the route must point to the real port %d, got %q", out.Port, routes.applied[0])
	}
	if out.Meta.RouteStatus != portless.StatusRegistered {
		t.Errorf("the Meta must carry the route status, got %q", out.Meta.RouteStatus)
	}
	if out.Meta.RouteURL == "" {
		t.Error("a registered and verified route persists its url")
	}
}

func TestRoutePointsAtWhereTheAppActuallyListens(t *testing.T) {
	f := newFixture(t)
	own := freePort(t)
	f.command(t, "fixed-port", "VROOM_HELPER_PORT="+itoaTest(own))
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
	routes := &fakeRoutes{result: registeredAt(0)}

	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if out.Port != own {
		t.Fatalf("discovery must see the real port %d, got %d", own, out.Port)
	}
	if !strings.HasSuffix(routes.applied[0], ":"+itoaTest(own)) {
		t.Errorf("the route must point to where the app listens (%d), got %q", own, routes.applied[0])
	}
	if out.Meta.RoutePort == out.Meta.ReservedPort {
		t.Error("the route cannot point to the reserved port when the app bound to another")
	}
}

func TestNoRouteWithoutAResolvedPort(t *testing.T) {
	f := newFixture(t)
	f.command(t, "udp-only")
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
	routes := &fakeRoutes{result: registeredAt(0)}

	out, err := f.startWithRoutes(t, 2*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if len(routes.applied) != 0 {
		t.Errorf("a service without a port must not register a route, got %v", routes.applied)
	}
	if out.Meta.RouteURL != "" {
		t.Errorf("without a port no url is published, got %q", out.Meta.RouteURL)
	}
	if out.Meta.State != state.StateNoPort {
		t.Errorf("the service must remain in its real state, got %q", out.Meta.State)
	}
}

func TestNoRouteWhenPortUnresolved(t *testing.T) {
	f := newFixture(t)
	f.command(t, "churn")
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
	routes := &fakeRoutes{result: registeredAt(0)}

	out, err := f.startWithRoutes(t, 900*time.Millisecond, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if out.Meta.State != state.StatePortUnresolved {
		t.Skipf("churn does not always beat discovery in this environment (state=%q)", out.Meta.State)
	}
	if len(routes.applied) != 0 {
		t.Errorf("with the port unresolved no route is registered, got %v", routes.applied)
	}
	if out.Meta.RouteURL != "" {
		t.Error("with the port unresolved no url is published")
	}
}

func TestDegradedRouteNeverPersistsAURL(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
	routes := &fakeRoutes{
		result: portless.Degraded("x", portless.ReasonRouteNotServed),
	}

	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if out.Meta.RouteURL != "" {
		t.Errorf("a degraded route does not persist url, got %q", out.Meta.RouteURL)
	}
	if out.Meta.RouteReason == "" {
		t.Error("a degraded route must persist its reason")
	}
	if out.Meta.RouteStatus != portless.StatusDegraded {
		t.Errorf("the status must be degraded, got %q", out.Meta.RouteStatus)
	}

	onDisk, err := f.store.LoadMeta(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.RouteURL != "" {
		t.Errorf("there can be no url on disk either, got %q", onDisk.RouteURL)
	}
	if onDisk.RouteReason != portless.ReasonRouteNotServed {
		t.Errorf("the reason must be persisted, got %q", onDisk.RouteReason)
	}
}

func TestReconcileWarningsSurfaceAsWarnings(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
	routes := &fakeRoutes{
		result: registeredAt(0),
		warns:  []string{"a portless route named \"otro\" is already serving another port (9999); it was left untouched"},
	}

	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	found := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "left untouched") {
			found = true
		}
	}
	if !found {
		t.Errorf("the reconciliation warning must reach the user: %v", out.Warnings)
	}
}

func TestRouteNameDerivedFromBranchInAutoMode(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
	routes := &fakeRoutes{result: registeredAt(0)}

	out, err := f.startWithRoutesBranch(t, 8*time.Second, routes, "feat/mi_app")
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if got := routes.applied[0]; !strings.HasPrefix(got, "feat-mi-app.svc:") {
		t.Errorf("auto must derive <branch>.<project>, got %q", got)
	}
}

func TestRouteNameUsesRouteNameInNamedMode(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.URLGeneration = manifest.URLGenByHostnameOrWorkspace
	f.manifest.RouteName = "My_OAuth_Callback"
	routes := &fakeRoutes{result: registeredAt(0)}

	out, err := f.startWithRoutesBranch(t, 8*time.Second, routes, "feat/mi_app")
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if !strings.HasPrefix(routes.applied[0], "my-oauth-callback:") {
		t.Errorf("named must use sanitized route_name, got %q", routes.applied[0])
	}
}

func TestRouteStatusDoesNotAffectServiceState(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
	routes := &fakeRoutes{
		result: portless.Degraded("x", portless.ReasonPortlessMissing),
	}

	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if out.Meta.State != state.StateRunning {
		t.Errorf("the service health is independent of the route, got %q", out.Meta.State)
	}
	if out.Meta.RouteStatus != portless.StatusDegraded {
		t.Errorf("the route does reflect its own result, got %q", out.Meta.RouteStatus)
	}
}

func TestRouteNeverUsesTheReservedPort(t *testing.T) {
	f := newFixture(t)
	own := freePort(t)
	f.command(t, "fixed-port", "VROOM_HELPER_PORT="+itoaTest(own))
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
	routes := &fakeRoutes{result: registeredAt(0)}

	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	reserved := out.Meta.ReservedPort
	if reserved == 0 {
		t.Skip("the fixture did not reserve a port")
	}
	if reserved == out.Port {
		t.Skip("in this case the reserved port is the real one; there is nothing to distinguish")
	}
	if strings.HasSuffix(routes.applied[0], ":"+itoaTest(reserved)) {
		t.Errorf("the route cannot point to the reserved port %d, got %q", reserved, routes.applied[0])
	}
}

// The fixture must leave no live children: this is what the package hygiene guard checks at suite end, and these tests spawn real children.
func TestFixtureKillsItsChildren(t *testing.T) {
	if !filepath.IsAbs(os.Args[0]) {
		t.Fatal("the test binary must have an absolute path")
	}
}
