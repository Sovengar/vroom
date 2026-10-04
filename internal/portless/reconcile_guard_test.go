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
		{"proxy parado (sin proxy.port)", true},
		{"proxy en marcha pero no sirve el host", false},
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
				t.Fatalf("la ruta de otro no puede borrarse sin propiedad (revoked=%v)", false)
			}
			if len(warns) == 0 {
				t.Error("y debe avisar: el usuario tiene que saber que hay una ruta que no se limpia")
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
		t.Error("una huérfana propia con el proxy parado DEBE retirarse: es lo único que prune no hace")
	}
	if len(warns) != 0 {
		t.Errorf("retirar una huérfana propia no avisa: %v", warns)
	}
}

func TestReconcileOwnedOrphanIsStillRemovedWhenNotServed(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("nuestra")] = 4321
	f.noProxy = true

	c.Reconcile("nuestra", Ownership{Owned: true, Port: 4321}, "nuestra-renombrada")

	if _, still := f.routes[Hostname("nuestra")]; still {
		t.Error("una huérfana propia que el proxy no enruta DEBE retirarse")
	}
}

func TestReconcileOwnedButNameIsFreeIsANoOp(t *testing.T) {
	f := newFake()
	c := f.client(t)
	removeFile(t, c, "proxy.port")

	warns := c.Reconcile("nunca-existio", Ownership{Owned: true, Port: 4321}, "otro-nombre")

	if len(warns) != 0 {
		t.Errorf("sin ruta que limpiar no hay nada que avisar: %v", warns)
	}
	if len(f.removedNames) != 0 {
		t.Errorf("y no debe intentarse siquiera: %v", f.removedNames)
	}
}

func TestReconcileRevokedWithFreeNameIsANoOp(t *testing.T) {
	f := newFake()
	c := f.client(t)
	removeFile(t, c, "proxy.port")

	warns := c.Reconcile("nunca-existio", Ownership{Owned: false, Port: 4321}, "otro-nombre")

	if len(warns) == 0 {
		t.Error("nombre libre y propiedad revocada: se avisa de que no se puede limpiar")
	}
	if len(f.removedNames) != 0 {
		t.Errorf("pero no debe intentarse la retirada: %v", f.removedNames)
	}
}

// verify only runs after Register succeeded, so its own literals carry Registered=true; for an unknown name Apply composes the Result, so verify must not claim a write.

func TestVerifyReportsRegisteredWhenTheRouteIsServed(t *testing.T) {
	f := newFake()
	f.routes[Hostname("sana")] = 4321

	res := f.client(t).verify("sana", 4321)

	if !res.Registered {
		t.Error("una ruta que el proxy sirve con 200 esta registrada por definicion")
	}
}

func TestVerifyDoesNotInventRegistrationForAnUnknownName(t *testing.T) {
	f := newFake()

	res := f.client(t).verify("nunca-registrada", 4321)

	if res.Registered {
		t.Error("verify no debe reclamar una escritura que no hizo")
	}
}
