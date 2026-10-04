package manifest

import "testing"

// With off vroom does not even look for the portless binary, so the backward-compatibility door is total.
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

// An unknown route_mode propagates as off but is not accepted: the backward-compatibility door cannot be the door garbage walks in.
func TestValidateRejectsUnknownRouteMode(t *testing.T) {
	m := Manifest{Name: "x", Command: "run", Port: 8080, RouteMode: "prestable"}
	if err := m.Validate(); err == nil {
		t.Fatal("route_mode inválido debe rechazarse")
	}
}

// A route points at a port, so accepting route_mode without one would be a promise vroom cannot keep.
func TestValidateRouteModeRequiresAPort(t *testing.T) {
	rejected := []Manifest{
		{Name: "x", Command: "run", RouteMode: RouteModeAuto}, // sin puerto
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

// A route_name without named is a name nobody would use, so it is rejected instead of silently ignored.
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
