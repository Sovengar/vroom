package manifest

import "testing"

// A manifest without port_mode behaves as fixed: that is the backward-compatibility door, not an implementation detail.
func TestEffectivePortModeDefaults(t *testing.T) {
	cases := []struct {
		name string
		m    Manifest
		want string
	}{
		{"sin port_mode con puerto", Manifest{Port: 8080}, PortModeFixed},
		{"sin port_mode sin puerto", Manifest{}, PortModeNone},
		{"fixed explícito", Manifest{Port: 8080, PortMode: PortModeFixed}, PortModeFixed},
		{"dynamic", Manifest{Port: 8080, PortMode: PortModeDynamic}, PortModeDynamic},
		{"none explícito", Manifest{Port: 8080, PortMode: PortModeNone}, PortModeNone},
		{"inválido se propaga para que Validate lo rechace", Manifest{PortMode: "wat"}, "wat"},
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
		t.Error("fixed con puerto debe tener puerto")
	}
	if (&Manifest{Port: 8080, PortMode: PortModeDynamic}).HasPort() != true {
		t.Error("dynamic declara el default de la app, luego tiene puerto")
	}
	if (&Manifest{PortMode: PortModeNone}).HasPort() {
		t.Error("none no tiene puerto, aunque declare port")
	}
	if (&Manifest{}).HasPort() {
		t.Error("sin puerto no tiene puerto")
	}
}

func TestValidateRejectsUnknownPortMode(t *testing.T) {
	m := Manifest{Name: "x", Command: "run", Port: 8080, PortMode: "random"}
	if err := m.Validate(); err == nil {
		t.Fatal("port_mode inválido debe rechazarse")
	}
}

// Riesgo 15: the cross-field rule the doc declared and the code never applied, now enforced as a genuine cross-field check.
func TestValidateHealthPathCrossField(t *testing.T) {
	rejected := []Manifest{
		{Name: "x", Command: "run", HealthPath: "/healthz"},                   // sin puerto en ningún modo
		{Name: "x", Command: "run", PortMode: PortModeNone, HealthPath: "/h"}, // none + health_path
		{Name: "x", Command: "run", PortMode: PortModeDynamic},                // dynamic sin default
	}
	for i, m := range rejected {
		if err := m.Validate(); err == nil {
			t.Errorf("manifiesto %d debía rechazarse: %+v", i, m)
		}
	}
	accept := Manifest{Name: "x", Command: "run", Port: 8080, HealthPath: "/healthz"}
	if err := accept.Validate(); err != nil {
		t.Errorf("health_path con puerto debe aceptarse: %v", err)
	}
}

func TestValidateBackwardsCompatible(t *testing.T) {
	legacy := []Manifest{
		{Name: "x", Command: "run"},
		{Name: "x", Command: "run", Port: 8080},
		{Name: "x", Command: "run", Port: 65535, ProcessPattern: "npm"},
	}
	for i, m := range legacy {
		if err := m.Validate(); err != nil {
			t.Errorf("manifiesto legacy %d rechazado: %v", i, err)
		}
	}
}
