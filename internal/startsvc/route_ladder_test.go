package startsvc

import (
	"strings"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/state"
)

// These tests drive applyRoute directly against the seam: the ladder is a decision about names and ownership, and a real process would only slow it down.

func ladderManifest() *manifest.Manifest {
	return &manifest.Manifest{
		Name: "api", Port: 8081,
		URLGeneration: manifest.URLGenByHostnameOrWorkspace, RouteName: "api",
	}
}

func conflictOn(port int) portless.Result {
	return portless.Result{
		Status:   portless.StatusDegraded,
		Reason:   portless.ReasonRouteConflict,
		HeldPort: port,
	}
}

func registeredRoute() portless.Result {
	return portless.Result{
		Status:     portless.StatusRegistered,
		Url:        "http://api.localhost",
		Registered: true,
	}
}

// The core promise: the second worktree to start used to get a bare route_conflict and no URL at all; now it claims the branch-derived name and the warning says who holds the stable one.
func TestLadderFallsBackWhenTheStableNameIsHeld(t *testing.T) {
	reg := &fakeRoutes{seq: []portless.Result{
		conflictOn(4001),
		{Status: portless.StatusRegistered, Url: "http://feature-x.api.localhost", Registered: true},
	}}
	req := Request{Manifest: ladderManifest(), Branch: "feature/x"}
	meta := state.Meta{Name: "api", Port: 8081}
	out := Result{}

	applyRoute(req, reg, &meta, 8081, manifest.URLGenByHostnameOrWorkspace, &out)

	if len(reg.applied) != 2 {
		t.Fatalf("the ladder must try both rungs, applied = %v", reg.applied)
	}
	if !strings.HasPrefix(reg.applied[0], "api:") {
		t.Errorf("the stable name must be tried first, got %q", reg.applied[0])
	}
	if !strings.HasPrefix(reg.applied[1], "feature-x.api:") {
		t.Errorf("the fallback must be the branch-derived name, got %q", reg.applied[1])
	}
	// Reconcile sees the whole ladder: showing it only the primary would make it read the persisted fallback as a branch rename and delete it.
	if len(reg.reconciledCands) != 1 || reg.reconciledCands[0] != "api|feature-x.api" {
		t.Errorf("Reconcile must receive both candidates, got %v", reg.reconciledCands)
	}
	joined := strings.Join(out.Warnings, " | ")
	if !strings.Contains(joined, "taken by port 4001") {
		t.Errorf("the warning must say WHO holds the stable name: %v", out.Warnings)
	}
	if !strings.Contains(joined, "feature-x.api.localhost") {
		t.Errorf("the warning must name the URL actually published: %v", out.Warnings)
	}
	if meta.RouteName != "feature-x.api" {
		t.Errorf("the handle must follow the name actually claimed, got %q", meta.RouteName)
	}
	if !meta.RouteOwned {
		t.Error("the fallback route is ours: without ownership stop could never revoke it")
	}
	if meta.RouteURL == "" {
		t.Error("the fallback URL is verified like any other and must be published")
	}
	if len(reg.retired) != 0 {
		t.Errorf("there was no previous handle, so nothing to retire: %v", reg.retired)
	}
}

// When the fallback is taken too, the ladder ends where the old behaviour did: no URL, the first route intact, the service healthy on its own port.
func TestLadderDegradesWhenBothRungsAreHeld(t *testing.T) {
	reg := &fakeRoutes{seq: []portless.Result{conflictOn(4001), conflictOn(4002)}}
	req := Request{Manifest: ladderManifest(), Branch: "feature/x"}
	meta := state.Meta{Name: "api", Port: 8081}
	out := Result{}

	applyRoute(req, reg, &meta, 8081, manifest.URLGenByHostnameOrWorkspace, &out)

	if len(reg.applied) != 2 {
		t.Fatalf("both rungs must be attempted before degrading, applied = %v", reg.applied)
	}
	if meta.RouteURL != "" {
		t.Errorf("a route nobody verified is never published, got %q", meta.RouteURL)
	}
	if meta.RouteOwned {
		t.Error("nothing was registered, so no ownership may be minted")
	}
	// prevName was empty, so the handle records the last attempted name — the same contract single-rung conflicts always had.
	if meta.RouteName != "feature-x.api" {
		t.Errorf("RouteName = %q, want the last attempted candidate", meta.RouteName)
	}
	joined := strings.Join(out.Warnings, " | ")
	if !strings.Contains(joined, "taken by port 4001") || !strings.Contains(joined, "already taken by another port") {
		t.Errorf("both conflicts must reach the user: %v", out.Warnings)
	}
}

// The upgrade path: the primary became free, so this worktree claims it — and must drop its old fallback route, or stop will never revoke it (the handle moved) and it would block the next same-branch worktree.
func TestLadderUpgradeRetiresTheOldFallbackRoute(t *testing.T) {
	reg := &fakeRoutes{result: registeredRoute()}
	req := Request{Manifest: ladderManifest(), Branch: "feature/x"}
	meta := state.Meta{
		Name: "api", Port: 8081,
		RouteName: "feature-x.api", RoutePort: 4321, RouteOwned: true,
	}
	out := Result{}

	applyRoute(req, reg, &meta, 8081, manifest.URLGenByHostnameOrWorkspace, &out)

	if len(reg.retired) != 1 || reg.retired[0] != "feature-x.api" {
		t.Fatalf("the abandoned fallback route must be retired, got %v", reg.retired)
	}
	if meta.RouteName != "api" {
		t.Errorf("the handle must move to the stable name, got %q", meta.RouteName)
	}
	if !meta.RouteOwned {
		t.Error("the new registration is ours")
	}
}

// Without an unrevoked lease the old name may now belong to another worktree, so retirement must not run at all.
func TestLadderUpgradeWithoutOwnershipRetiresNothing(t *testing.T) {
	reg := &fakeRoutes{result: registeredRoute()}
	req := Request{Manifest: ladderManifest(), Branch: "feature/x"}
	meta := state.Meta{
		Name: "api", Port: 8081,
		RouteName: "feature-x.api", RoutePort: 4321, RouteOwned: false,
	}
	out := Result{}

	applyRoute(req, reg, &meta, 8081, manifest.URLGenByHostnameOrWorkspace, &out)

	if len(reg.retired) != 0 {
		t.Errorf("a revoked lease is not authority to delete: %v", reg.retired)
	}
	if meta.RouteName != "api" {
		t.Errorf("the registered name still takes the handle, got %q", meta.RouteName)
	}
}

// A conflict whose holder port could not be read still warns — it just cannot say who holds the stable name.
func TestLadderFallbackWarningWithoutAKnownHolder(t *testing.T) {
	reg := &fakeRoutes{seq: []portless.Result{
		portless.Degraded("api", portless.ReasonRouteConflict),
		{Status: portless.StatusRegistered, Url: "http://feature-x.api.localhost", Registered: true},
	}}
	req := Request{Manifest: ladderManifest(), Branch: "feature/x"}
	meta := state.Meta{Name: "api", Port: 8081}
	out := Result{}

	applyRoute(req, reg, &meta, 8081, manifest.URLGenByHostnameOrWorkspace, &out)

	joined := strings.Join(out.Warnings, " | ")
	if !strings.Contains(joined, "taken by another service") {
		t.Errorf("the fallback must be announced even without a holder port: %v", out.Warnings)
	}
	if !strings.Contains(joined, "feature-x.api.localhost") {
		t.Errorf("the warning must still name the URL actually published: %v", out.Warnings)
	}
}

// Nothing claimed and the old name is still a candidate: the handle must stay on it, because it names a route this service may still own — moving it to a merely attempted name would orphan a real one.
func TestLadderFailureKeepsTheHandleOnTheNameWeMayStillOwn(t *testing.T) {
	reg := &fakeRoutes{seq: []portless.Result{conflictOn(4001), conflictOn(4002)}}
	req := Request{Manifest: ladderManifest(), Branch: "feature/x"}
	meta := state.Meta{
		Name: "api", Port: 8081,
		RouteName: "feature-x.api", RoutePort: 4321, RouteOwned: true,
	}
	out := Result{}

	applyRoute(req, reg, &meta, 8081, manifest.URLGenByHostnameOrWorkspace, &out)

	if meta.RouteName != "feature-x.api" {
		t.Errorf("the handle must stay on the name we may still own, got %q", meta.RouteName)
	}
	if meta.RouteOwned {
		t.Error("ownership is granted by registration only, and nothing was registered")
	}
	if len(reg.retired) != 0 {
		t.Errorf("with nothing registered the old route must be kept, not retired: %v", reg.retired)
	}
	if meta.RouteURL != "" {
		t.Errorf("no url may be published, got %q", meta.RouteURL)
	}
}
