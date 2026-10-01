package orchestrate

import (
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/scanner"

	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// El stop de los stacks tiene que RETIRAR la ruta.
//
// El reviewer comprobó que borrando los tres call sites de Release la suite
// seguía en verde, así que la decisión 13 del ADR —"se retira en los tres
// caminos"— no la verificaba nada. Este test cierra el hueco para el motor de
// stacks, que tiene además el caso del rollback de un arranque fallido.
// ---------------------------------------------------------------------------

// recordingReleaser registra lo que se le pide retirar.
type recordingReleaser struct{ removed []string }

func (r *recordingReleaser) RemoveAbsent(name string) error {
	r.removed = append(r.removed, name)
	return nil
}

// installEngineReleaser apunta la retirada del engine al doble del test.
func installEngineReleaser(t *testing.T, rec *recordingReleaser) {
	t.Helper()
	t.Cleanup(func() { engineReleaseStub, engineReleaseStubInstalled = nil, false })
	engineReleaseStub = rec.RemoveAbsent
	engineReleaseStubInstalled = true
}

// newTestEngine monta un engine con store aislado y un manager inerte: lo que
// se prueba es la retirada de la ruta, no el kill.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	return NewEngine(&noKillManager{}, state.NewStoreAt(t.TempDir()))
}

// noKillManager acepta cualquier Stop sin tocar nada.
type noKillManager struct{}

func (*noKillManager) Stop(process.StopSpec) error { return nil }
func (*noKillManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}
func (*noKillManager) Evaluate(process.EvalSpec) process.Status { return process.StatusStopped }

// stopProcess retira la ruta del servicio que para.
func TestEngineStopProcessRemovesTheServiceRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installEngineReleaser(t, rec)

	e := newTestEngine(t)
	e.stopProcess("/tmp/proyecto", &state.Meta{
		Name: "p", Pid: 0, Pgid: 0, Port: 0,
		RouteName: "p-route", RoutePort: 4321, RouteOwned: true,
	})

	if len(rec.removed) != 1 || rec.removed[0] != "p-route" {
		t.Errorf("stopProcess debe retirar la ruta del servicio, got %v", rec.removed)
	}
}

// El rollback de un arranque fallido también consume la retirada: abortAndCleanup
// llama a stopProcess, así que abortar una sesión con rutas registradas las
// retira y no las deja huérfanas.
func TestEngineAbortCleanupRemovesRoutes(t *testing.T) {
	rec := &recordingReleaser{}
	installEngineReleaser(t, rec)

	e := newTestEngine(t)
	dir := t.TempDir()
	if err := e.store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 1, Port: 4321, State: state.StateRunning,
		RouteName: "ruta-de-la-sesion", RoutePort: 4321, RouteOwned: true,
	}); err != nil {
		t.Fatal(err)
	}

	e.abortAndCleanup([]string{dir})

	if len(rec.removed) != 1 || rec.removed[0] != "ruta-de-la-sesion" {
		t.Errorf("el rollback de la sesión debe retirar la ruta, got %v", rec.removed)
	}
}

// Sin ruta registrada no se invoca la retirada.
func TestEngineStopWithoutRouteDoesNotCallRelease(t *testing.T) {
	rec := &recordingReleaser{}
	installEngineReleaser(t, rec)

	e := newTestEngine(t)
	e.stopProcess("/tmp/proyecto", &state.Meta{Name: "p", Pid: 1, Port: 4321})

	if len(rec.removed) != 0 {
		t.Errorf("sin ruta registrada no debe retirarse nada, got %v", rec.removed)
	}
}

// ---------------------------------------------------------------------------
// Las dos ramas del servicio YA MUERTO.
//
// stopProcess sólo corre cuando hay algo que matar. Un servicio que ya estaba
// muerto al pararse (Pid 0, Pgid 0, Port 0) con una ruta heredada en el Meta
// no pasa por ahí, y sus dos ramas propias se saltaban sin que ningún test las
// mirara: neutralizarlas dejaba la suite VERDE.
//
// El estado es alcanzable: resolveDynamicPort sólo registra ruta cuando
// State==running && Port>0, pero RouteName/RoutePort heredados siguen
// persistidos, así que un servicio que tuvo ruta y luego no abre ningún puerto
// TCP acaba con Pid=0, Pgid=0, Port=0 y RouteName != "".
// ---------------------------------------------------------------------------

// stopService con el servicio ya muerto retira su ruta.
func TestStopServiceDeadServiceRemovesRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installEngineReleaser(t, rec)

	e := newTestEngine(t)
	dir := t.TempDir()
	if _, err := e.store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 0, Pgid: 0, Port: 0, State: state.StateRunning,
		RouteName: "ruta-heredada", RoutePort: 4321, RouteOwned: true,
	}); err != nil {
		t.Fatal(err)
	}

	e.stopService(scanner.Project{Path: dir, Configured: true, Manifest: &manifest.Manifest{Name: "p"}})

	if len(rec.removed) != 1 || rec.removed[0] != "ruta-heredada" {
		t.Errorf("stopService debe retirar la ruta del servicio ya muerto, got %v", rec.removed)
	}
	meta, err := e.store.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RouteOwned {
		t.Error("tras retirar la ruta la propiedad debe quedar revocada")
	}
}

// abortAndCleanup con el servicio ya muerto también: esta rama era la que
// llamaba a portless.Release(nil, ...) con el cliente real hardcodeado, así que
// ningún test podía observarla.
func TestAbortCleanupDeadServiceRemovesRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installEngineReleaser(t, rec)

	e := newTestEngine(t)
	dir := t.TempDir()
	if _, err := e.store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 0, Pgid: 0, Port: 0, State: state.StateRunning,
		RouteName: "ruta-de-sesion", RoutePort: 4321, RouteOwned: true,
	}); err != nil {
		t.Fatal(err)
	}

	e.abortAndCleanup([]string{dir})

	if len(rec.removed) != 1 || rec.removed[0] != "ruta-de-sesion" {
		t.Errorf("abortAndCleanup debe retirar la ruta del servicio ya muerto, got %v", rec.removed)
	}
	meta, err := e.store.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RouteOwned {
		t.Error("tras retirar la ruta la propiedad debe quedar revocada")
	}
}

var _ portless.Releaser = (*recordingReleaser)(nil)

// El mismo caso en el motor de stacks: handle vivo sin propiedad NO es
// autoridad para borrar.
func TestEngineDoesNotRemoveAForeignRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installEngineReleaser(t, rec)

	e := newTestEngine(t)
	e.stopProcess("/tmp/proyecto", &state.Meta{
		Name: "p", Pid: 0, Port: 0,
		RouteName: "ajena", RoutePort: 4000, RouteOwned: false,
	})

	if len(rec.removed) != 0 {
		t.Errorf("el stop no puede retirar una ruta que nunca fue nuestra, got %v", rec.removed)
	}
}
