package orchestrate

import (
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/scanner"

	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/state"
)

type recordingReleaser struct{ removed []string }

func (r *recordingReleaser) RemoveAbsent(name string) error {
	r.removed = append(r.removed, name)
	return nil
}

func installEngineReleaser(t *testing.T, rec *recordingReleaser) {
	t.Helper()
	t.Cleanup(func() { engineReleaseStub, engineReleaseStubInstalled = nil, false })
	engineReleaseStub = rec.RemoveAbsent
	engineReleaseStubInstalled = true
}

// The manager is inert on purpose: what is under test is the route removal, not the kill.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	return NewEngine(&noKillManager{}, state.NewStoreAt(t.TempDir()))
}

type noKillManager struct{}

func (*noKillManager) Stop(process.StopSpec) error { return nil }
func (*noKillManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}
func (*noKillManager) Evaluate(process.EvalSpec) process.Status { return process.StatusStopped }

func TestEngineStopProcessRemovesTheServiceRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installEngineReleaser(t, rec)

	e := newTestEngine(t)
	e.stopProcess("/tmp/proyecto", &state.Meta{
		Name: "p", Pid: 0, Pgid: 0, Port: 0,
		RouteName: "p-route", RoutePort: 4321, RouteOwned: true,
	})

	if len(rec.removed) != 1 || rec.removed[0] != "p-route" {
		t.Errorf("stopProcess must remove the service route, got %v", rec.removed)
	}
}

// Rollback goes through stopProcess, so aborting a session with registered routes removes them instead of orphaning them.
func TestEngineAbortCleanupRemovesRoutes(t *testing.T) {
	rec := &recordingReleaser{}
	installEngineReleaser(t, rec)

	e := newTestEngine(t)
	dir := t.TempDir()
	if err := e.store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 1, Port: 4321, State: state.StateRunning,
		RouteName: "ruta-de-la-sesion", RoutePort: 4321, RouteOwned: true,
	}); err != nil {
		t.Fatal(err)
	}

	e.abortAndCleanup([]string{dir})

	if len(rec.removed) != 1 || rec.removed[0] != "ruta-de-la-sesion" {
		t.Errorf("session rollback must remove the route, got %v", rec.removed)
	}
}

func TestEngineStopWithoutRouteDoesNotCallRelease(t *testing.T) {
	rec := &recordingReleaser{}
	installEngineReleaser(t, rec)

	e := newTestEngine(t)
	e.stopProcess("/tmp/proyecto", &state.Meta{Name: "p", Pid: 1, Port: 4321})

	if len(rec.removed) != 0 {
		t.Errorf("with no registered route nothing should be removed, got %v", rec.removed)
	}
}

// The state is reachable: resolveDynamicPort registers a route only when State==running && Port>0, but inherited RouteName/RoutePort stay persisted, so a service that once had a route and then opens no TCP port ends with Pid=0, Pgid=0, Port=0 and RouteName != "".
func TestStopServiceDeadServiceRemovesRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installEngineReleaser(t, rec)

	e := newTestEngine(t)
	dir := t.TempDir()
	if _, err := e.store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 0, Pgid: 0, Port: 0, State: state.StateRunning,
		RouteName: "ruta-heredada", RoutePort: 4321, RouteOwned: true,
	}); err != nil {
		t.Fatal(err)
	}

	e.stopService(scanner.Project{Path: dir, Configured: true, Manifest: &manifest.Manifest{Name: "p"}})

	if len(rec.removed) != 1 || rec.removed[0] != "ruta-heredada" {
		t.Errorf("stopService must remove the route of the already-dead service, got %v", rec.removed)
	}
	meta, err := e.store.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RouteOwned {
		t.Error("after removing the route, ownership must be revoked")
	}
}

// This branch used to call portless.Release(nil, ...) with the real client hardcoded, so no test could observe it.
func TestAbortCleanupDeadServiceRemovesRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installEngineReleaser(t, rec)

	e := newTestEngine(t)
	dir := t.TempDir()
	if _, err := e.store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 0, Pgid: 0, Port: 0, State: state.StateRunning,
		RouteName: "ruta-de-sesion", RoutePort: 4321, RouteOwned: true,
	}); err != nil {
		t.Fatal(err)
	}

	e.abortAndCleanup([]string{dir})

	if len(rec.removed) != 1 || rec.removed[0] != "ruta-de-sesion" {
		t.Errorf("abortAndCleanup must remove the route of the already-dead service, got %v", rec.removed)
	}
	meta, err := e.store.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RouteOwned {
		t.Error("after removing the route, ownership must be revoked")
	}
}

var _ portless.Releaser = (*recordingReleaser)(nil)

func TestEngineDoesNotRemoveAForeignRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installEngineReleaser(t, rec)

	e := newTestEngine(t)
	e.stopProcess("/tmp/proyecto", &state.Meta{
		Name: "p", Pid: 0, Port: 0,
		RouteName: "ajena", RoutePort: 4000, RouteOwned: false,
	})

	if len(rec.removed) != 0 {
		t.Errorf("stop cannot remove a route that was never ours, got %v", rec.removed)
	}
}
