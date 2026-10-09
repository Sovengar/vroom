package manifest

import "testing"

// A manifest without port_mode behaves as fixed: that is the backward-compatibility door, not an implementation detail.
func TestEffectivePortModeDefaults(t *testing.T) {
	cases := []struct {
		name string
		m    Manifest
		want string
	}{
		{"without port_mode with port", Manifest{Port: 8080}, PortModeFixed},
		{"without port_mode without port", Manifest{}, PortModeNone},
		{"explicit fixed", Manifest{Port: 8080, PortMode: PortModeFixed}, PortModeFixed},
		{"dynamic", Manifest{Port: 8080, PortMode: PortModeDynamic}, PortModeDynamic},
		{"explicit none", Manifest{Port: 8080, PortMode: PortModeNone}, PortModeNone},
		{"invalid propagates so Validate rejects it", Manifest{PortMode: "wat"}, "wat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.EffectivePortMode(); got != tc.want {
				t.Errorf("EffectivePortMode() = %q, want %q", got, tc.want)
			}
		})
	}
}

// HasPort tells "no port by design" apart from "not resolved yet".
func TestHasPort(t *testing.T) {
	if (&Manifest{Port: 8080}).HasPort() != true {
		t.Error("fixed with port must have port")
	}
	if (&Manifest{Port: 8080, PortMode: PortModeDynamic}).HasPort() != true {
		t.Error("dynamic declares the app default, so it has port")
	}
	if (&Manifest{PortMode: PortModeNone}).HasPort() {
		t.Error("none has no port, even if port is declared")
	}
	if (&Manifest{}).HasPort() {
		t.Error("without port has no port")
	}
}

func TestValidateRejectsUnknownPortMode(t *testing.T) {
	m := Manifest{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, PortMode: "random"}
	if err := m.Validate(); err == nil {
		t.Fatal("invalid port_mode must be rejected")
	}
}

// Riesgo 15: the cross-field rule the doc declared and the code never applied, now enforced as a genuine cross-field check.
func TestValidateHealthPathCrossField(t *testing.T) {
	rejected := []Manifest{
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, HealthPath: "/healthz"},                   // without port in any mode
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, PortMode: PortModeNone, HealthPath: "/h"}, // none + health_path
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, PortMode: PortModeDynamic},                // dynamic without default
	}
	for i, m := range rejected {
		if err := m.Validate(); err == nil {
			t.Errorf("manifest %d should have been rejected: %+v", i, m)
		}
	}
	accept := Manifest{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, HealthPath: "/healthz"}
	if err := accept.Validate(); err != nil {
		t.Errorf("health_path with port must be accepted: %v", err)
	}
}

func TestValidateBackwardsCompatible(t *testing.T) {
	legacy := []Manifest{
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 65535, ProcessPattern: "npm"},
	}
	for i, m := range legacy {
		if err := m.Validate(); err != nil {
			t.Errorf("legacy manifest %d rejected: %v", i, err)
		}
	}
}
