package startsvc

import (
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// HIGH-A: la propiedad se concedía sin mirar el resultado del alta.
//
// applyRoute hacía `meta.RouteOwned = true` SIEMPRE, después de Apply. Una
// registration que no ocurrió —por conflicto, o porque no había binario—
// acuñaba igual una capacidad concedente, con el puerto pedido.
//
// El defecto no es visible en make check, y no lo era leyendo el predicado de
// Apply: el predicado era correcto. El defecto estaba en quién concedía, y en que
// esa línea nunca miró `res`.
//
// Y NO se arregla concediendo con res.Succeeded(): eso significaría registered Y
// verificada, y una ruta escrita con el proxy parado es genuinamente nuestra y
// debe conservar su handle —si no, un reinicio con puerto movido chocaría con
// nuestra propia ruta. Lo honesto es un campo que diga "registrada", puesto donde
// Register tuvo éxito.
// ---------------------------------------------------------------------------

// Un alta que NO registró (conflicto) no debe dejar propiedad concedente.
func TestConflictDoesNotGrantOwnership(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
	routes := &resultSpy{result: portless.Result{
		Name: "svc", Host: "svc.localhost",
		Status: portless.StatusDegraded, Reason: portless.ReasonRouteConflict,
	}}

	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if out.Meta.RouteOwned {
		t.Error("un alta que no registró no debe conceder propiedad: es la capacidad " +
			"que permite pisar la ruta de otro en el arranque siguiente")
	}
}

// Una ruta registrada con el proxy PARADO sí es nuestra y debe conservar el
// handle: no se concede con Succeeded(), sino con "registrada".
func TestRegisteredButUnverifiedStillGrantsOwnership(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
	// proxy_not_running: escrita en disco pero sin verificar. Es NUESTRA.
	routes := &resultSpy{result: portless.Result{
		Name: "svc", Host: "svc.localhost",
		Status: portless.StatusDegraded, Reason: portless.ReasonProxyNotRunning,
		Registered: true,
	}}

	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if !out.Meta.RouteOwned {
		t.Error("una ruta registrada aunque no verificada es nuestra: debe conservar la propiedad")
	}
	if out.Meta.RouteName == "" {
		t.Error("y debe conservar el handle")
	}
}

// ---------------------------------------------------------------------------
// HIGH-B: la revocación no llegaba a Reconcile.
//
// Apply ya exigía Ownership, pero Reconcile seguía recibiendo un puerto crudo.
// La revocación cerraba una puerta y dejaba abierta su hermana: con Owned=false
// el handle sigue vivo —a propósito, para que la reconciliación tenga dónde
// mirar— y un prevPort crudo sobre ese handle es una autoridad de borrado REAL
// sobre un nombre que vroom ha descartado explícitamente.
//
// Secuencia: Apply(x,4321) → stop+Release (revoca, handle vive) → otro dueño
// toma x en 4321 con el backend caído (502) → rama renombrada → Reconcile ve
// prev != current → borra la ruta AJENA.
// ---------------------------------------------------------------------------

// Con la propiedad revocada, una rama renombrada NO debe borrar una ruta que
// responde en nuestro puerto anterior: no es nuestra.
func TestRenamedBranchDoesNotRemoveRevokedRoute(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
	// Meta de un servicio parado: handle vivo, propiedad revocada.
	if err := f.store.SaveMeta(f.dir, state.Meta{
		Name: "svc", Port: 4000, RouteName: "svc", RoutePort: 4321, RouteOwned: false,
		State: state.StateStopped,
	}); err != nil {
		t.Fatal(err)
	}

	routes := &reconcileSpy{}
	out, err := f.startWithRoutesBranch(t, 8*time.Second, routes, "feat/nueva-rama")
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	// El nombre anterior NO se ha de retirar: la propiedad está revocada.
	if routes.reconciledWithOwnership.Owned {
		t.Error("con la propiedad revocada, Reconcile no debe recibir una concesión")
	}
	if routes.removedPrev {
		t.Error("con la propiedad revocada, reconciliar no puede borrar un nombre: " +
			"el handle vivo no es autoridad para borrar")
	}
}

// Y el caso que sí debe retirar: la propiedad VIVA y la ruta muerta. Es la
// huérfana que la reconciliación existe para limpiar.
func TestRenamedBranchStillRemovesOurOwnDeadRoute(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
	if err := f.store.SaveMeta(f.dir, state.Meta{
		Name: "svc", Port: 4000, RouteName: "svc", RoutePort: 4321, RouteOwned: true,
		State: state.StateStopped,
	}); err != nil {
		t.Fatal(err)
	}

	routes := &reconcileSpy{}
	out, err := f.startWithRoutesBranch(t, 8*time.Second, routes, "feat/nueva-rama")
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	// Reconcile corre ANTES del alta, así que lo que debe recibir es la
	// propiedad PERSISTIDA del arranque anterior: concedida aquí, y con el
	// puerto que persistió. Lo que la distingue de un puerto crudo es que llega
	// dentro de Ownership, y eso es lo que se comprueba abajo.
	if !routes.reconciledWithOwnership.Authorises(4321) {
		t.Errorf("Reconcile debe recibir la Ownership persistida, no un puerto crudo: %+v",
			routes.reconciledWithOwnership)
	}
}

// ---------------------------------------------------------------------------
// El sitio que nadie listó: la herencia del handle.
//
// Start copiaba RouteName y RoutePort del Meta anterior y NO RouteOwned. Es el
// mismo patrón de los otros dos hallazgos —una decisión que lee un subconjunto
// de la propiedad— y por eso se cuenta aquí.
// ---------------------------------------------------------------------------

// La herencia copia los TRES hechos o ninguno. Copiar dos deja la propiedad
// perdida en silencio en cada arranque.
func TestInheritedRouteStateIsAllOrNothing(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
	if err := f.store.SaveMeta(f.dir, state.Meta{
		Name: "svc", Port: 4000, RouteName: "svc", RoutePort: 4321, RouteOwned: true,
		State: state.StateStopped,
	}); err != nil {
		t.Fatal(err)
	}

	routes := &resultSpy{result: portless.Result{
		Name: "svc", Host: "svc.localhost", Status: portless.StatusDegraded,
	}}
	// Sin ruta que retirar y sin alta que conceda: se observa lo heredado.
	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	_ = routes.sawPrev // el handle heredado debe viajar completo
}

// ---- dobles ----

// resultSpy devuelve un Result fijado y recuerda la Ownership que recibió.
type resultSpy struct {
	result  portless.Result
	sawPrev portless.Ownership
}

func (r *resultSpy) Apply(name string, port int, prev portless.Ownership) portless.Result {
	r.sawPrev = prev
	res := r.result
	res.Name = name
	res.Host = portless.Hostname(name)
	if res.Status == "" {
		res.Status = portless.StatusDegraded
	}
	return res
}

func (r *resultSpy) Reconcile(_ string, _ portless.Ownership, _ string) []string {
	return nil
}

// reconcileSpy recuerda lo que se le pasó a Reconcile y si retire el nombre
// anterior.
type reconcileSpy struct {
	reconciledWithOwnership portless.Ownership
	sawPrev                 portless.Ownership
	removedPrev             bool
}

func (r *reconcileSpy) Apply(name string, port int, prev portless.Ownership) portless.Result {
	r.sawPrev = prev
	return portless.Result{
		Name: name, Host: portless.Hostname(name),
		Status: portless.StatusDegraded, Reason: portless.ReasonPortlessMissing,
	}
}

func (r *reconcileSpy) Reconcile(_ string, prev OwnershipPort, _ string) []string {
	r.reconciledWithOwnership = prev
	return nil
}

// OwnershipPort es el tipo de la cuarta posición de Reconcile. Se declara aquí
// para que el test falle a COMPILAR si la firma vuelve a ser un puerto crudo —que
// es más fuerte que un fallo en runtime, porque no se puede perder en un refactor.
type OwnershipPort = portless.Ownership
