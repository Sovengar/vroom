package cli

import (
	"testing"

	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// El stop de la CLI tiene que RETIRAR la ruta.
//
// El reviewer comprobó que borrando los tres call sites de Release la suite
// seguía en verde, así que la decisión 13 del ADR —"se retira en los tres
// caminos"— no la verificaba nada. Este test cierra el hueco para la CLI.
//
// Y además fija un caso que el guard de proceso dejaba fuera: un servicio que
// ya estaba MUERTO cuando se paró también deja una ruta detrás, y esa ruta no
// se retiraba porque la retirada estaba dentro del guard de Pid > 0.
// ---------------------------------------------------------------------------

// recordingReleaser registra lo que se le pide retirar.
type recordingReleaser struct{ removed []string }

func (r *recordingReleaser) Remove(name string) error {
	r.removed = append(r.removed, name)
	return nil
}

// installCLIReleaser apunta la retirada de la CLI al doble del test.
func installCLIReleaser(t *testing.T, rec *recordingReleaser) {
	t.Helper()
	t.Cleanup(func() { cliReleaseStub, cliReleaseStubInstalled = nil, false })
	cliReleaseStub = rec.Remove
	cliReleaseStubInstalled = true
}

// Un servicio con proceso vivo retira su ruta al pararse.
func TestCLIStopRemovesTheServiceRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)

	stopWithMeta(t, state.Meta{
		Name: "p", Pid: 424242, Port: 4321, State: state.StateRunning,
		RouteName: "p-route",
	})

	if len(rec.removed) != 1 || rec.removed[0] != "p-route" {
		t.Errorf("el stop debe retirar la ruta del servicio, got %v", rec.removed)
	}
}

// El caso que faltaba: un servicio YA MUERTO (Pid 0) también deja una ruta
// detrás. Con la retirada dentro del guard de proceso, esa ruta se quedaba
// para siempre y la única que podía limpiarla era la reconciliación del
// arranque siguiente — que sólo existe para huérfanas, no como sustituto de
// retirar la propia.
func TestCLIStopRemovesRouteEvenWhenAlreadyDead(t *testing.T) {
	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)

	stopWithMeta(t, state.Meta{
		Name: "p", Pid: 0, Pgid: 0, Port: 0, State: state.StateRunning,
		RouteName: "ruta-huerfana",
	})

	if len(rec.removed) != 1 || rec.removed[0] != "ruta-huerfana" {
		t.Errorf("un servicio ya muerto también debe retirar su ruta, got %v", rec.removed)
	}
}

// Sin ruta registrada no se invoca la retirada: RouteName vacío es la señal de
// que no hubo contrato de ruta, y una llamada con nombre vacío sería ruido.
func TestCLIStopWithoutRouteDoesNotCallRelease(t *testing.T) {
	rec := &recordingReleaser{}
	installCLIReleaser(t, rec)

	stopWithMeta(t, state.Meta{Name: "p", Pid: 7, Port: 4321})

	if len(rec.removed) != 0 {
		t.Errorf("sin ruta registrada no debe retirarse nada, got %v", rec.removed)
	}
}

// stopWithMeta ejerce el MISMO tramo de limpieza que cmdStop ejecuta tras
// manager.Stop -- stopCleanup, que es la funcion de produccion que el comando
// llama de verdad.
//
// Se llama a esa funcion y no a una reimplementacion: un test que replica la
// logica pasa aunque la logica se borre, y eso es justo el hueco que dejo la
// primera version.
func stopWithMeta(t *testing.T, meta state.Meta) {
	t.Helper()
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(dir, meta); err != nil {
		t.Fatal(err)
	}
	stopCleanup(store, &noKillManager{}, dir)
}

// noKillManager acepta cualquier Stop sin tocar nada: lo que se prueba es la
// retirada de la ruta, no el kill (que ya cubren los tests de process).
type noKillManager struct{}

func (*noKillManager) Stop(process.StopSpec) error { return nil }
func (*noKillManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}
func (*noKillManager) Evaluate(process.EvalSpec) process.Status { return process.StatusStopped }

// El seam de portless debe satisfacer Releaser: es lo que permite observar la
// retirada sin un binario real.
var _ portless.Releaser = (*recordingReleaser)(nil)
