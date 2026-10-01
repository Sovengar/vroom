package tui

import (
	"testing"

	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// El stop de la TUI tiene que RETIRAR la ruta.
//
// El reviewer comprobó que borrando los tres call sites de Release la suite
// seguía verde, así que la decisión 13 del ADR no la verificaba nada. Estos
// tests cierran el hueco para el camino de la TUI: si mañana alguien quita el
// release de stopCmd, esto se pone rojo.
//
// Y un route que no se retira al parar es exactamente el daño que esta feature
// existe para evitar: una dirección apuntando a un puerto muerto.
// ---------------------------------------------------------------------------

// Un stub que registra las retiradas. Las variables de costura viven en
// route.go (producción) porque releaseRoute tiene que poder leerlas; aquí sólo
// se instalan y se limpian.
type recordingReleaser struct{ removed []string }

func (r *recordingReleaser) RemoveAbsent(name string) error {
	r.removed = append(r.removed, name)
	return nil
}

// installRouteStub apunta la retirada de la TUI al doble del test.
func installRouteStub(t *testing.T, rec *recordingReleaser) {
	t.Helper()
	t.Cleanup(func() {
		tuiReleaseStub = nil
		routeStubInstalled = false
	})
	tuiReleaseStub = rec.RemoveAbsent
	routeStubInstalled = true
}

// El stop de un servicio con ruta registrada la retira.
func TestStopRemovesTheServiceRoute(t *testing.T) {
	rec := &recordingReleaser{}
	installRouteStub(t, rec)

	dir := t.TempDir()
	store := state.NewStoreAt(t.TempDir())
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 0, Pgid: 0, Port: 0,
		State: state.StateRunning, RouteName: "p-route", RoutePort: 4321, RouteOwned: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}

	msg := stopCmd(store, &stubManager{}, dir, "")()

	if len(rec.removed) != 1 || rec.removed[0] != "p-route" {
		t.Errorf("el stop debe retirar la ruta del servicio, got %v", rec.removed)
	}
	if m, ok := msg.(stoppedMsg); !ok || m.err != nil {
		t.Errorf("parar no puede fallar por la retirada de una ruta: %#v", msg)
	}

	// Y revoca la propiedad: sin esto el Meta seguiría declarando nuestra una
	// ruta ya retirada, que es lo que permite que el arranque siguiente pise la
	// ruta de otro que haya tomado el nombre.
	meta, err := store.LoadMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RouteOwned {
		t.Error("tras retirar la ruta la propiedad debe quedar revocada en el Meta")
	}
	if meta.RouteName == "" {
		t.Error("el handle de reconciliación debe conservarse aunque la propiedad se revoque")
	}
}

// Un servicio SIN ruta no invoca la retirada: RouteName vacío es la señal de que
// no hubo contrato de ruta, y una llamada con nombre vacío sería ruido.
func TestStopWithoutRouteDoesNotCallRelease(t *testing.T) {
	rec := &recordingReleaser{}
	installRouteStub(t, rec)

	dir := t.TempDir()
	store := state.NewStoreAt(t.TempDir())
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", State: state.StateRunning, // sin RouteName
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}

	stopCmd(store, &stubManager{}, dir, "")()

	if len(rec.removed) != 0 {
		t.Errorf("sin ruta registrada no debe retirarse nada, got %v", rec.removed)
	}
}
