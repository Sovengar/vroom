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
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
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
		t.Error("a registration that did not register must not grant ownership: it is the capability " +
			"that allows overwriting another's route on the next start")
	}
}

func TestRegisteredButUnverifiedStillGrantsOwnership(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
	// ReasonsProxyNotRunning carries Registered, meaning written to disk but unverified, which is still ours.
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
		t.Error("a registered route even if unverified is ours: it must preserve ownership")
	}
	if out.Meta.RouteName == "" {
		t.Error("and it must preserve the handle")
	}
}

func TestRenamedBranchDoesNotRemoveRevokedRoute(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
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
		t.Error("with ownership revoked, Reconcile must not receive a grant")
	}
	if routes.removedPrev {
		t.Error("with ownership revoked, reconciling cannot delete a name: " +
			"a live handle is not authority to delete")
	}
}

func TestRenamedBranchStillRemovesOurOwnDeadRoute(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
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
		t.Errorf("Reconcile must receive the persisted Ownership, not a raw port: %+v",
			routes.reconciledWithOwnership)
	}
}

func TestInheritedRouteStateIsAllOrNothing(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.URLGeneration = manifest.URLGenByWorkspaceHostname
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

func (r *resultSpy) Lookup(string) (int, bool, error) { return 0, false, nil }

func (r *resultSpy) Reconcile(_ string, _ portless.Ownership, _ ...string) []string {
	return nil
}

func (r *resultSpy) Retire(string, portless.Ownership) []string { return nil }

type reconcileSpy struct {
	reconciledWithOwnership portless.Ownership
	sawPrev                 portless.Ownership
	removedPrev             bool
}

func (r *reconcileSpy) Lookup(string) (int, bool, error) { return 0, false, nil }

func (r *reconcileSpy) Apply(name string, port int, prev portless.Ownership) portless.Result {
	r.sawPrev = prev
	return portless.Result{
		Name: name, Host: portless.Hostname(name),
		Status: portless.StatusDegraded, Reason: portless.ReasonPortlessMissing,
	}
}

func (r *reconcileSpy) Reconcile(_ string, prev OwnershipPort, _ ...string) []string {
	r.reconciledWithOwnership = prev
	return nil
}

func (r *reconcileSpy) Retire(string, portless.Ownership) []string { return nil }

// OwnershipPort pins Reconcile's prev argument so the test fails to compile if the seam reverts to a raw port, which no refactor can silently drop.
type OwnershipPort = portless.Ownership
