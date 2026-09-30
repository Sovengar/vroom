package manifest

import "testing"

// route_mode es tri-estado con default off, igual que port_mode pero con la
// diferencia que importa: con off vroom NI SIQUIERA busca el binario de
// portless, así que la puerta de compatibilidad hacia atrás es total.
func TestEffectiveRouteModeDefaults(t *testing.T) {
	cases := []struct {
		name string
		m    Manifest
		want string
	}{
		{"sin route_mode", Manifest{Name: "x", Command: "run", Port: 8080}, RouteModeOff},
		{"off explícito", Manifest{RouteMode: RouteModeOff}, RouteModeOff},
		{"auto", Manifest{RouteMode: RouteModeAuto}, RouteModeAuto},
		{"named", Manifest{RouteMode: RouteModeNamed}, RouteModeNamed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.EffectiveRouteMode(); got != tc.want {
				t.Errorf("EffectiveRouteMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

// route_mode inválido se propaga como off pero NO se acepta: la puerta de
// atrás no puede ser la puerta por la que entra un valor basura.
func TestValidateRejectsUnknownRouteMode(t *testing.T) {
	m := Manifest{Name: "x", Command: "run", Port: 8080, RouteMode: "prestable"}
	if err := m.Validate(); err == nil {
		t.Fatal("route_mode inválido debe rechazarse")
	}
}

// Una ruta apunta a un puerto: aceptar route_mode sin puerto sería una promesa
// que vroom no puede cumplir.
func TestValidateRouteModeRequiresAPort(t *testing.T) {
	rejected := []Manifest{
		{Name: "x", Command: "run", RouteMode: RouteModeAuto},                 // sin puerto
		{Name: "x", Command: "run", PortMode: PortModeNone, RouteMode: RouteModeAuto},
		{Name: "x", Command: "run", PortMode: PortModeDynamic, RouteMode: RouteModeAuto}, // sin default
	}
	for i, m := range rejected {
		if err := m.Validate(); err == nil {
			t.Errorf("manifiesto %d route_mode sin puerto debía rechazarse: %+v", i, m)
		}
	}
	accept := Manifest{Name: "x", Command: "run", Port: 8080, RouteMode: RouteModeAuto}
	if err := accept.Validate(); err != nil {
		t.Errorf("route_mode con puerto debe aceptarse: %v", err)
	}
}

// route_name sin named es un nombre que nadie usaría. Se rechaza en vez de
// ignorarse en silencio.
func TestValidateRouteNameRequiresNamedMode(t *testing.T) {
	rejected := []Manifest{
		{Name: "x", Command: "run", Port: 8080, RouteName: "mi-nombre"},
		{Name: "x", Command: "run", Port: 8080, RouteMode: RouteModeAuto, RouteName: "mi-nombre"},
		{Name: "x", Command: "run", Port: 8080, RouteMode: RouteModeOff, RouteName: "mi-nombre"},
	}
	for i, m := range rejected {
		if err := m.Validate(); err == nil {
			t.Errorf("manifiesto %d route_name sin named debía rechazarse: %+v", i, m)
		}
	}
	accept := Manifest{Name: "x", Command: "run", Port: 8080, RouteMode: RouteModeNamed, RouteName: "mi-nombre"}
	if err := accept.Validate(); err != nil {
		t.Errorf("route_name con named debe aceptarse: %v", err)
	}
}

// LA PUERTA DE COMPATIBILIDAD HACIA ATRÁS: todo manifiesto que se aceptaba
// antes sigue aceptándose, y sin route_mode no se busca el binario.
func TestManifestWithoutRouteModeIsUnchanged(t *testing.T) {
	legacy := []Manifest{
		{Name: "x", Command: "run"},
		{Name: "x", Command: "run", Port: 8080},
		{Name: "x", Command: "run", Port: 8080, PortMode: PortModeDynamic},
		{Name: "x", Command: "run", Port: 65535, HealthPath: "/healthz"},
		{Name: "x", Command: "run", PortMode: PortModeNone},
	}
	for i, m := range legacy {
		if err := m.Validate(); err != nil {
			t.Errorf("manifiesto legacy %d rechazado: %v", i, err)
		}
		if got := m.EffectiveRouteMode(); got != RouteModeOff {
			t.Errorf("manifiesto legacy %d debe quedar en off, got %q", i, got)
		}
	}
}
