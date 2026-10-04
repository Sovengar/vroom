package tui

import (
	"strings"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// Only a verified route shows a URL: a degraded one has no address that leads anywhere, and a dead link is worse than none.
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

// nil must not panic because the details panel is built before any service state exists.
func TestDisplayRouteURLToleratesNilServiceState(t *testing.T) {
	p := scanner.Project{Path: "/p", Name: "p", Configured: true, Manifest: &manifest.Manifest{Name: "p"}}
	if got := displayRouteURL(p, nil); got != "" {
		t.Errorf("sin ServiceState no hay url, got %q", got)
	}
}

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
