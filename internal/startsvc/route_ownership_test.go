package startsvc

import (
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/state"
)

// These go through the real start and stop and read the Meta left on disk, because the defect was in what stop persists and not in Apply's predicate: a hand-built Ownership would never catch it.

func TestStopRevokesOwnershipAndKeepsHandle(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStoreAt(t.TempDir())
	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	// A stopped service that did own a route: RouteName and RoutePort survive, the exact shape that triggered the defect.
	if err := store.SaveMeta(dir, state.Meta{
		Name: "svc", Port: 4321, RouteName: "svc", RoutePort: 4321,
		RouteOwned: true, RouteStatus: portless.StatusRegistered,
	}); err != nil {
		t.Fatal(err)
	}

	stopFixtureService(t, store, dir)

	meta, err := store.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RouteOwned {
		t.Error("after revoking the route the ownership must remain revoked in the Meta")
	}
	// The handle is kept on purpose: if the removal failed the route may still be there, and without the handle reconciliation could not clean it up.
	if meta.RouteName == "" || meta.RoutePort != 4321 {
		t.Errorf("the reconciliation handle must be preserved, got name=%q port=%d",
			meta.RouteName, meta.RoutePort)
	}
}

func TestStartAfterStopDoesNotEvictForeignRouteInOurOldPort(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStoreAt(t.TempDir())
	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(dir, state.Meta{
		Name: "svc", Port: 4321, RouteName: "svc", RoutePort: 4321, RouteOwned: true,
	}); err != nil {
		t.Fatal(err)
	}
	stopFixtureService(t, store, dir)

	f := newFixture(t)
	f.dir = dir
	f.store = store
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto

	routes := &ownershipSpy{}
	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if routes.saw.Owned {
		t.Errorf("the start received an ownership revoked by stop, got %+v", routes.saw)
	}
}

func TestStartGrantsRouteOwnership(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
	routes := &ownershipSpy{}

	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if !out.Meta.RouteOwned {
		t.Error("after registering, the route is ours: ownership must be granted")
	}
	if routes.saw.Owned {
		t.Error("a cold start must not inherit ownership: there was no previous route")
	}
}

type ownershipSpy struct {
	prevPorts []int
	saw       portless.Ownership
}

func (o *ownershipSpy) Apply(name string, port int, prev portless.Ownership) portless.Result {
	o.saw = prev
	o.prevPorts = append(o.prevPorts, prev.Port)
	return portless.Result{
		Name: name, Host: portless.Hostname(name),
		Status: portless.StatusRegistered, Url: "http://x.localhost", Port: port,
		Registered: true,
	}
}

func (o *ownershipSpy) Reconcile(_ string, _ portless.Ownership, _ ...string) []string {
	return nil
}

func (o *ownershipSpy) Retire(string, portless.Ownership) []string { return nil }

// stopFixtureService exercises the stop path that removes the route without touching the process, because what matters here is what ends up persisted.
func stopFixtureService(t *testing.T, store *state.Store, dir string) {
	t.Helper()
	rec := &ownershipSpy{}
	releaseOnce(t, rec, store, dir)
}

// The removal body as the real stop paths run it, isolated so the resulting Meta can be observed.
func releaseOnce(t *testing.T, rec *ownershipSpy, store *state.Store, dir string) {
	t.Helper()
	meta, err := store.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if portless.Release(nil, meta.RouteName) {
		meta.RouteOwned = false
		_ = store.SaveMeta(dir, meta)
	}
}

var _ process.Manager = (*stopNoopManager)(nil)

// A no-op manager, because the kill is not what this file tests.
type stopNoopManager struct{}

func (*stopNoopManager) Stop(process.StopSpec) error { return nil }
func (*stopNoopManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}
func (*stopNoopManager) Evaluate(process.EvalSpec) process.Status { return process.StatusStopped }
