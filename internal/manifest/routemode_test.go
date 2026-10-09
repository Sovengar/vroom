package manifest

import "testing"

// With off vroom does not even look for the portless binary, so the backward-compatibility door is total.
func TestEffectiveRouteModeDefaults(t *testing.T) {
	cases := []struct {
		name string
		m    Manifest
		want string
	}{
		{"without route_mode", Manifest{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080}, RouteModeOff},
		{"explicit off", Manifest{RouteMode: RouteModeOff}, RouteModeOff},
		{"auto", Manifest{RouteMode: RouteModeAuto}, RouteModeAuto},
		{"named with auto fallback", Manifest{RouteMode: RouteModeNamedWithAutoFallback}, RouteModeNamedWithAutoFallback},
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
	m := Manifest{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, RouteMode: "prestable"}
	if err := m.Validate(); err == nil {
		t.Fatal("invalid route_mode must be rejected")
	}
}

// A route points at a port, so accepting route_mode without one would be a promise vroom cannot keep.
func TestValidateRouteModeRequiresAPort(t *testing.T) {
	rejected := []Manifest{
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, RouteMode: RouteModeAuto}, // without port
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, PortMode: PortModeNone, RouteMode: RouteModeAuto},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, PortMode: PortModeDynamic, RouteMode: RouteModeAuto}, // without default
	}
	for i, m := range rejected {
		if err := m.Validate(); err == nil {
			t.Errorf("manifest %d route_mode without port should have been rejected: %+v", i, m)
		}
	}
	accept := Manifest{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, RouteMode: RouteModeAuto}
	if err := accept.Validate(); err != nil {
		t.Errorf("route_mode with port must be accepted: %v", err)
	}
}

// A route_name without the ladder mode is a name nobody would use, so it is rejected instead of silently ignored.
func TestValidateRouteNameRequiresNamedMode(t *testing.T) {
	rejected := []Manifest{
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, RouteName: "mi-nombre"},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, RouteMode: RouteModeAuto, RouteName: "mi-nombre"},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, RouteMode: RouteModeOff, RouteName: "mi-nombre"},
	}
	for i, m := range rejected {
		if err := m.Validate(); err == nil {
			t.Errorf("manifest %d route_name without named should have been rejected: %+v", i, m)
		}
	}
	accept := Manifest{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, RouteMode: RouteModeNamedWithAutoFallback, RouteName: "mi-nombre"}
	if err := accept.Validate(); err != nil {
		t.Errorf("route_name with named must be accepted: %v", err)
	}
}

// The old value is gone on purpose: nothing on this machine uses route_mode = "named", and accepting it as an alias would keep two spellings of one mode alive forever.
func TestValidateRejectsTheRetiredNamedValue(t *testing.T) {
	m := Manifest{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, RouteMode: "named", RouteName: "mi-nombre"}
	if err := m.Validate(); err == nil {
		t.Error(`route_mode = "named" was renamed to "named_with_auto_fallback" and must not be accepted silently`)
	}
}

func TestManifestWithoutRouteModeIsUnchanged(t *testing.T) {
	legacy := []Manifest{
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, PortMode: PortModeDynamic},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 65535, HealthPath: "/healthz"},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, PortMode: PortModeNone},
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
