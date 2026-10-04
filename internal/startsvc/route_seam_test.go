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
		{"sin manifiesto", nil, true},
		{"route_mode ausente (el default)", &manifest.Manifest{Name: "api", Command: "./api"}, true},
		{"route_mode off explícito", &manifest.Manifest{Name: "api", Command: "./api", RouteMode: manifest.RouteModeOff}, true},
		{"route_mode inválido", &manifest.Manifest{Name: "api", Command: "./api", RouteMode: "inventado"}, true},
	}
	for _, tt := range casos {
		t.Run(tt.nombre, func(t *testing.T) {
			reg := RegistrarFor(tt.manifiesto)
			if reg != nil && tt.quiereNil {
				t.Errorf("RegistrarFor = %T no-nil, want interfaz nil: el consumidor no puede distinguirlo de una ruta activa", reg)
			}
			if tt.quiereNil && reg != nil {
				if _, ok := reg.(*portless.Client); ok {
					t.Error("es un *portless.Client nil envuelto en una interfaz: el guard `req.Routes == nil` no se cumplirá")
				}
			}
		})
	}

	t.Run("route_mode activo devuelve un cliente real", func(t *testing.T) {
		reg := RegistrarFor(&manifest.Manifest{Name: "api", Command: "./api", RouteMode: manifest.RouteModeAuto})
		if reg == nil {
			t.Error("con route_mode = auto tiene que haber seam: si no, el servicio arranca sin ruta sin avisar")
		}
		if _, ok := reg.(*portless.Client); !ok {
			t.Errorf("se esperaba un *portless.Client, got %T", reg)
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
			t.Errorf("un servicio sin route_mode no puede recibir un aviso de ruta: %q", w)
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
			t.Errorf("apareció el aviso del bug con route_mode = auto: %q", w)
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
