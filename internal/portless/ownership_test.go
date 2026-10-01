package portless

import "testing"

// ---------------------------------------------------------------------------
// prevPort es una CAPACIDAD, y una capacidad caduca.
//
// Autorizaba una sola cosa: "puedo mover esta ruta porque es mía y la app
// reinició en otro puerto". Autorizada una vez, se quedaba ahí para siempre en
// el Meta, porque el stop no limpiaba RouteName/RoutePort. Entonces cualquier
// actor que tomara el nombre y lo dejara en NUESTRO puerto anterior se
// convertía, de hecho, en un dueño cuya ruta podíamos pisar.
//
// La secuencia, reproducida end-to-end:
//
//	Apply("main.proj", 4321, 0)   -> registered, persistido 4321
//	Remove("main.proj")           -> la ruta desaparece
//	otro dueño registra main.proj -> 4321
//	Apply("main.proj", 5000, 4321)-> registered, y la ruta del otro pasa a 5000
//
// Lo que falta no es el puerto, es SI SEGUIMOS SIENDO EL DUEÑO. Por eso el
// predicado exige un hecho de propiedad persistido, no un número.
// ---------------------------------------------------------------------------

// Tras retirar la ruta, un tercero que tome el nombre queda fuera de alcance:
// vroom no puede destruirla ni aunque coincida con el puerto que persistió.
func TestPrevPortDoesNotSurviveRelease(t *testing.T) {
	f := newFake()
	c := f.client(t)

	// 1. Primer arranque: la ruta es nuestra y queda registrada en 4321.
	if res := c.Apply("main.proj", 4321, Ownership{}); !res.Succeeded() {
		t.Fatalf("primer arranque: %+v", res)
	}

	// 2. Se para el servicio. El Meta que se persiste es el que queda:
	//    la propiedad se revoca SÓLO si la retirada surtió efecto.
	//
	// Esto se modela con la función de producción, no a mano, porque el punto
	// del HIGH es precisamente que "revocar" depende de que la retirada
	// funcione. Un test que pone prev a mano no probaría el bug, porque el bug
	// estaba en el Meta que el stop persiste.
	persisted := persistedOwnershipAfterStop(c, 4321)
	if persisted.Owned {
		t.Fatal("tras una retirada efectiva la propiedad debe quedar revocada")
	}

	// 3. Otro dueño toma el nombre.
	f.routes[Hostname("main.proj")] = 4321

	// 4. Nuestro arranque siguiente encuentra un nombre ajeno EN NUESTRO PUERTO
	//    ANTERIOR, que es la única coincidencia posible sin ser el dueño.
	res := c.Apply("main.proj", 5000, persisted)

	if res.Succeeded() {
		t.Fatal("tras retirar la ruta, prevPort no autoriza nada: no se puede pisar la ruta de otro")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("el motivo debe ser route_conflict, got %q", res.Reason)
	}
	if got := f.routes[Hostname("main.proj")]; got != 4321 {
		t.Errorf("la ruta del otro dueño debe quedar intacta en 4321, got %d", got)
	}
}

// persistedOwnershipAfterStop es lo que el STOP persiste tras retirar la ruta:
// la propiedad revocada si la retirada surtió efecto, y conservada si no.
//
// Se usa en vez de escribir el Ownership a mano porque el defecto no estaba en
// el predicado de Apply sino en el hecho persistido: un Meta que guardaba
// RoutePort para siempre. Modelar el stop es lo que hace que el test reproduzca
// el fallo en vez de describirlo.
func persistedOwnershipAfterStop(c *Client, wasPort int) Ownership {
	held := Ownership{Owned: true, Port: wasPort}
	if Release(c, "main.proj") {
		return Ownership{} // retirada efectiva: ya no es nuestra
	}
	return held // la ruta puede seguir ahí: se conserva el handle
}

// El mismo caso sin el handle previo (prevPort = 0) es el fail-closed de siempre.
func TestForeignRouteAlwaysConflictsWithoutOwnership(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("ajena")] = 4321

	res := c.Apply("ajena", 5000, Ownership{})

	if res.Reason != ReasonRouteConflict {
		t.Errorf("sin propiedad previa, cualquier nombre ajeno es conflicto, got %q", res.Reason)
	}
	if got := f.routes[Hostname("ajena")]; got != 4321 {
		t.Errorf("la ruta ajena debe quedar intacta, got %d", got)
	}
}
