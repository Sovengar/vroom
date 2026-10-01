package portless

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// La guarda muerta de routeUnknown.
//
// El predicado era:
//
//	if !held.Authorises(published) && published != 0 { return aviso }
//
// y published es ESTRUCTURALMENTE 0 en esa rama: liveRoute devuelve
// (0, routeUnknown) en sus dos caminos de "no sé" — sin proxy.port, o todas las
// sondas fallan/404—. Así que:
//
//   - published != 0  NUNCA se cumple
//   - Authorises(0)   NUNCA es cierto (exige Port > 0)
//
// y Remove(prev) corría INCONDICIONAL, sin comprobar propiedad alguna.
//
// El comentario del sitio afirmaba que la guarda impedía justo eso. No lo
// impedía, y `alias --remove` es una escritura pura (M1), así que el borrado
// tenía éxito incluso con el proxy parado.
//
// Estos tests exerten Client.Reconcile DIRECTAMENTE. Un spy que demuestra que
// startsvc PASA una Ownership revocada no prueba nada de una guarda que vive un
// nivel más abajo: por eso el hueco era invisible.
// ---------------------------------------------------------------------------

// revoked + proxy que no sirve → la ruta ajena NO se borra.
func TestReconcileRevokedWithUnknownRouteLeavesItAlone(t *testing.T) {
	cases := []struct {
		name    string
		noProxy bool // sin proxy.port, que es uno de los dos caminos de routeUnknown
	}{
		{"proxy parado (sin proxy.port)", true},
		{"proxy en marcha pero no sirve el host", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			c := f.client(t)
			// Una ruta AJENA en el nombre, en nuestro puerto persistido.
			f.routes[Hostname("ajena")] = 4321
			if tc.noProxy {
				// sin proxy.port: liveRoute no puede ni mirar
				removeFile(t, c, "proxy.port")
			} else {
				// Proxy en marcha que NO enruta este host. El doble responde
				// 404 a lo que no conoce, así que la ruta tiene que existir en
				// el estado pero no estar en la tabla que el proxy sirve: se
				// withdraw del set que el doble enruta y se deja en routes.
				f.serve404[Hostname("ajena")] = true // la sonda compara con el TLD
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

// removeFile borra un fichero del state dir del cliente, para simular que el
// proxy está parado sin tocar el de nadie. Es el estado real de M6: proxy.port
// sólo existe mientras corre.
func removeFile(t *testing.T, c *Client, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(c.stateDir, name)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Las otras dos celdas. Un arreglo que fallara cerrado en todas partes pasaría
// el test de arriba borrando de más, y perdería la razón de existir de
// Reconcile: las huérfanas que prune no toca.
// ---------------------------------------------------------------------------

// owned + proxy parado → nuestra huérfana SÍ se retira. Es la función
// principal de esta ruta y no puede romperse al cerrar el hueco.
func TestReconcileOwnedOrphanIsStillRemovedWithProxyDown(t *testing.T) {
	f := newFake()
	c := f.client(t)
	// Nuestra ruta, en nuestro puerto persistido, con la propiedad VIVA.
	f.routes[Hostname("nuestra")] = 4321
	removeFile(t, c, "proxy.port") // el proxy está parado

	warns := c.Reconcile("nuestra", Ownership{Owned: true, Port: 4321}, "nuestra-renombrada")

	if _, still := f.routes[Hostname("nuestra")]; still {
		t.Error("una huérfana propia con el proxy parado DEBE retirarse: es lo único que prune no hace")
	}
	if len(warns) != 0 {
		t.Errorf("retirar una huérfana propia no avisa: %v", warns)
	}
}

// owned + proxy en marcha que no enruta (404) → también se retira.
func TestReconcileOwnedOrphanIsStillRemovedWhenNotServed(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("nuestra")] = 4321
	f.noProxy = true // nadie atiende: el doble no enruta el host

	c.Reconcile("nuestra", Ownership{Owned: true, Port: 4321}, "nuestra-renombrada")

	if _, still := f.routes[Hostname("nuestra")]; still {
		t.Error("una huérfana propia que el proxy no enruta DEBE retirarse")
	}
}

// owned + el nombre ya no existe → no-op, sin avisos ni borrados.
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

// La propiedad revocada con el nombre LIBRE tampoco hace nada: no hay ruta
// ajena que proteger, y Remove de un nombre ausente es benigno pero pointless.
func TestReconcileRevokedWithFreeNameIsANoOp(t *testing.T) {
	f := newFake()
	c := f.client(t)
	removeFile(t, c, "proxy.port")

	warns := c.Reconcile("nunca-existio", Ownership{Owned: false, Port: 4321}, "otro-nombre")

	// Con la propiedad revocada no se puede ni afirmar que haya ruta, así que se
	// avisa: el nombre sigue ahí y nadie lo va a limpiar. Es el comportamiento
	// fail-closed, y es lo que hace que este camino no pueda fallar en silencio.
	if len(warns) == 0 {
		t.Error("nombre libre y propiedad revocada: se avisa de que no se puede limpiar")
	}
	if len(f.removedNames) != 0 {
		t.Errorf("pero no debe intentarse la retirada: %v", f.removedNames)
	}
}

// ---------------------------------------------------------------------------
// El invariante de verify.
//
// verify solo se llama DESPUES de que Register tuviera exito, asi que todo lo
// que devuelve lleva Registered=true, incluido lo degradado: degradar el ESTADO
// no deshace el HECHO de la escritura.
//
// Ese campo lo pone verify en sus propios literales, no Apply por fuera. Asi la
// funcion no puede equivocarse por su cuenta si manana se llama desde otro
// sitio. Este test fija el invariante para que el refactor que lo eliminase
// no pueda volver sin que nada se entere.
// ---------------------------------------------------------------------------

func TestVerifyReportsRegisteredWhenTheRouteIsServed(t *testing.T) {
	f := newFake()
	f.routes[Hostname("sana")] = 4321

	res := f.client(t).verify("sana", 4321)

	if !res.Registered {
		t.Error("una ruta que el proxy sirve con 200 esta registrada por definicion")
	}
}

// Y el limite del invariante: verify sobre un nombre que no existe NO es una
// ruta registrada. En ese camino el Result lo compone Apply, no verify.
func TestVerifyDoesNotInventRegistrationForAnUnknownName(t *testing.T) {
	f := newFake()

	res := f.client(t).verify("nunca-registrada", 4321)

	if res.Registered {
		t.Error("verify no debe reclamar una escritura que no hizo")
	}
}
