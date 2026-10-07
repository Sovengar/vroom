package portless

import (
	"context"
	"testing"
)

// The interleaved-window test only proves the port comparison exists: Register overwrites unconditionally, so read-back merely confirms our own write, a tautology that detects no conflict.

func TestSequentialConflictDoesNotEvictForeignRoute(t *testing.T) {
	f := newFake()
	c := f.client(t)

	f.routes[Hostname("main.proj")] = 4000

	res := c.Apply("main.proj", 5000, Ownership{})

	if res.Succeeded() {
		t.Fatal("a route whose name has another port cannot be reported registered")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("the reason must be route_conflict, got %q", res.Reason)
	}
	if res.Url != "" {
		t.Error("in conflict no url is published")
	}

	// The foreign route must stay intact: destroying it is the harm this design exists to avoid, the same fail-closed rule that governs cleanup.
	if got := f.routes[Hostname("main.proj")]; got != 4000 {
		t.Errorf("the foreign route must remain intact at its port 4000, got %d", got)
	}
}

// A restart has to converge, so re-registering our own name on our own port is the one case a write is allowed.
func TestReRegisteringOurOwnRouteSucceeds(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("mine.proj")] = 4321

	res := c.Apply("mine.proj", 4321, Ownership{Owned: true, Port: 4321})

	if !res.Succeeded() {
		t.Fatalf("re-registering the owned route must work: %+v", res)
	}
	if res.Port != 4321 {
		t.Errorf("it must point to the real port, got %d", res.Port)
	}
}

// Only a third party is rejected: the persisted and current ports are compared, so moving our own port is not a conflict.
func TestReregisterMovesOurRouteToTheNewPort(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("app")] = 4000

	res := c.Apply("app", 4321, Ownership{Owned: true, Port: 4000})
	if !res.Succeeded() {
		t.Fatalf("updating the owned route must work: %+v", res)
	}
	if got := f.routes[Hostname("app")]; got != 4321 {
		t.Errorf("the owned route must switch to pointing to the new port, got %d", got)
	}
}

// The interleaved window still needs covering, or "look up before registering" looks sufficient when it is not.

func TestInterleavedConflictIsStillDetected(t *testing.T) {
	f := newFake()
	c := f.client(t)

	realExec := c.exec
	var hijacked bool
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if len(args) > 0 && args[0] == "list" && !hijacked {
			hijacked = true
			f.routes[Hostname("taken")] = 9999
		}
		return realExec(ctx, bin, args...)
	}

	res := c.Apply("taken", 4321, Ownership{})
	if res.Reason != ReasonRouteConflict {
		t.Errorf("the interleaved conflict must also be detected, got %q", res.Reason)
	}
	if res.Succeeded() {
		t.Error("a url cannot be published over a name that another owner just took")
	}
}
