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
		t.Error("a Meta with RouteOwned must authorize its own port")
	}
	if (portless.Ownership{Owned: held.RouteOwned, Port: held.RoutePort}).Authorises(9999) {
		t.Error("it must not authorize a port that is not its own")
	}

	released := state.Meta{RouteName: "app", RoutePort: 4321, RouteOwned: false}
	if (portless.Ownership{Owned: released.RouteOwned, Port: released.RoutePort}).Authorises(4321) {
		t.Error("a Meta with RouteOwned=false must not authorize anything even if it keeps the port")
	}
}

// Port 0 never authorises even with Owned set: otherwise a fresh all-zero Meta would look like the owner of everything.
func TestOwnershipWithoutPortNeverAuthorises(t *testing.T) {
	if (portless.Ownership{Owned: true, Port: 0}).Authorises(0) {
		t.Error("ownership without a port cannot authorize")
	}
	if (portless.Ownership{}).Authorises(4321) {
		t.Error("empty ownership authorizes nothing")
	}
}
