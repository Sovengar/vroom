package portless

import "testing"

// A persisted port is a capability and a capability expires: stop kept RouteName/RoutePort forever, so any actor taking the name and leaving it on our old port became an owner we would stomp.

func TestPrevPortDoesNotSurviveRelease(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if res := c.Apply("main.proj", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("primer arranque: %+v", res)
	}

	// Modelled with the production stop function, not by hand: the bug was in the persisted Meta, so hand-writing prev would not reproduce it.
	persisted := persistedOwnershipAfterStop(c, 4321)
	if persisted.Owned {
		t.Fatal("tras una retirada efectiva la propiedad debe quedar revocada")
	}

	f.routes[Hostname("main.proj")] = 4321

	res := c.Apply("main.proj", 5000, persisted)

	if res.Succeeded() {
		t.Fatal("tras retirar la ruta, prevPort no autoriza nada: no se puede pisar la ruta de otro")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("el motivo debe ser route_conflict, got %q", res.Reason)
	}
	if got := f.routes[Hostname("main.proj")]; got != 4321 {
		t.Errorf("la ruta del otro dueño debe quedar intacta en 4321, got %d", got)
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
		t.Errorf("sin propiedad previa, cualquier nombre ajeno es conflicto, got %q", res.Reason)
	}
	if got := f.routes[Hostname("ajena")]; got != 4321 {
		t.Errorf("la ruta ajena debe quedar intacta, got %d", got)
	}
}
