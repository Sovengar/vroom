package orchestrate

import (
	"testing"

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

func (r *recordingReleaser) Remove(name string) error {
	r.removed = append(r.removed, name)
	return nil
}

// installEngineReleaser apunta la retirada del engine al doble del test.
func installEngineReleaser(t *testing.T, rec *recordingReleaser) {
	t.Helper()
	t.Cleanup(func() { engineReleaseStub, engineReleaseStubInstalled = nil, false })
	engineReleaseStub = rec.Remove
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
	e.stopProcess("/tmp/proyecto", state.Meta{
		Name: "p", Pid: 0, Pgid: 0, Port: 0,
		RouteName: "p-route",
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
		RouteName: "ruta-de-la-sesion",
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
	e.stopProcess("/tmp/proyecto", state.Meta{Name: "p", Pid: 1, Port: 4321})

	if len(rec.removed) != 0 {
		t.Errorf("sin ruta registrada no debe retirarse nada, got %v", rec.removed)
	}
}

var _ portless.Releaser = (*recordingReleaser)(nil)
