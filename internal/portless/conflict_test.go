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
		t.Fatal("no se puede reportar registrada una ruta cuyo nombre tiene otro puerto")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("el motivo debe ser route_conflict, got %q", res.Reason)
	}
	if res.Url != "" {
		t.Error("en conflicto no se publica url")
	}

	// The foreign route must stay intact: destroying it is the harm this design exists to avoid, the same fail-closed rule that governs cleanup.
	if got := f.routes[Hostname("main.proj")]; got != 4000 {
		t.Errorf("la ruta ajena debe quedar intacta en su puerto 4000, got %d", got)
	}
}

// A restart has to converge, so re-registering our own name on our own port is the one case a write is allowed.
func TestReRegisteringOurOwnRouteSucceeds(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("mine.proj")] = 4321

	res := c.Apply("mine.proj", 4321, Ownership{Owned: true, Port: 4321})

	if !res.Succeeded() {
		t.Fatalf("re-registrar la ruta propia debe funcionar: %+v", res)
	}
	if res.Port != 4321 {
		t.Errorf("debe apuntar al puerto real, got %d", res.Port)
	}
}

// Only a third party is rejected: the persisted and current ports are compared, so moving our own port is not a conflict.
func TestReregisterMovesOurRouteToTheNewPort(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("app")] = 4000

	res := c.Apply("app", 4321, Ownership{Owned: true, Port: 4000})
	if !res.Succeeded() {
		t.Fatalf("actualizar la ruta propia debe funcionar: %+v", res)
	}
	if got := f.routes[Hostname("app")]; got != 4321 {
		t.Errorf("la ruta propia debe pasar a apuntar al puerto nuevo, got %d", got)
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
		t.Errorf("el conflicto intercalado también debe detectarse, got %q", res.Reason)
	}
	if res.Succeeded() {
		t.Error("no se puede publicar una url sobre un nombre que otro dueño acaba de tomar")
	}
}
