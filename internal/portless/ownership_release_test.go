package portless

import (
	"errors"
	"testing"
)

// recordingReleaser registra lo que se le pide retirar y puede fallar.
type recordingReleaser struct {
	removed []string
	err     error
}

func (r *recordingReleaser) RemoveAbsent(name string) error {
	r.removed = append(r.removed, name)
	return r.err
}

// errRemoveFailed es un fallo de retirada que NO debe revoke la propiedad.
var errRemoveFailed = errors.New("portless alias --remove exited 1")

// ---------------------------------------------------------------------------
// El stop retira la ruta y REVOCA la propiedad.
//
// Son dos hechos distintos y confundirlos es lo que abrió el HIGH: RouteName y
// RoutePort se conservaban para siempre, así que una ruta ya retirada seguía
// pareciendo nuestra y el arranque siguiente podía pisar la de otro.
//
// El handle se conserva SÍ o sí, incluso tras revocar: si la retirada falla, la
// ruta puede seguir ahí y sin handle la reconciliación no podría limpiarla. El
// handle dice DÓNDE mirar; la propiedad dice SI se puede pisar. No se
// contradicen.
// ---------------------------------------------------------------------------

// Una retirada efectiva revoca la propiedad y devuelve true.
func TestReleaseRevokesOwnershipOnSuccess(t *testing.T) {
	rec := &recordingReleaser{}
	if !Release(rec, "mi-ruta") {
		t.Error("una retirada efectiva debe devolver true")
	}
	if len(rec.removed) != 1 {
		t.Errorf("debe intentar la retirada, got %v", rec.removed)
	}
}

// Una retirada que FALLA no revoca: la ruta puede seguir ahí, y perder el
// handle la dejaría sin nadie que la limpie.
func TestReleaseKeepsOwnershipWhenItFails(t *testing.T) {
	rec := &recordingReleaser{err: errRemoveFailed}
	if Release(rec, "mi-ruta") {
		t.Error("una retirada fallida no debe decir que surtió efecto")
	}
	if len(rec.removed) != 1 {
		t.Error("debe intentarla igualmente")
	}
}

// Un nombre vacío no es una retirada: no había nada nuestro que revocar.
func TestReleaseOfNothingRevokesNothing(t *testing.T) {
	rec := &recordingReleaser{}
	if !Release(rec, "") {
		t.Error("no había nada que retirar, así que la propiedad no queda pendiente")
	}
	if len(rec.removed) != 0 {
		t.Errorf("sin nombre no se llama al binario, got %v", rec.removed)
	}
}

// ---------------------------------------------------------------------------
// La verdad completa de la combinación que cerraba el HIGH, en un sitio
// legible: propiedad CONCEDIDA o REVOCADA, con el nombre libre u ocupado por
// otro en nuestro puerto anterior.
// ---------------------------------------------------------------------------

// REVOCADA + nombre ajeno en nuestro puerto anterior → no se puede pisar. Éste
// es exactamente el HIGH.
func TestRevokedOwnershipDoesNotAuthoriseForeignRoute(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if res := c.Apply("app", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("alta inicial: %+v", res)
	}

	// El stop retira la ruta y revoca.
	held := Ownership{Owned: true, Port: 4321}
	if Release(c, "app") {
		held = Ownership{}
	}

	// Otro dueño toma el nombre, en nuestro puerto anterior.
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

// CONCEDIDA + nombre ajeno en nuestro puerto → se mueve. Es el caso que prevPort
// existía para servir, y el que no debe romperse al cerrar el HIGH: sin esto,
// reiniciar la app en otro puerto dejaría la ruta apuntando a un puerto muerto.
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

// CONCEDIDA + nombre ya en NUESTRO puerto → alta idempotente, que es lo que
// hace que un reinicio converja sin acumular rutas.
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

// SIN propiedad + nombre libre → se registra. El caso normal de arranque en
// frío, que no debe depender de ninguna concesión.
func TestNoOwnershipStillRegistersAFreeName(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if res := c.Apply("nuevo", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("un nombre libre se registra sin necesidad de propiedad: %+v", res)
	}
}
