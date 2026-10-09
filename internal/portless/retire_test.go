package portless

import (
	"errors"
	"strings"
	"testing"
)

// The claim ladder abandons names: stop only revokes meta.RouteName, which now points at the NEW name, so a retired route that was never removed would outlive the handle, block the next worktree falling back to it, and never be reconciled again.

func TestRetireRemovesTheRouteThisServiceAbandoned(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("feature-x.api")] = 4321

	warns := c.Retire("feature-x.api", Ownership{Owned: true, Port: 4321})

	if len(warns) != 0 {
		t.Errorf("retiring our own route is not a warning-worthy event: %v", warns)
	}
	if _, still := f.routes[Hostname("feature-x.api")]; still {
		t.Error("the abandoned route must be removed: nothing else can, the handle already moved")
	}
}

func TestRetireIsSilentWhenThereIsNothingToRemove(t *testing.T) {
	f := newFake()
	c := f.client(t)

	warns := c.Retire("nunca-existio", Ownership{Owned: true, Port: 4321})

	if len(warns) != 0 {
		t.Errorf("a name that is already free is not worth a warning: %v", warns)
	}
	if len(f.removedNames) != 0 {
		t.Errorf("and no removal should even be attempted: %v", f.removedNames)
	}
}

// Fail-closed survives the stronger removal: another actor upserting the name (M8) leaves a route pointing at a port we never persisted, and deleting it would be exactly the harm this whole design avoids.
func TestRetireNeverDeletesARouteAnotherActorOverwrote(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("feature-x.api")] = 9999

	warns := c.Retire("feature-x.api", Ownership{Owned: true, Port: 4321})

	if _, still := f.routes[Hostname("feature-x.api")]; !still {
		t.Fatal("a route serving another port is not provably ours and must be kept")
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "another port") {
		t.Errorf("it must warn instead of deleting silently: %v", warns)
	}
}

func TestRetireWithoutOwnershipDeletesNothing(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("feature-x.api")] = 4321

	warns := c.Retire("feature-x.api", Ownership{Owned: false, Port: 4321})

	if _, still := f.routes[Hostname("feature-x.api")]; !still {
		t.Fatal("a revoked lease is not authority to delete: the name may now belong to another worktree")
	}
	if len(f.removedNames) != 0 {
		t.Errorf("no removal may be attempted without ownership: %v", f.removedNames)
	}
	if len(warns) != 0 {
		t.Errorf("leaving another's route alone is the normal outcome, not a warning: %v", warns)
	}
}

// A bin-less client is the "portless not installed" degradation; Retire must degrade with it instead of reaching for an executable.
func TestRetireWithoutABinaryDoesNothing(t *testing.T) {
	c := New()

	if warns := c.Retire("feature-x.api", Ownership{Owned: true, Port: 4321}); len(warns) != 0 {
		t.Errorf("without a binary there is nothing to retire and nothing to warn about: %v", warns)
	}
}

// An empty handle means there was never a contract here: nothing to look up and no CLI call to make.
func TestRetireWithAnEmptyNameIsANoOp(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if warns := c.Retire("", Ownership{Owned: true, Port: 4321}); len(warns) != 0 {
		t.Errorf("an empty handle is not worth a warning: %v", warns)
	}
	if len(f.calls) != 0 {
		t.Errorf("and no CLI call may be made for it: %v", f.calls)
	}
}

// The read itself can fail (corrupt routes.json, Node too old): say so, because a retirement that silently did not happen is a stale name nobody will ever reconcile again — the handle already moved to the new one.
func TestRetireWarnsWhenTheRouteCannotBeRead(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.binErr = errors.New("Error: requires Node >= 24")

	warns := c.Retire("feature-x.api", Ownership{Owned: true, Port: 4321})

	if len(warns) == 0 || !strings.Contains(warns[0], "left untouched") {
		t.Errorf("an unreadable route must be reported, not dropped: %v", warns)
	}
}

// A removal that fails must say so: silently losing the retirement is how a stale name comes back weeks later as an inexplicable conflict.
func TestRetireWarnsWhenTheRemovalFails(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("feature-x.api")] = 4321
	f.removeErr = errors.New("EACCES: permission denied")

	warns := c.Retire("feature-x.api", Ownership{Owned: true, Port: 4321})

	if _, still := f.routes[Hostname("feature-x.api")]; !still {
		t.Error("a failed removal leaves the route in place")
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "left behind") {
		t.Errorf("the user must learn the route could not be removed: %v", warns)
	}
}
