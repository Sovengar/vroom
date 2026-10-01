package portless

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// El caso SECUENCIAL del conflicto: el que de verdad ocurre.
//
// El test de la ventana intercalada (TestReadBackDetectsNameTakenByAnotherPort)
// hace que OTRO dueño re-registre el nombre ENTRE nuestra escritura y nuestra
// lectura. Eso prueba que la comparación existe, pero no que haya nadie
// mirando el estado previo: `Register` sobrescribe sin condiciones, así que en el caso
// secuencial —que es el común— la lectura de vuelta sólo puede confirmar la
// escritura que acabamos de hacer. Es una tautología, y una tautología no
// detecta un conflicto.
//
// Aquí el nombre YA lo tiene otro puerto cuando llegamos, y nada lo cambia
// durante la llamada. Es exactamente la reproducción del reviewer.
// ---------------------------------------------------------------------------

// Un nombre que ya tiene OTRO puerto NO se puede sobrescribir a ciegas: el
// registro se rechaza, la ruta ajena se queda como estaba, y vroom degrada con
// motivo de conflicto en vez de publicar una dirección que acaba de destruir.
func TestSequentialConflictDoesNotEvictForeignRoute(t *testing.T) {
	f := newFake()
	c := f.client(t)

	// Una ruta ajena ya establecida, que vroom no ha creado.
	f.routes[Hostname("main.proj")] = 4000

	res := c.Apply("main.proj", 5000, Ownership{}) // prevPort 0: nunca registramos este nombre

	if res.Succeeded() {
		t.Fatal("no se puede reportar registrada una ruta cuyo nombre tiene otro puerto")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("el motivo debe ser route_conflict, got %q", res.Reason)
	}
	if res.Url != "" {
		t.Error("en conflicto no se publica url")
	}

	// Y lo importante: la ruta AJENA sigue intacta. Destruirla sería el daño
	// que todo este diseño existe para evitar — el mismo fallo cerrado que
	// gobierna la limpieza, aplicado al alta.
	if got := f.routes[Hostname("main.proj")]; got != 4000 {
		t.Errorf("la ruta ajena debe quedar intacta en su puerto 4000, got %d", got)
	}
}

// El caso idempotente: el nombre ya es NUESTRO y apunta a NUESTRO puerto.
// Re-registrar debe funcionar, porque un reinicio tiene que converger. Es el
// único caso en el que el re-registro se permite.
func TestReRegisteringOurOwnRouteSucceeds(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("mine.proj")] = 4321 // la nuestra, del arranque anterior

	res := c.Apply("mine.proj", 4321, Ownership{Owned: true, Port: 4321}) // persistida en ese mismo puerto

	if !res.Succeeded() {
		t.Fatalf("re-registrar la ruta propia debe funcionar: %+v", res)
	}
	if res.Port != 4321 {
		t.Errorf("debe apuntar al puerto real, got %d", res.Port)
	}
}

// Un reinicio donde la app hace bind en OTRO puerto: la ruta NUESTRA se
// actualiza. Es distinto del conflicto porque el nombre y el puerto anterior
// los puso vroom, y por eso hay que poder distinguirlos.
//
// La distinción que lo hace posible: se comparan el puerto persistido y el
// actual, y sólo se rechaza el alta cuando hay un tercero. Este test fija ese
// comportamiento desde el lado del cliente, que es quien ve el estado previo.
func TestReregisterMovesOurRouteToTheNewPort(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("app")] = 4000

	// El servicio se reinició y hace bind en otro puerto. La ruta es nuestra,
	// así que actualizarla es lo correcto y lo que hace que no queden rutas
	// apuntando a puertos muertos.
	res := c.Apply("app", 4321, Ownership{Owned: true, Port: 4000}) // persistida en 4000, ahora en 4321
	if !res.Succeeded() {
		t.Fatalf("actualizar la ruta propia debe funcionar: %+v", res)
	}
	if got := f.routes[Hostname("app")]; got != 4321 {
		t.Errorf("la ruta propia debe pasar a apuntar al puerto nuevo, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// La ventana intercalada se sigue cubriendo: el conflicto también puede
// aparecer ENTRE la lectura de vuelta y el alta. Sin esto, el arreglo de
// "lookup antes de registrar" parecería suficiente y no lo es.
// ---------------------------------------------------------------------------

func TestInterleavedConflictIsStillDetected(t *testing.T) {
	f := newFake()
	c := f.client(t)

	realExec := c.exec
	var hijacked bool
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if len(args) > 0 && args[0] == "list" && !hijacked {
			hijacked = true
			// Tras nuestra lectura de vuelta, un tercer actor toma el nombre.
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
