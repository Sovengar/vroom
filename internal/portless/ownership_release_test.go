package portless

import (
	"errors"
	"testing"
)

type recordingReleaser struct {
	removed []string
	err     error
}

func (r *recordingReleaser) RemoveAbsent(name string) error {
	r.removed = append(r.removed, name)
	return r.err
}

// errRemoveFailed is a removal failure, which must NOT revoke ownership.
var errRemoveFailed = errors.New("portless alias --remove exited 1")

// Removing the route and revoking ownership are two facts: conflating them is what kept RouteName/RoutePort forever, and the handle survives either way because it says WHERE to look while ownership says WHETHER to stomp.

func TestReleaseRevokesOwnershipOnSuccess(t *testing.T) {
	rec := &recordingReleaser{}
	if !Release(rec, "mi-ruta") {
		t.Error("una retirada efectiva debe devolver true")
	}
	if len(rec.removed) != 1 {
		t.Errorf("debe intentar la retirada, got %v", rec.removed)
	}
}

func TestReleaseKeepsOwnershipWhenItFails(t *testing.T) {
	rec := &recordingReleaser{err: errRemoveFailed}
	if Release(rec, "mi-ruta") {
		t.Error("una retirada fallida no debe decir que surtió efecto")
	}
	if len(rec.removed) != 1 {
		t.Error("debe intentarla igualmente")
	}
}

func TestReleaseOfNothingRevokesNothing(t *testing.T) {
	rec := &recordingReleaser{}
	if !Release(rec, "") {
		t.Error("no había nada que retirar, así que la propiedad no queda pendiente")
	}
	if len(rec.removed) != 0 {
		t.Errorf("sin nombre no se llama al binario, got %v", rec.removed)
	}
}

func TestRevokedOwnershipDoesNotAuthoriseForeignRoute(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if res := c.Apply("app", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("alta inicial: %+v", res)
	}

	held := Ownership{Owned: true, Port: 4321}
	if Release(c, "app") {
		held = Ownership{}
	}

	f.routes[Hostname("app")] = 4321

	res := c.Apply("app", 5000, held)
	if res.Succeeded() {
		t.Fatal("con la propiedad revocada no se puede pisar la ruta de otro")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("el motivo debe ser route_conflict, got %q", res.Reason)
	}
	if got := f.routes[Hostname("app")]; got != 4321 {
		t.Errorf("la ruta ajena debe seguir en 4321, got %d", got)
	}
}

func TestLiveOwnershipStillAuthorisesMovingOurRoute(t *testing.T) {
	f := newFake()
	c := f.client(t)
	if res := c.Apply("app", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("alta inicial: %+v", res)
	}

	res := c.Apply("app", 5000, Ownership{Owned: true, Port: 4321})
	if !res.Succeeded() {
		t.Fatalf("mover la ruta propia debe funcionar: %+v", res)
	}
	if got := f.routes[Hostname("app")]; got != 5000 {
		t.Errorf("la ruta propia debe pasar a 5000, got %d", got)
	}
}

func TestLiveOwnershipAuthorisesIdempotentReRegister(t *testing.T) {
	f := newFake()
	c := f.client(t)
	if res := c.Apply("app", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("alta inicial: %+v", res)
	}
	if res := c.Apply("app", 4321, Ownership{Owned: true, Port: 4321}); !res.Succeeded() {
		t.Fatalf("re-registrar la ruta propia sin cambios debe funcionar: %+v", res)
	}
}

func TestNoOwnershipStillRegistersAFreeName(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if res := c.Apply("nuevo", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("un nombre libre se registra sin necesidad de propiedad: %+v", res)
	}
}
