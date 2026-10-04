package portless_test

import (
	"testing"

	"vroom/internal/portless"
	"vroom/internal/state"
)

// Meta is where ownership is persisted and read back; a Meta with RoutePort set and RouteOwned=false is exactly what stop used to leave.
func TestOwnershipRoundTripsThroughMeta(t *testing.T) {
	held := state.Meta{RouteName: "app", RoutePort: 4321, RouteOwned: true}
	if !(portless.Ownership{Owned: held.RouteOwned, Port: held.RoutePort}).Authorises(4321) {
		t.Error("un Meta con RouteOwned debe autorizar su propio puerto")
	}
	if (portless.Ownership{Owned: held.RouteOwned, Port: held.RoutePort}).Authorises(9999) {
		t.Error("no debe autorizar un puerto que no es el suyo")
	}

	released := state.Meta{RouteName: "app", RoutePort: 4321, RouteOwned: false}
	if (portless.Ownership{Owned: released.RouteOwned, Port: released.RoutePort}).Authorises(4321) {
		t.Error("un Meta con RouteOwned=false no debe autorizar nada aunque conserve el puerto")
	}
}

// Port 0 never authorises even with Owned set: otherwise a fresh all-zero Meta would look like the owner of everything.
func TestOwnershipWithoutPortNeverAuthorises(t *testing.T) {
	if (portless.Ownership{Owned: true, Port: 0}).Authorises(0) {
		t.Error("una propiedad sin puerto no puede autorizar")
	}
	if (portless.Ownership{}).Authorises(4321) {
		t.Error("la propiedad vacía no autoriza nada")
	}
}
