package manifest

import "testing"

// With off vroom does not even look for the portless binary, so the backward-compatibility door is total.
func TestEffectiveRouteModeDefaults(t *testing.T) {
	cases := []struct {
		name string
		m    Manifest
		want string
	}{
		{"without route_mode", Manifest{Name: "x", Command: "run", Port: 8080}, RouteModeOff},
		{"explicit off", Manifest{RouteMode: RouteModeOff}, RouteModeOff},
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
		t.Fatal("invalid route_mode must be rejected")
	}
}

// A route points at a port, so accepting route_mode without one would be a promise vroom cannot keep.
func TestValidateRouteModeRequiresAPort(t *testing.T) {
	rejected := []Manifest{
		{Name: "x", Command: "run", RouteMode: RouteModeAuto}, // without port
		{Name: "x", Command: "run", PortMode: PortModeNone, RouteMode: RouteModeAuto},
		{Name: "x", Command: "run", PortMode: PortModeDynamic, RouteMode: RouteModeAuto}, // without default
	}
	for i, m := range rejected {
		if err := m.Validate(); err == nil {
			t.Errorf("manifest %d route_mode without port should have been rejected: %+v", i, m)
		}
	}
	accept := Manifest{Name: "x", Command: "run", Port: 8080, RouteMode: RouteModeAuto}
	if err := accept.Validate(); err != nil {
		t.Errorf("route_mode with port must be accepted: %v", err)
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
			t.Errorf("manifest %d route_name without named should have been rejected: %+v", i, m)
		}
	}
	accept := Manifest{Name: "x", Command: "run", Port: 8080, RouteMode: RouteModeNamed, RouteName: "mi-nombre"}
	if err := accept.Validate(); err != nil {
		t.Errorf("route_name with named must be accepted: %v", err)
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
			t.Errorf("legacy manifest %d rejected: %v", i, err)
		}
		if got := m.EffectiveRouteMode(); got != RouteModeOff {
			t.Errorf("legacy manifest %d must stay off, got %q", i, got)
		}
	}
}
