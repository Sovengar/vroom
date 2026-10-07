package cli

import (
	"testing"

	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/state"
)

// CLI stop must RELEASE the route: deleting the three Release call sites left the suite green, so ADR decision 13 ("release on all three paths") was verified by nothing, and a service already DEAD at stop time also leaked a route because the release sat inside the Pid > 0 guard.

// Records what it was asked to release.
type recordingReleaser struct{ removed []string }

func (r *recordingReleaser) RemoveAbsent(name string) error {
	r.removed = append(r.removed, name)
	return nil
}

// installCLIReleaser points the CLI release at the test double.
func installCLIReleaser(t *testing.T, rec *recordingReleaser) {
	t.Helper()
	t.Cleanup(func() { cliReleaseStub, cliReleaseStubInstalled = nil, false })
	cliReleaseStub = rec.RemoveAbsent
	cliReleaseStubInstalled = true
}

// A service with a live process releases its route on stop.
func TestCLIStopRemovesTheServiceRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)

	dir, store := stopWithMeta(t, state.Meta{
		Name: "p", Pid: 424242, Port: 4321, State: state.StateRunning,
		RouteName: "p-route", RoutePort: 4321, RouteOwned: true,
	})

	if len(rec.removed) != 1 || rec.removed[0] != "p-route" {
		t.Errorf("stop must remove the service route, got %v", rec.removed)
	}

	// Ownership is revoked too: otherwise the Meta still claims a released route, which is exactly what let vroom overwrite someone else's.
	meta, err := store.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RouteOwned {
		t.Error("after removing the route the ownership must be revoked in the Meta")
	}
	if meta.RouteName == "" {
		t.Error("the reconciliation handle must be preserved even if ownership is revoked")
	}
}

// With the release inside the process guard an already DEAD service (Pid 0) kept its route forever, and only the next boot's reconciliation could clean it, which exists for orphans and is no substitute for releasing your own.
func TestCLIStopRemovesRouteEvenWhenAlreadyDead(t *testing.T) {
	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)

	_, _ = stopWithMeta(t, state.Meta{
		Name: "p", Pid: 0, Pgid: 0, Port: 0, State: state.StateRunning,
		RouteName: "ruta-huerfana", RoutePort: 4321, RouteOwned: true,
	})

	if len(rec.removed) != 1 || rec.removed[0] != "ruta-huerfana" {
		t.Errorf("an already dead service must also remove its route, got %v", rec.removed)
	}
}

// An empty RouteName is the signal that there was no route contract, so calling release with an empty name would be noise.
func TestCLIStopWithoutRouteDoesNotCallRelease(t *testing.T) {
	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)

	_, _ = stopWithMeta(t, state.Meta{Name: "p", Pid: 7, Port: 4321})

	if len(rec.removed) != 0 {
		t.Errorf("without a registered route nothing must be removed, got %v", rec.removed)
	}
}

// Calls the production stopCleanup that cmdStop itself runs, not a reimplementation: a test that replicates the logic passes even after the logic is deleted, which is the hole the first version left.
func stopWithMeta(t *testing.T, meta state.Meta) (string, *state.Store) {
	t.Helper()
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(dir, meta); err != nil {
		t.Fatal(err)
	}
	if err := stopCleanup(store, &noKillManager{}, dir); err != nil {
		t.Fatalf("stopCleanup: %v", err)
	}
	return dir, store
}

// Accepts any Stop without touching anything: what is under test is the route release, not the kill, which the process tests already cover.
type noKillManager struct{}

func (*noKillManager) Stop(process.StopSpec) error { return nil }
func (*noKillManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}
func (*noKillManager) Evaluate(process.EvalSpec) process.Status { return process.StatusStopped }

// The portless seam must satisfy Releaser, which is what lets the release be observed without a real binary.
var _ portless.Releaser = (*recordingReleaser)(nil)

// A registration that COLLIDED with a foreign route leaves the handle set but ownership revoked, and stop cannot treat that handle as delete authority: the name holds someone else's route, and deleting it is the damage this whole design exists to prevent (reproduced against real portless before this test was written).
func TestCLIStopDoesNotRemoveAForeignRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)

	_, _ = stopWithMeta(t, state.Meta{
		Name: "p", Pid: 424242, Port: 4321, State: state.StateRunning,
		RouteName: "ajena", RoutePort: 4000, RouteOwned: false,
	})

	if len(rec.removed) != 0 {
		t.Errorf("stop cannot remove a route that was never ours, got %v", rec.removed)
	}
}
