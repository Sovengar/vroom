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
		t.Error("an effective removal must return true")
	}
	if len(rec.removed) != 1 {
		t.Errorf("it must attempt the removal, got %v", rec.removed)
	}
}

func TestReleaseKeepsOwnershipWhenItFails(t *testing.T) {
	rec := &recordingReleaser{err: errRemoveFailed}
	if Release(rec, "mi-ruta") {
		t.Error("a failed removal must not say it took effect")
	}
	if len(rec.removed) != 1 {
		t.Error("it must attempt it anyway")
	}
}

func TestReleaseOfNothingRevokesNothing(t *testing.T) {
	rec := &recordingReleaser{}
	if !Release(rec, "") {
		t.Error("there was nothing to remove, so ownership is not left pending")
	}
	if len(rec.removed) != 0 {
		t.Errorf("without a name the binary is not called, got %v", rec.removed)
	}
}

func TestRevokedOwnershipDoesNotAuthoriseForeignRoute(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if res := c.Apply("app", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("initial registration: %+v", res)
	}

	held := Ownership{Owned: true, Port: 4321}
	if Release(c, "app") {
		held = Ownership{}
	}

	f.routes[Hostname("app")] = 4321

	res := c.Apply("app", 5000, held)
	if res.Succeeded() {
		t.Fatal("with revoked ownership another's route cannot be stomped")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("the reason must be route_conflict, got %q", res.Reason)
	}
	if got := f.routes[Hostname("app")]; got != 4321 {
		t.Errorf("the foreign route must remain at 4321, got %d", got)
	}
}

func TestLiveOwnershipStillAuthorisesMovingOurRoute(t *testing.T) {
	f := newFake()
	c := f.client(t)
	if res := c.Apply("app", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("initial registration: %+v", res)
	}

	res := c.Apply("app", 5000, Ownership{Owned: true, Port: 4321})
	if !res.Succeeded() {
		t.Fatalf("moving the owned route must work: %+v", res)
	}
	if got := f.routes[Hostname("app")]; got != 5000 {
		t.Errorf("the owned route must switch to 5000, got %d", got)
	}
}

func TestLiveOwnershipAuthorisesIdempotentReRegister(t *testing.T) {
	f := newFake()
	c := f.client(t)
	if res := c.Apply("app", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("initial registration: %+v", res)
	}
	if res := c.Apply("app", 4321, Ownership{Owned: true, Port: 4321}); !res.Succeeded() {
		t.Fatalf("re-registering the owned route without changes must work: %+v", res)
	}
}

func TestNoOwnershipStillRegistersAFreeName(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if res := c.Apply("nuevo", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("a free name is registered without needing ownership: %+v", res)
	}
}
