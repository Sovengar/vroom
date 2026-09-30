package cli

import (
	"encoding/json"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// marshalInfo es el JSON que ve un agente. Se marshaliza de verdad —no se
// inspecciona la struct— porque el contrato que importa es el que se publica.
func marshalInfo(t *testing.T, info ProjectInfo) map[string]any {
	t.Helper()
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// projectWith construye un scanner.Project configurado con el manifiesto dado,
// en dir: el Meta se persiste por ruta de proyecto, así que ambos tienen que
// coincidir o buildProjectInfo leería un Meta vacío.
func projectWith(dir string, m *manifest.Manifest) scanner.Project {
	return scanner.Project{Path: dir, Name: "proyecto", Configured: true, Manifest: m}
}

// ---- El JSON distingue INTENCIÓN de RESULTADO ----

// route_mode refleja lo que el manifiesto PIDE; el objeto route refleja lo que
// vroom CONSIGUIÓ. Confundirlos haría que una ruta degradada pareciera una
// ruta que el usuario no pidió.
func TestJSONSeparatesIntentFromOutcome(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	// El manifiesto PIDE named; lo que se consiguió fue degradado.
	m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080,
		PortMode: manifest.PortModeDynamic, RouteMode: manifest.RouteModeNamed, RouteName: "mi-url"}
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 42, Port: 8080, State: state.StateRunning,
		RouteName: "mi-url", RoutePort: 8080,
		RouteStatus: portless.StatusDegraded, RouteReason: portless.ReasonProxyNotRunning,
	}); err != nil {
		t.Fatal(err)
	}

	info := buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m))
	out := marshalInfo(t, info)

	if out["route_mode"] != manifest.RouteModeNamed {
		t.Errorf("route_mode debe publicar la INTENCIÓN, got %v", out["route_mode"])
	}
	route, ok := out["route"].(map[string]any)
	if !ok {
		t.Fatalf("debe haber objeto de ruta, got %v", out["route"])
	}
	if route["status"] != portless.StatusDegraded {
		t.Errorf("el objeto route debe reflejar el RESULTADO, got %v", route["status"])
	}
	if route["name"] != "mi-url" {
		t.Errorf("el nombre publicado es el PRETENDIDO, got %v", route["name"])
	}
}

// El nombre publicado NUNCA es una url: un agente que lo lea como nombre
// intentaría abrirlo como un host.
func TestPublishedRouteNameIsNeverAURL(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080, RouteMode: manifest.RouteModeAuto}
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 1, Port: 8080, State: state.StateRunning,
		RouteName: "feat.api", RouteStatus: portless.StatusRegistered, RouteURL: "https://feat.api.localhost",
	}); err != nil {
		t.Fatal(err)
	}
	info := buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m))
	route, _ := marshalInfo(t, info)["route"].(map[string]any)
	name, _ := route["name"].(string)
	if name != "feat.api" {
		t.Errorf("el nombre debe ser el nombre, no una url: %q", name)
	}
}

// ---- Una ruta no verificada NO publica url ----

// El campo AUSENTE es lo que hace el contrato honesto: un agente que lo lea no
// puede conectarse a una dirección que nadie comprobó.
func TestDegradedRoutePublishesNoURL(t *testing.T) {
	for _, reason := range []string{
		portless.ReasonPortlessMissing, portless.ReasonPortlessTimeout,
		portless.ReasonPortlessFailed, portless.ReasonProxyNotRunning,
		portless.ReasonProxyUnreachable, portless.ReasonRouteConflict,
		portless.ReasonPortUnresolved, portless.ReasonRouteNotServed,
	} {
		store := state.NewStoreAt(t.TempDir())
		dir := t.TempDir()
		m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080, RouteMode: manifest.RouteModeAuto}
		if err := store.SaveMeta(dir, state.Meta{
			Name: "p", Pid: 1, Port: 8080, State: state.StateRunning,
			RouteName: "p", RouteStatus: portless.StatusDegraded, RouteReason: reason,
			// Aunque un Meta viejo traiga una url, no se publica: el estado
			// manda sobre el residuo.
			RouteURL: "https://p.localhost",
		}); err != nil {
			t.Fatal(err)
		}
		out := marshalInfo(t, buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m)))
		route, ok := out["route"].(map[string]any)
		if !ok {
			t.Fatalf("[%s] debe haber objeto de ruta", reason)
		}
		if _, hasURL := route["url"]; hasURL {
			t.Errorf("[%s] una ruta degradada NO puede publicar url", reason)
		}
		if route["reason"] != reason {
			t.Errorf("[%s] debe haber motivo legible por máquina, got %v", reason, route["reason"])
		}
		if route["status"] == portless.StatusRegistered {
			t.Errorf("[%s] ningún campo puede afirmar que haya una ruta disponible", reason)
		}
	}
}

// Una ruta registrada sí publica su url: el contrato no es tímido, es exacto.
func TestRegisteredRoutePublishesItsURL(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080, RouteMode: manifest.RouteModeAuto}
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 1, Port: 8080, State: state.StateRunning,
		RouteName: "p", RoutePort: 8080,
		RouteStatus: portless.StatusRegistered, RouteURL: "https://p.localhost",
	}); err != nil {
		t.Fatal(err)
	}
	route, ok := marshalInfo(t, buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m)))["route"].(map[string]any)
	if !ok {
		t.Fatal("una ruta registrada debe publicarse")
	}
	if route["url"] != "https://p.localhost" {
		t.Errorf("una ruta registrada publica su url, got %v", route["url"])
	}
}

// ---- Sin contrato de ruta, no hay objeto de ruta ----

// El AUSENTE también es un estado: un manifiesto sin route_mode no afirma ni
// niega nada. Afirmar "no hay ruta" sería inventar un contrato que nadie pidió.
func TestNoRouteContractPublishesNoRouteObject(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080}
	out := marshalInfo(t, buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m)))
	if _, has := out["route"]; has {
		t.Error("sin contrato de ruta el objeto route debe estar AUSENTE")
	}
	if _, has := out["route_mode"]; has {
		t.Error("route_mode = off no es información: se omite")
	}
}

// La puerta de compatibilidad hacia atrás, en el JSON: un manifiesto que nunca
// oyó hablar de portless produce EXACTAMENTE el mismo JSON que antes.
func TestLegacyManifestJSONIsUnchanged(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080}
	if err := store.SaveMeta(dir, state.Meta{Name: "p", Pid: 7, Port: 8080, State: state.StateRunning}); err != nil {
		t.Fatal(err)
	}
	out := marshalInfo(t, buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m)))
	for _, forbidden := range []string{"route", "route_mode", "route_name", "route_url"} {
		if _, has := out[forbidden]; has {
			t.Errorf("un manifiesto legacy no debe publicar %q", forbidden)
		}
	}
	// Y los campos de puerto siguen igual.
	if out["port"].(float64) != 8080 {
		t.Errorf("el puerto debe seguir publicándose igual: %v", out["port"])
	}
}

// El JSON no afirma que exista una ruta sólo porque esté ESCRITA: un Meta con
// ruta pero sin verificación no la publica.
func TestJSONDoesNotAssertRouteJustBecauseItIsWritten(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	m := &manifest.Manifest{Name: "p", Command: "run", Port: 8080, RouteMode: manifest.RouteModeAuto}
	// La ruta existe en el estado de portless pero nadie la verificó.
	if err := store.SaveMeta(dir, state.Meta{
		Name: "p", Pid: 1, Port: 8080, State: state.StateRunning,
		RouteName: "p", RouteStatus: "", RouteReason: "",
	}); err != nil {
		t.Fatal(err)
	}
	out := marshalInfo(t, buildProjectInfo(&stubManager{}, store, nil, projectWith(dir, m)))
	if _, has := out["route"]; has {
		t.Error("una ruta escrita y no verificada no puede aparecer como disponible")
	}
}

// stubManager es un process.Manager inerte para estas pruebas: la superficie
// JSON se construye desde el Meta, no desde el proceso.
type stubManager struct{}

func (stubManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}
func (stubManager) Stop(process.StopSpec) error { return nil }
func (stubManager) Evaluate(process.EvalSpec) process.Status {
	return process.StatusRunning
}
