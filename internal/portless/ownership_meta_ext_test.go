package portless_test

import (
	"testing"

	"vroom/internal/portless"
	"vroom/internal/state"
)

// El Meta declara la propiedad y Apply la lee. Es el enlace entre las dos
// mitades: sin él, el arranque no vería la propiedad al reiniciar y movería la
// ruta sin autorización.
//
// Y es donde el HIGH se cierra: un Meta con RoutePort escrito y RouteOwned
// con Owned=false es EXACTAMENTE lo que dejaba el stop antes de esta corrección,
// y es lo que hacía falta para no pisar la ruta de otro.
func TestOwnershipRoundTripsThroughMeta(t *testing.T) {
	held := state.Meta{RouteName: "app", RoutePort: 4321, RouteOwned: true}
	if !(portless.Ownership{Owned: held.RouteOwned, Port: held.RoutePort}).Authorises(4321) {
		t.Error("un Meta con RouteOwned debe autorizar su propio puerto")
	}
	if (portless.Ownership{Owned: held.RouteOwned, Port: held.RoutePort}).Authorises(9999) {
		t.Error("no debe autorizar un puerto que no es el suyo")
	}

	// El Meta que dejaba el stop antes: puerto persistido, propiedad ausente.
	released := state.Meta{RouteName: "app", RoutePort: 4321, RouteOwned: false}
	if (portless.Ownership{Owned: released.RouteOwned, Port: released.RoutePort}).Authorises(4321) {
		t.Error("un Meta con RouteOwned=false no debe autorizar nada aunque conserve el puerto")
	}
}

// Port 0 nunca autoriza, aunque se olvide Owned: una propiedad sin puerto es
// una afirmación vacía, y tratarla como autorización abriría la puerta por la
// que un Meta recién creado (todo cero) parecería dueño de cualquier cosa.
func TestOwnershipWithoutPortNeverAuthorises(t *testing.T) {
	if (portless.Ownership{Owned: true, Port: 0}).Authorises(0) {
		t.Error("una propiedad sin puerto no puede autorizar")
	}
	if (portless.Ownership{}).Authorises(4321) {
		t.Error("la propiedad vacía no autoriza nada")
	}
}
