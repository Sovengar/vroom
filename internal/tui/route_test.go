package tui

import (
	"strings"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// displayRouteURL sólo muestra una url VERIFICADA. Una ruta degradada no tiene
// ninguna, y el usuario no debe ver una dirección que no lleva a nada.
func TestDisplayRouteURLOnlyWhenVerified(t *testing.T) {
	cases := []struct {
		name string
		meta state.Meta
		want string
	}{
		{"verificada", state.Meta{RouteStatus: portless.StatusRegistered, RouteURL: "https://api.localhost"}, "https://api.localhost"},
		{"degradada", state.Meta{RouteStatus: portless.StatusDegraded, RouteReason: portless.ReasonProxyNotRunning}, ""},
		{"escrita pero no verificada", state.Meta{RouteName: "api", RouteURL: "https://api.localhost"}, ""},
		{"sin ruta", state.Meta{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := scanner.Project{Path: "/p", Name: "p", Configured: true, Manifest: &manifest.Manifest{Name: "p"}}
			if got := displayRouteURL(p, &ServiceState{Meta: tc.meta}); got != tc.want {
				t.Errorf("displayRouteURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

// nil no debe hacer panic: el panel se construye antes de que exista estado.
func TestDisplayRouteURLToleratesNilServiceState(t *testing.T) {
	p := scanner.Project{Path: "/p", Name: "p", Configured: true, Manifest: &manifest.Manifest{Name: "p"}}
	if got := displayRouteURL(p, nil); got != "" {
		t.Errorf("sin ServiceState no hay url, got %q", got)
	}
}

// La url aparece junto al puerto en el panel de detalles, y sólo si se ha
// verificado: el usuario tiene que ver a dónde ir, pero no una dirección falsa.
func TestDetailsShowVerifiedURLNextToPort(t *testing.T) {
	m, store := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := m.selected().Path
	if err := store.SaveMeta(path, state.Meta{
		Name: "tienda-api", Port: 4321, Pid: 11, State: state.StateRunning,
		RouteStatus: portless.StatusRegistered, RouteURL: "https://api.localhost",
	}); err != nil {
		t.Fatal(err)
	}
	m.services[path] = &ServiceState{Meta: state.Meta{
		Name: "tienda-api", Port: 4321, Pid: 11, State: state.StateRunning,
		RouteStatus: portless.StatusRegistered, RouteURL: "https://api.localhost",
	}}

	joined := strings.Join(m.detailsLines(m.rightW), "\n")
	if !strings.Contains(joined, "https://api.localhost") {
		t.Errorf("una ruta verificada debe verse en el panel de detalles:\n%s", joined)
	}
	if !strings.Contains(joined, "4321") {
		t.Errorf("el puerto debe seguir viéndose:\n%s", joined)
	}
}

// Una ruta degradada no se muestra: no hay url que ofrecer.
func TestDetailsHideDegradedURL(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := m.selected().Path
	m.services[path] = &ServiceState{Meta: state.Meta{
		Name: "tienda-api", Port: 4321, Pid: 11, State: state.StateRunning,
		RouteStatus: portless.StatusDegraded, RouteReason: portless.ReasonProxyNotRunning,
	}}

	joined := strings.Join(m.detailsLines(m.rightW), "\n")
	if strings.Contains(joined, "url:") {
		t.Errorf("una ruta degradada no debe mostrar url:\n%s", joined)
	}
	if !strings.Contains(joined, "4321") {
		t.Errorf("el puerto debe seguir viéndose aunque la ruta degrade:\n%s", joined)
	}
}
