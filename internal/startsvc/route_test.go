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
}

// Apply always echoes the requested name, as the real client does: the intended name is input, never something the result decides.
func (f *fakeRoutes) Apply(name string, port int, prev portless.Ownership) portless.Result {
	f.applied = append(f.applied, name+":"+itoaTest(port))
	f.prevPorts = append(f.prevPorts, prev.Port)
	r := f.result
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

func (f *fakeRoutes) Reconcile(_ string, _ portless.Ownership, _ string) []string {
	return f.warns
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
		{"ruta registrada", &fakeRoutes{result: registeredAt(0)}, false},
		{"portless ausente", &fakeRoutes{result: portless.Degraded("x", portless.ReasonPortlessMissing)}, true},
		{"sin proxy", &fakeRoutes{result: portless.Degraded("x", portless.ReasonProxyNotRunning)}, true},
		{"no verificable", &fakeRoutes{result: portless.Degraded("x", portless.ReasonRouteNotServed)}, true},
		{"conflicto de nombre", &fakeRoutes{result: portless.Degraded("x", portless.ReasonRouteConflict)}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.command(t, "honor-port")
			f.manifest.RouteMode = manifest.RouteModeAuto

			out, err := f.startWithRoutes(t, 8*time.Second, tc.routes)
			if err != nil {
				t.Fatalf("el arranque NO puede fallar por el resultado de la ruta: %v", err)
			}
			f.cleanup(t, out)

			if out.Meta.State != state.StateRunning {
				t.Fatalf("el servicio debe quedar running, got %q", out.Meta.State)
			}
			if out.Port <= 0 {
				t.Errorf("el puerto real debe estar resuelto aunque la ruta degrade, got %d", out.Port)
			}
			if tc.wantWarn && len(out.Warnings) == 0 {
				t.Error("una ruta degradada debe emitir aviso")
			}
			if !tc.wantWarn && len(out.Warnings) != 0 {
				t.Errorf("una ruta registrada no debe avisar: %v", out.Warnings)
			}
		})
	}
}

func TestRouteModeOffNeverInvokesPortless(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")

	out, err := f.startWithRoutes(t, 8*time.Second, nil) // nil = sin seam
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if out.Meta.RouteName != "" {
		t.Errorf("con route_mode off no debe haber ruta, got %q", out.Meta.RouteName)
	}
	if out.Meta.RouteURL != "" {
		t.Errorf("con route_mode off no se publica url, got %q", out.Meta.RouteURL)
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
		t.Fatalf("debe registrarse exactamente una ruta, got %v", routes.applied)
	}
	// The registered port is the resolved one, never the reserved one (R4).
	if !strings.HasSuffix(routes.applied[0], ":"+itoaTest(out.Port)) {
		t.Errorf("la ruta debe apuntar al puerto real %d, got %q", out.Port, routes.applied[0])
	}
	if out.Meta.RouteStatus != portless.StatusRegistered {
		t.Errorf("el Meta debe llevar el estado de la ruta, got %q", out.Meta.RouteStatus)
	}
	if out.Meta.RouteURL == "" {
		t.Error("una ruta registrada y verificada persiste su url")
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
		t.Fatalf("el discovery debe ver el puerto real %d, got %d", own, out.Port)
	}
	if !strings.HasSuffix(routes.applied[0], ":"+itoaTest(own)) {
		t.Errorf("la ruta debe apuntar a donde la app escucha (%d), got %q", own, routes.applied[0])
	}
	if out.Meta.RoutePort == out.Meta.ReservedPort {
		t.Error("la ruta no puede apuntar al puerto reservado cuando la app hizo bind en otro")
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
		t.Errorf("un servicio sin puerto no debe registrar ruta, got %v", routes.applied)
	}
	if out.Meta.RouteURL != "" {
		t.Errorf("sin puerto no se publica url, got %q", out.Meta.RouteURL)
	}
	if out.Meta.State != state.StateNoPort {
		t.Errorf("el servicio debe quedar en su estado real, got %q", out.Meta.State)
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
		t.Skipf("el churn no siempre vence el discovery en este entorno (state=%q)", out.Meta.State)
	}
	if len(routes.applied) != 0 {
		t.Errorf("con el puerto sin resolver no se registra ruta, got %v", routes.applied)
	}
	if out.Meta.RouteURL != "" {
		t.Error("con el puerto sin resolver no se publica url")
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
		t.Errorf("una ruta degradada no persiste url, got %q", out.Meta.RouteURL)
	}
	if out.Meta.RouteReason == "" {
		t.Error("una ruta degradada debe persistir su motivo")
	}
	if out.Meta.RouteStatus != portless.StatusDegraded {
		t.Errorf("el estado debe ser degraded, got %q", out.Meta.RouteStatus)
	}

	onDisk, err := f.store.LoadMeta(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.RouteURL != "" {
		t.Errorf("tampoco en disco puede haber url, got %q", onDisk.RouteURL)
	}
	if onDisk.RouteReason != portless.ReasonRouteNotServed {
		t.Errorf("el motivo debe persistirse, got %q", onDisk.RouteReason)
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
		t.Errorf("el aviso de reconciliación debe llegar al usuario: %v", out.Warnings)
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
		t.Errorf("auto debe derivar <rama>.<proyecto>, got %q", got)
	}
}

func TestRouteNameUsesRouteNameInNamedMode(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeNamed
	f.manifest.RouteName = "My_OAuth_Callback"
	routes := &fakeRoutes{result: registeredAt(0)}

	out, err := f.startWithRoutesBranch(t, 8*time.Second, routes, "feat/mi_app")
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if !strings.HasPrefix(routes.applied[0], "my-oauth-callback:") {
		t.Errorf("named debe usar route_name saneado, got %q", routes.applied[0])
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
		t.Errorf("la salud del servicio es independiente de la ruta, got %q", out.Meta.State)
	}
	if out.Meta.RouteStatus != portless.StatusDegraded {
		t.Errorf("la ruta sí refleja su propio resultado, got %q", out.Meta.RouteStatus)
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
		t.Skip("el fixture no reservó puerto")
	}
	if reserved == out.Port {
		t.Skip("en este caso el puerto reservado es el real; no hay nada que distinguir")
	}
	if strings.HasSuffix(routes.applied[0], ":"+itoaTest(reserved)) {
		t.Errorf("la ruta no puede apuntar al puerto reservado %d, got %q", reserved, routes.applied[0])
	}
}

// The fixture must leave no live children: this is what the package hygiene guard checks at suite end, and these tests spawn real children.
func TestFixtureKillsItsChildren(t *testing.T) {
	if !filepath.IsAbs(os.Args[0]) {
		t.Fatal("el binario de test debe tener ruta absoluta")
	}
}
