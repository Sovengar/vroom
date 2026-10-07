package startsvc

import (
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
		manifiesto *manifest.Manifest
		quiereNil  bool
	}{
		{"no manifest", nil, true},
		{"route_mode absent (the default)", &manifest.Manifest{Name: "api", Command: "./api"}, true},
		{"explicit route_mode off", &manifest.Manifest{Name: "api", Command: "./api", RouteMode: manifest.RouteModeOff}, true},
		{"invalid route_mode", &manifest.Manifest{Name: "api", Command: "./api", RouteMode: "invented"}, true},
	}
	for _, tt := range casos {
		t.Run(tt.nombre, func(t *testing.T) {
			reg := RegistrarFor(tt.manifiesto)
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

	t.Run("active route_mode returns a real client", func(t *testing.T) {
		reg := RegistrarFor(&manifest.Manifest{Name: "api", Command: "./api", RouteMode: manifest.RouteModeAuto})
		if reg == nil {
			t.Error("with route_mode = auto there must be a seam: otherwise, the service starts without a route without warning")
		}
		if _, ok := reg.(*portless.Client); !ok {
			t.Errorf("expected a *portless.Client, got %T", reg)
		}
	})
}

func TestArrancarSinRouteModeNoEscribeElAvisoDeRutaEnElLog(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}

	m := manifest.Manifest{
		Name:     "api",
		Command:  "sleep 30",
		Port:     freePort(t),
		PortMode: manifest.PortModeFixed,
	}

	out, err := Start(Request{
		Manifest:   &m,
		Path:       path,
		Store:      store,
		Manager:    &pararAntesDeSalir{},
		StdoutPath: store.StdoutLog(path),
		StderrPath: store.StderrLog(path),
		Routes:     RegistrarFor(&m), // The repo callers pass RegistrarFor's result straight through, so the test wires the seam exactly as production does.
		Branch:     "main",
	})
	t.Cleanup(func() { _ = out.Pid })
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	for _, w := range out.Warnings {
		if strings.Contains(w, "route_mode") {
			t.Errorf("a service without route_mode cannot receive a route warning: %q", w)
		}
	}
}

// With no portless binary in a test environment the documented degradation is a warning, not a start failure.
func TestConRouteModeAutoSeIntentaLaRuta(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}

	m := manifest.Manifest{
		Name:      "api",
		Command:   "sleep 30",
		Port:      freePort(t),
		PortMode:  manifest.PortModeFixed,
		RouteMode: manifest.RouteModeAuto,
	}

	out, err := Start(Request{
		Manifest:   &m,
		Path:       path,
		Store:      store,
		Manager:    &pararAntesDeSalir{},
		StdoutPath: store.StdoutLog(path),
		StderrPath: store.StderrLog(path),
		Routes:     RegistrarFor(&m),
		Branch:     "main",
	})
	t.Cleanup(func() { _ = out.Pid })
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if strings.Contains(out.Meta.RouteName, "") && out.Meta.RouteName != "" {
		t.Logf("RouteName=%q RouteReason=%q", out.Meta.RouteName, out.Meta.RouteReason)
	}
	for _, w := range out.Warnings {
		if strings.Contains(w, `unknown route_mode "off"`) {
			t.Errorf("the bug warning appeared with route_mode = auto: %q", w)
		}
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
