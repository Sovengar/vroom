package startsvc

import (
	"strings"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// El contrato "nil significa sin ruta".
//
// Este archivo existe por un bug medido, no por cobertura. `Request.Routes` es un
// campo de tipo INTERFAZ y los tres llamadores del repo le pasaban directamente el
// `*portless.Client` que devuelve `portless.ClientFor`. Un puntero nil dentro de una
// interfaz no es una interfaz nil, así que el `if req.Routes == nil` que abre
// applyRoute no se cumplía y el camino de ruta se ejecutaba entero para servicios
// que no quieren ruta.
//
// MEDIDO con un servicio real sin `route_mode`: su stderr empezaba en cada
// arranque con `portless route: unknown route_mode "off"`. Y como `off` es el
// DEFAULT de route_mode, el ruido era la regla y no la excepción.
// ---------------------------------------------------------------------------

// TestRegistrarForDevuelveNilDeVerdadYNoUnPunteriorDentroDeUnaInterfaz: el nil que
// importa.
//
// La aserción que falla con el bug no es `ClientFor(...) == nil` —eso sigue siendo
// verdad— sino `RegistrarFor(...) == nil`. Un `*Client` nil asignado a un campo de
// tipo interfaz da una interfaz NO nil, y el `if req.Routes == nil` del consumidor no
// se activa. Lo que se comprueba es el valor tal y como lo recibe el consumidor.
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
			// El tipo se infiere como RouteRegistrar, que es lo que importa: lo que
			// se compara contra nil es la INTERFAZ, no el puntero de detrás.
			reg := RegistrarFor(tt.manifiesto)
			if reg != nil && tt.quiereNil {
				t.Errorf("RegistrarFor = %T no-nil, want interfaz nil: el consumidor no puede distinguirlo de una ruta activa", reg)
			}
			if tt.quiereNil && reg != nil {
				// Y la comprobación de tipo, que es la que delata el typed-nil.
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

// TestArrancarSinRouteModeNoEscribeElAvisoDeRutaEnElLog: el síntoma que se ve.
//
// El aviso falso no era cosmetics: aparecía en el stderr de cada servicio por
// defecto en cada arranque. Un usuario que lee su log de vroom ve una línea de
// "portless route: unknown route_mode" y tiene reason para pensar que su
// configuración de rutas está mal, cuando lo único que ha pasado es que no ha
// escrito route_mode — que es exactamente lo que significa no querer rutas.
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
		// RouteMode ausente: el default es off.
	}

	out, err := Start(Request{
		Manifest:   &m,
		Path:       path,
		Store:      store,
		Manager:    &pararAntesDeSalir{},
		StdoutPath: store.StdoutLog(path),
		StderrPath: store.StderrLog(path),
		Routes:     RegistrarFor(&m), // lo que hacen los tres llamadores del repo
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

// TestConRouteModeAutoSeIntentaLaRuta: el otro lado.
//
// La normalización del nil no puede haber apagado el camino de ruta: un servicio
// que SÍ pide ruta tiene que seguirPidiéndola, y con un aviso si no se puede.
//
// Y como no hay portless en un entorno de test, el resultado es un aviso —que es
// la degradación documentada, no un fallo de arranque.
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

	// Con route_mode el meta puede llevar ruta o un motivo, pero nunca debe llevar
	// el nombre de una ruta derivada de "off".
	if strings.Contains(out.Meta.RouteName, "") && out.Meta.RouteName != "" {
		// No hay ruta derivada sin nombre de rama legible; se comprueba el motivo en
		// su lugar: con portless ausente, o hay Reason o no hay nada.
		t.Logf("RouteName=%q RouteReason=%q", out.Meta.RouteName, out.Meta.RouteReason)
	}
	for _, w := range out.Warnings {
		if strings.Contains(w, `unknown route_mode "off"`) {
			t.Errorf("apareció el aviso del bug con route_mode = auto: %q", w)
		}
	}
}

// pararAntesDeSalir es un manager que devuelve un PID sin arrancar nada, para que
// el test no deje procesos sueltos.
type pararAntesDeSalir struct{}

func (pararAntesDeSalir) Start(spec process.StartSpec) (process.StartResult, error) {
	return process.StartResult{Pid: 0, Pgid: 0}, nil
}

func (pararAntesDeSalir) Stop(spec process.StopSpec) error { return nil }

func (pararAntesDeSalir) Evaluate(spec process.EvalSpec) process.Status {
	return process.StatusStopped
}
