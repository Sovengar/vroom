package portless

import (
	"os"
	"path/filepath"
	"testing"
)

// Reconcile's ownership guard was dead code: liveRoute returns published=0 on both routeUnknown paths and Authorises(0) is never true, so Remove ran with revoked ownership.

func TestReconcileRevokedWithUnknownRouteLeavesItAlone(t *testing.T) {
	cases := []struct {
		name    string
		noProxy bool // noProxy removes proxy.port, one of the two routeUnknown paths.
	}{
		{"proxy stopped (no proxy port)", true},
		{"proxy running but does not serve the host", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			c := f.client(t)
			f.routes[Hostname("ajena")] = 4321
			if tc.noProxy {
				removeFile(t, c, "proxy.port")
			} else {
				// The fake answers 404 for what it does not know, so the route must exist in the state file yet be excluded from what it routes.
				f.serve404[Hostname("ajena")] = true
			}

			warns := c.Reconcile("ajena", Ownership{Owned: false, Port: 4321}, "ajena-renombrada")

			if _, still := f.routes[Hostname("ajena")]; !still {
				t.Fatalf("another's route cannot be deleted without ownership (revoked=%v)", false)
			}
			if len(warns) == 0 {
				t.Error("and it must warn: the user needs to know there is a route that is not cleaned up")
			}
		})
	}
}

// Removing proxy.port is the real M6 state: the file only exists while the proxy runs.
func removeFile(t *testing.T, c *Client, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(c.stateDir, name)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// A guard that failed closed everywhere would pass the test above by over-removing, losing the orphans prune never touches.

func TestReconcileOwnedOrphanIsStillRemovedWithProxyDown(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("nuestra")] = 4321
	removeFile(t, c, "proxy.port")

	warns := c.Reconcile("nuestra", Ownership{Owned: true, Port: 4321}, "nuestra-renombrada")

	if _, still := f.routes[Hostname("nuestra")]; still {
		t.Error("an owned orphan with the proxy stopped MUST be removed: it is the only thing prune does not do")
	}
	if len(warns) != 0 {
		t.Errorf("removing an owned orphan does not warn: %v", warns)
	}
}

func TestReconcileOwnedOrphanIsStillRemovedWhenNotServed(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("nuestra")] = 4321
	f.noProxy = true

	c.Reconcile("nuestra", Ownership{Owned: true, Port: 4321}, "nuestra-renombrada")

	if _, still := f.routes[Hostname("nuestra")]; still {
		t.Error("an owned orphan that the proxy does not route MUST be removed")
	}
}

func TestReconcileOwnedButNameIsFreeIsANoOp(t *testing.T) {
	f := newFake()
	c := f.client(t)
	removeFile(t, c, "proxy.port")

	warns := c.Reconcile("nunca-existio", Ownership{Owned: true, Port: 4321}, "otro-nombre")

	if len(warns) != 0 {
		t.Errorf("with no route to clean up there is nothing to warn about: %v", warns)
	}
	if len(f.removedNames) != 0 {
		t.Errorf("and it should not even be attempted: %v", f.removedNames)
	}
}

func TestReconcileRevokedWithFreeNameIsANoOp(t *testing.T) {
	f := newFake()
	c := f.client(t)
	removeFile(t, c, "proxy.port")

	warns := c.Reconcile("nunca-existio", Ownership{Owned: false, Port: 4321}, "otro-nombre")

	if len(warns) == 0 {
		t.Error("free name and revoked ownership: it warns that it cannot be cleaned up")
	}
	if len(f.removedNames) != 0 {
		t.Errorf("but the removal should not be attempted: %v", f.removedNames)
	}
}

// verify only runs after Register succeeded, so its own literals carry Registered=true; for an unknown name Apply composes the Result, so verify must not claim a write.

func TestVerifyReportsRegisteredWhenTheRouteIsServed(t *testing.T) {
	f := newFake()
	f.routes[Hostname("sana")] = 4321

	res := f.client(t).verify("sana", 4321)

	if !res.Registered {
		t.Error("a route that the proxy serves with 200 is registered by definition")
	}
}

func TestVerifyDoesNotInventRegistrationForAnUnknownName(t *testing.T) {
	f := newFake()

	res := f.client(t).verify("nunca-registrada", 4321)

	if res.Registered {
		t.Error("verify must not claim a write it did not do")
	}
}
