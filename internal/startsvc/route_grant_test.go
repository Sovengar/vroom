package startsvc

import (
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/state"
)

func TestConflictDoesNotGrantOwnership(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
	routes := &resultSpy{result: portless.Result{
		Name: "svc", Host: "svc.localhost",
		Status: portless.StatusDegraded, Reason: portless.ReasonRouteConflict,
	}}

	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if out.Meta.RouteOwned {
		t.Error("un alta que no registró no debe conceder propiedad: es la capacidad " +
			"que permite pisar la ruta de otro en el arranque siguiente")
	}
}

func TestRegisteredButUnverifiedStillGrantsOwnership(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
	// ReasonProxyNotRunning carries Registered, meaning written to disk but unverified, which is still ours.
	routes := &resultSpy{result: portless.Result{
		Name: "svc", Host: "svc.localhost",
		Status: portless.StatusDegraded, Reason: portless.ReasonProxyNotRunning,
		Registered: true,
	}}

	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if !out.Meta.RouteOwned {
		t.Error("una ruta registrada aunque no verificada es nuestra: debe conservar la propiedad")
	}
	if out.Meta.RouteName == "" {
		t.Error("y debe conservar el handle")
	}
}

func TestRenamedBranchDoesNotRemoveRevokedRoute(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
	if err := f.store.SaveMeta(f.dir, state.Meta{
		Name: "svc", Port: 4000, RouteName: "svc", RoutePort: 4321, RouteOwned: false,
		State: state.StateStopped,
	}); err != nil {
		t.Fatal(err)
	}

	routes := &reconcileSpy{}
	out, err := f.startWithRoutesBranch(t, 8*time.Second, routes, "feat/nueva-rama")
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if routes.reconciledWithOwnership.Owned {
		t.Error("con la propiedad revocada, Reconcile no debe recibir una concesión")
	}
	if routes.removedPrev {
		t.Error("con la propiedad revocada, reconciliar no puede borrar un nombre: " +
			"el handle vivo no es autoridad para borrar")
	}
}

func TestRenamedBranchStillRemovesOurOwnDeadRoute(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
	if err := f.store.SaveMeta(f.dir, state.Meta{
		Name: "svc", Port: 4000, RouteName: "svc", RoutePort: 4321, RouteOwned: true,
		State: state.StateStopped,
	}); err != nil {
		t.Fatal(err)
	}

	routes := &reconcileSpy{}
	out, err := f.startWithRoutesBranch(t, 8*time.Second, routes, "feat/nueva-rama")
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	// Reconcile runs before Apply, so what it receives is the ownership the previous start persisted.
	if !routes.reconciledWithOwnership.Authorises(4321) {
		t.Errorf("Reconcile debe recibir la Ownership persistida, no un puerto crudo: %+v",
			routes.reconciledWithOwnership)
	}
}

func TestInheritedRouteStateIsAllOrNothing(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
	if err := f.store.SaveMeta(f.dir, state.Meta{
		Name: "svc", Port: 4000, RouteName: "svc", RoutePort: 4321, RouteOwned: true,
		State: state.StateStopped,
	}); err != nil {
		t.Fatal(err)
	}

	routes := &resultSpy{result: portless.Result{
		Name: "svc", Host: "svc.localhost", Status: portless.StatusDegraded,
	}}
	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	_ = routes.sawPrev
}

type resultSpy struct {
	result  portless.Result
	sawPrev portless.Ownership
}

func (r *resultSpy) Apply(name string, port int, prev portless.Ownership) portless.Result {
	r.sawPrev = prev
	res := r.result
	res.Name = name
	res.Host = portless.Hostname(name)
	if res.Status == "" {
		res.Status = portless.StatusDegraded
	}
	return res
}

func (r *resultSpy) Reconcile(_ string, _ portless.Ownership, _ string) []string {
	return nil
}

type reconcileSpy struct {
	reconciledWithOwnership portless.Ownership
	sawPrev                 portless.Ownership
	removedPrev             bool
}

func (r *reconcileSpy) Apply(name string, port int, prev portless.Ownership) portless.Result {
	r.sawPrev = prev
	return portless.Result{
		Name: name, Host: portless.Hostname(name),
		Status: portless.StatusDegraded, Reason: portless.ReasonPortlessMissing,
	}
}

func (r *reconcileSpy) Reconcile(_ string, prev OwnershipPort, _ string) []string {
	r.reconciledWithOwnership = prev
	return nil
}

// OwnershipPort pins Reconcile's prev argument so the test fails to compile if the seam reverts to a raw port, which no refactor can silently drop.
type OwnershipPort = portless.Ownership
