package startsvc

import (
	"os"
	"strings"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/state"
)

func TestRegistrarForDevuelveNilDeVerdadYNoUnPunteriorDentroDeUnaInterfaz(t *testing.T) {
	casos := []struct {
		nombre     string
		generacion string
		quiereNil  bool
	}{
		{"empty generation (the default by_port)", "", true},
		{"by_port", manifest.URLGenByPort, true},
		{"none", manifest.URLGenNone, true},
		{"invalid generation", "invented", true},
	}
	for _, tt := range casos {
		t.Run(tt.nombre, func(t *testing.T) {
			reg := RegistrarFor(tt.generacion)
			if reg != nil && tt.quiereNil {
				t.Errorf("RegistrarFor = %T non-nil, want nil interface: the consumer cannot distinguish it from an active route", reg)
			}
			if tt.quiereNil && reg != nil {
				if _, ok := reg.(*portless.Client); ok {
					t.Error("it is a nil *portless.Client wrapped in an interface: the guard `req.Routes == nil` will not hold")
				}
			}
		})
	}

	t.Run("a URL-publishing generation returns a real client", func(t *testing.T) {
		for _, gen := range []string{
			manifest.URLGenByHostname,
			manifest.URLGenByWorkspaceHostname,
			manifest.URLGenByHostnameOrWorkspace,
		} {
			reg := RegistrarFor(gen)
			if reg == nil {
				t.Errorf("with url_generation = %q there must be a seam: otherwise, the service starts without a route without warning", gen)
			}
			if _, ok := reg.(*portless.Client); !ok {
				t.Errorf("expected a *portless.Client, got %T", reg)
			}
		}
	})
}

// A start that publishes no URL must not emit a route warning: the registrar seam stays untouched.
func TestArrancarEnByPortNoEscribeElAvisoDeRutaEnElLog(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}

	m := manifest.Manifest{
		Name:          "api",
		Command:       "sleep 30",
		Port:          freePort(t),
		URLGeneration: manifest.URLGenByPort,
	}

	out, err := Start(Request{
		Manifest:   &m,
		Path:       path,
		Store:      store,
		Manager:    &pararAntesDeSalir{},
		StdoutPath: store.StdoutLog(path),
		StderrPath: store.StderrLog(path),
		// The repo callers pass the factory straight through, so the test wires the seam exactly as production does.
		Registrar: RegistrarFor,
		Branch:    "main",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer stopOne(t, store, path)

	for _, w := range out.Warnings {
		if strings.Contains(w, "route") {
			t.Errorf("a by_port service cannot receive a route warning: %q", w)
		}
	}
}

// With no portless binary in a test environment the documented degradation is a warning, not a start failure.
// The generation publishes a URL, so the registrar resolves; the helper never binds a port, which the
// discovery window reports rather than failing the start.
func TestConGeneracionQuePublicaSeIntentaLaRuta(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}

	m := manifest.Manifest{
		Name:          "api",
		Command:       shellQuote(os.Args[0]) + " -test.run=^TestHelperService$",
		Port:          freePort(t),
		URLGeneration: manifest.URLGenByWorkspaceHostname,
	}
	t.Setenv(helperEnv, "honor-port-now")

	out, err := Start(Request{
		Manifest:   &m,
		Path:       path,
		Store:      store,
		Manager:    process.NewManager(),
		StdoutPath: store.StdoutLog(path),
		StderrPath: store.StderrLog(path),
		Registrar:  RegistrarFor,
		Branch:     "main",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer stopOne(t, store, path)

	// The route result must reach the CALLER, not only the store: out.Meta used to be
	// copied before applyRoute, so a workspace start returned the previous start's
	// route (empty on a cold one) and the assertion that was here could never fail.
	if out.Meta.RouteName == "" || out.Meta.RouteStatus == "" {
		t.Errorf("the returned Meta must carry THIS start's route result, got RouteName=%q RouteStatus=%q",
			out.Meta.RouteName, out.Meta.RouteStatus)
	}
	for _, w := range out.Warnings {
		if strings.Contains(w, `unknown route_mode "off"`) {
			t.Errorf("the bug warning appeared with a URL-publishing generation: %q", w)
		}
	}
}

// A publishing start must hand the caller the route it just resolved. out.Meta was copied before
// applyRoute, so the TUI painted the PREVIOUS start's route (empty on a cold one) while the store
// already held the new one; the dynamic path always re-assigns out.Meta after applyRoute, which is
// why only this branch drifted.
func TestFixedPortStartHandsTheRouteResultToTheCaller(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}

	m := manifest.Manifest{
		Name:          "api",
		Command:       shellQuote(os.Args[0]) + " -test.run=^TestHelperService$",
		Port:          freePort(t),
		URLGeneration: manifest.URLGenByWorkspaceHostname,
	}
	t.Setenv(helperEnv, "honor-port-now")
	routes := &resultSpy{result: registeredRoute()}

	out, err := Start(Request{
		Manifest:   &m,
		Path:       path,
		Store:      store,
		Manager:    process.NewManager(),
		StdoutPath: store.StdoutLog(path),
		StderrPath: store.StderrLog(path),
		Registrar:  func(string) RouteRegistrar { return routes },
		Branch:     "main",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Spawning for real means cleaning up for real: without this the helper (and its reserved port) outlived the test and the NEXT test's hygiene assertion failed.
	defer stopOne(t, store, path)

	// by_workspace_hostname on branch main derives main.api, and that name is what the caller must see.
	if out.Meta.RouteName != "main.api" {
		t.Errorf("RouteName = %q, want %q", out.Meta.RouteName, "main.api")
	}
	if out.Meta.RouteStatus != portless.StatusRegistered {
		t.Errorf("RouteStatus = %q, want %q", out.Meta.RouteStatus, portless.StatusRegistered)
	}
	if !out.Meta.RouteOwned {
		t.Error("a registered route is ours: the caller must receive the grant, not only the store")
	}
	if out.Meta.RouteURL == "" {
		t.Error("the verified URL must reach the caller, not only the store")
	}

	// Stop revokes from the store and the TUI paints from the result: two views of one start must not disagree.
	persisted, err := store.LoadMeta(path)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if persisted.RouteName != out.Meta.RouteName ||
		persisted.RouteStatus != out.Meta.RouteStatus ||
		persisted.RouteOwned != out.Meta.RouteOwned ||
		persisted.RouteURL != out.Meta.RouteURL {
		t.Errorf("the persisted route and the returned one must agree:\n persisted %+v\n returned %+v",
			persisted, out.Meta)
	}
}

// pararAntesDeSalir fakes a process manager so the suite spawns nothing and leaves no stray processes behind.
type pararAntesDeSalir struct{}

func (pararAntesDeSalir) Start(spec process.StartSpec) (process.StartResult, error) {
	return process.StartResult{Pid: 0, Pgid: 0}, nil
}

func (pararAntesDeSalir) Stop(spec process.StopSpec) error { return nil }

func (pararAntesDeSalir) Evaluate(spec process.EvalSpec) process.Status {
	return process.StatusStopped
}
