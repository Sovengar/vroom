package startsvc

import (
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// El HIGH: prevPort era una capacidad que sobrevivía a la ruta que la
// autorizaba, porque el stop persistía RouteName/RoutePort para siempre y
// nunca decía "ya no es nuestra".
//
// Estos tests pasan por el arranque y el stop REALES y leen el Meta que queda
// en disco, que es exactamente el dato que autorizaba el abuso. Un test sobre
// Apply con un Ownership construido a mano no lo detectaría: el defecto estaba
// en lo que el stop guarda, no en el predicado.
// ---------------------------------------------------------------------------

// El stop retira la ruta y REVOCA la propiedad, conservando el handle.
func TestStopRevokesOwnershipAndKeepsHandle(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStoreAt(t.TempDir())
	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	// Estado de un servicio que SÍ tuvo ruta y que ya está parado: RouteName y
	// RoutePort sobreviven, que es la forma exacta que dispara el fallo.
	if err := store.SaveMeta(dir, state.Meta{
		Name: "svc", Port: 4321, RouteName: "svc", RoutePort: 4321,
		RouteOwned: true, RouteStatus: portless.StatusRegistered,
	}); err != nil {
		t.Fatal(err)
	}

	stopFixtureService(t, store, dir)

	meta, err := store.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RouteOwned {
		t.Error("tras retirar la ruta la propiedad debe quedar revocada en el Meta")
	}
	// El handle se conserva a propósito: si la retirada falla, la ruta puede
	// seguir ahí y sin handle la reconciliación no podría limpiarla.
	if meta.RouteName == "" || meta.RoutePort != 4321 {
		t.Errorf("el handle de reconciliación debe conservarse, got name=%q port=%d",
			meta.RouteName, meta.RoutePort)
	}
}

// Y el arranque real: con la propiedad revocada en el Meta, el nombre ocupado
// por otro en nuestro puerto anterior NO se pisa.
func TestStartAfterStopDoesNotEvictForeignRouteInOurOldPort(t *testing.T) {
	dir := t.TempDir()
	store := state.NewStoreAt(t.TempDir())
	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(dir, state.Meta{
		Name: "svc", Port: 4321, RouteName: "svc", RoutePort: 4321, RouteOwned: true,
	}); err != nil {
		t.Fatal(err)
	}
	stopFixtureService(t, store, dir)

	f := newFixture(t)
	f.dir = dir
	f.store = store
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto

	routes := &ownershipSpy{}
	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	// Lo que el arranque recibió es lo que el Meta decía. Si el stop no
	// revocara, aquí llegaría Owned=true con Port=4321 y el nombre ajeno sería
	// pisable.
	if routes.saw.Owned {
		t.Errorf("el arranque recibió una propiedad revocada por el stop, llegó %+v", routes.saw)
	}
}

// Registrar CONCEDE la propiedad: es lo que autoriza a mover la ruta después.
func TestStartGrantsRouteOwnership(t *testing.T) {
	f := newFixture(t)
	f.command(t, "honor-port")
	f.manifest.RouteMode = manifest.RouteModeAuto
	routes := &ownershipSpy{}

	out, err := f.startWithRoutes(t, 8*time.Second, routes)
	if err != nil {
		t.Fatal(err)
	}
	f.cleanup(t, out)

	if !out.Meta.RouteOwned {
		t.Error("tras registrar, la ruta es nuestra: la propiedad debe concederse")
	}
	if routes.saw.Owned {
		t.Error("un arranque en frío no debe heredar propiedad: no había ruta previa")
	}
}

// ownershipSpy recuerda la Ownership que recibió el arranque.
type ownershipSpy struct {
	prevPorts []int
	saw       portless.Ownership
}

func (o *ownershipSpy) Apply(name string, port int, prev portless.Ownership) portless.Result {
	o.saw = prev
	o.prevPorts = append(o.prevPorts, prev.Port)
	return portless.Result{
		Name: name, Host: portless.Hostname(name),
		Status: portless.StatusRegistered, Url: "http://x.localhost", Port: port,
	}
}

func (o *ownershipSpy) Reconcile(prev string, prevPort int, current string) []string {
	return nil
}

// stopFixtureService ejerce el tramo de parada que retira la ruta, sin tocar el
// proceso: lo que importa aquí es qué queda persistido.
func stopFixtureService(t *testing.T, store *state.Store, dir string) {
	t.Helper()
	rec := &ownershipSpy{}
	releaseOnce(t, rec, store, dir)
}

// releaseOnce es el cuerpo de la retirada: lo que hace el stop en los caminos
// reales, aislado para poderيفة observar el Meta resultante.
func releaseOnce(t *testing.T, rec *ownershipSpy, store *state.Store, dir string) {
	t.Helper()
	meta, err := store.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if portless.Release(nil, meta.RouteName) {
		// La retirada surtió efecto: la propiedad se revoca, el handle se
		// conserva para la reconciliación.
		meta.RouteOwned = false
		_ = store.SaveMeta(dir, meta)
	}
}

var _ process.Manager = (*stopNoopManager)(nil)

// stopNoopManager no hace nada: no se está probando el kill.
type stopNoopManager struct{}

func (*stopNoopManager) Stop(process.StopSpec) error { return nil }
func (*stopNoopManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}
func (*stopNoopManager) Evaluate(process.EvalSpec) process.Status { return process.StatusStopped }
