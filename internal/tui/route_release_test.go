package tui

import (
	"testing"

	"vroom/internal/state"
)

// The double only records removals: the real releaser talks to the portless binary, which these tests never run.
type recordingReleaser struct{ removed []string }

func (r *recordingReleaser) RemoveAbsent(name string) error {
	r.removed = append(r.removed, name)
	return nil
}

func installRouteStub(t *testing.T, rec *recordingReleaser) {
	t.Helper()
	t.Cleanup(func() {
		tuiReleaseStub = nil
		routeStubInstalled = false
	})
	tuiReleaseStub = rec.RemoveAbsent
	routeStubInstalled = true
}

func TestStopRemovesTheServiceRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installRouteStub(t, rec)

	dir := t.TempDir()
	store := state.NewStoreAt(t.TempDir())
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 0, Pgid: 0, Port: 0,
		State: state.StateRunning, RouteName: "p-route", RoutePort: 4321, RouteOwned: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}

	msg := stopCmd(store, &stubManager{}, dir, "")()

	if len(rec.removed) != 1 || rec.removed[0] != "p-route" {
		t.Errorf("stop must remove the service route, got %v", rec.removed)
	}
	if m, ok := msg.(stoppedMsg); !ok || m.err != nil {
		t.Errorf("stopping must not fail due to route removal: %#v", msg)
	}

	// Ownership must be revoked too, or the next start could stomp a route another worktree has since claimed.
	meta, err := store.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RouteOwned {
		t.Error("after removing the route, ownership must be revoked in Meta")
	}
	if meta.RouteName == "" {
		t.Error("the reconciliation handle must be preserved even when ownership is revoked")
	}
}

// An empty RouteName means there was never a route contract, and releasing that empty name would be noise.
func TestStopWithoutRouteDoesNotCallRelease(t *testing.T) {
	rec := &recordingReleaser{}
	installRouteStub(t, rec)

	dir := t.TempDir()
	store := state.NewStoreAt(t.TempDir())
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", State: state.StateRunning,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}

	stopCmd(store, &stubManager{}, dir, "")()

	if len(rec.removed) != 0 {
		t.Errorf("with no registered route nothing should be removed, got %v", rec.removed)
	}
}

// A live handle without ownership is not authority to delete, so a foreign route holding the name survives.
func TestStopDoesNotRemoveAForeignRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installRouteStub(t, rec)

	dir := t.TempDir()
	store := state.NewStoreAt(t.TempDir())
	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 0, State: state.StateRunning,
		RouteName: "ajena", RoutePort: 4000, RouteOwned: false,
	}); err != nil {
		t.Fatal(err)
	}

	stopCmd(store, &stubManager{}, dir, "")()

	if len(rec.removed) != 0 {
		t.Errorf("stop cannot remove a route that was never ours, got %v", rec.removed)
	}
}
