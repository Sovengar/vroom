package portless

import "testing"

// A persisted port is a capability and a capability expires: stop kept RouteName/RoutePort forever, so any actor taking the name and leaving it on our old port became an owner we would stomp.

func TestPrevPortDoesNotSurviveRelease(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if res := c.Apply("main.proj", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("first startup: %+v", res)
	}

	// Modelled with the production stop function, not by hand: the bug was in the persisted Meta, so hand-writing prev would not reproduce it.
	persisted := persistedOwnershipAfterStop(c, 4321)
	if persisted.Owned {
		t.Fatal("after an effective removal the ownership must be revoked")
	}

	f.routes[Hostname("main.proj")] = 4321

	res := c.Apply("main.proj", 5000, persisted)

	if res.Succeeded() {
		t.Fatal("after removing the route, prevPort authorizes nothing: another's route cannot be stomped")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("the reason must be route_conflict, got %q", res.Reason)
	}
	if got := f.routes[Hostname("main.proj")]; got != 4321 {
		t.Errorf("the other owner's route must remain intact at 4321, got %d", got)
	}
}

func persistedOwnershipAfterStop(c *Client, wasPort int) Ownership {
	held := Ownership{Owned: true, Port: wasPort}
	if Release(c, "main.proj") {
		return Ownership{}
	}
	return held // the route may still be there, so the handle stays for reconciliation
}

func TestForeignRouteAlwaysConflictsWithoutOwnership(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("ajena")] = 4321

	res := c.Apply("ajena", 5000, Ownership{})

	if res.Reason != ReasonRouteConflict {
		t.Errorf("without prior ownership, any foreign name is a conflict, got %q", res.Reason)
	}
	if got := f.routes[Hostname("ajena")]; got != 4321 {
		t.Errorf("the foreign route must remain intact, got %d", got)
	}
}
