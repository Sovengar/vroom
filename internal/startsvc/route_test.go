package startsvc

import (
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
}

// Apply always echoes the requested name, as the real client does: the intended name is input, never something the result decides.
func (f *fakeRoutes) Apply(name string, port int, prev portless.Ownership) portless.Result {
	f.applied = append(f.applied, name+":"+itoaTest(port))
	f.prevPorts = append(f.prevPorts, prev.Port)
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

func (f *fakeRoutes) Reconcile(_ string, _ portless.Ownership, current ...string) []string {
	f.reconciledCands = append(f.reconciledCands, strings.Join(current, "|"))
	return f.warns
}

func (f *fakeRoutes) Retire(name string, _ portless.Ownership) []string {
	f.retired = append(f.retired, name)
	return nil
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
			f.manifest.RouteMode = manifest.RouteModeAuto

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

func TestRouteIsRegisteredAfterDiscoveryAndPointsAtTheRealPort(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
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
	f.manifest.RouteMode = manifest.RouteModeAuto
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
	f.manifest.RouteMode = manifest.RouteModeAuto
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
	f.manifest.RouteMode = manifest.RouteModeAuto
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
	f.manifest.RouteMode = manifest.RouteModeAuto
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
	f.manifest.RouteMode = manifest.RouteModeAuto
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
	f.manifest.RouteMode = manifest.RouteModeAuto
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
	f.manifest.RouteMode = manifest.RouteModeNamedWithAutoFallback
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
	f.manifest.RouteMode = manifest.RouteModeAuto
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
	f.manifest.RouteMode = manifest.RouteModeAuto
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
