package manifest

import "testing"

// The generation is the ONE start axis: it decides both the address and the port, so a manifest that declares neither keeps the pre-url_generation behaviour (a port means by_port, port 0 means none) and stays byte-compatible.
func TestEffectiveURLGenerationDefaults(t *testing.T) {
	cases := []struct {
		name       string
		m          Manifest
		isWorktree bool
		want       string
	}{
		{"legacy port means by_port", Manifest{Port: 8080}, false, URLGenByPort},
		{"legacy port 0 means none", Manifest{}, false, URLGenNone},
		{"explicit by_port", Manifest{Port: 8080, URLGeneration: URLGenByPort}, false, URLGenByPort},
		{"explicit by_hostname", Manifest{Port: 8080, URLGeneration: URLGenByHostname, RouteName: "api"}, false, URLGenByHostname},
		{"explicit by_workspace_hostname", Manifest{Port: 8080, URLGeneration: URLGenByWorkspaceHostname}, false, URLGenByWorkspaceHostname},
		{"explicit ladder", Manifest{Port: 8080, URLGeneration: URLGenByHostnameOrWorkspace, RouteName: "api"}, false, URLGenByHostnameOrWorkspace},
		{"explicit none", Manifest{URLGeneration: URLGenNone}, false, URLGenNone},
		{"main copy ignores the worktree policy", Manifest{Port: 8080, URLGeneration: URLGenByPort, Worktrees: Worktrees{URLGeneration: URLGenByWorkspaceHostname}}, false, URLGenByPort},
		{"worktree takes the override", Manifest{Port: 8080, URLGeneration: URLGenByPort, Worktrees: Worktrees{URLGeneration: URLGenByWorkspaceHostname}}, true, URLGenByWorkspaceHostname},
		{"worktree without override inherits", Manifest{Port: 8080, URLGeneration: URLGenByHostname, RouteName: "api"}, true, URLGenByHostname},
		{"invalid propagates so Validate rejects it", Manifest{Port: 8080, URLGeneration: "wat"}, false, "wat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.EffectiveURLGeneration(tc.isWorktree); got != tc.want {
				t.Errorf("EffectiveURLGeneration(%v) = %q, want %q", tc.isWorktree, got, tc.want)
			}
		})
	}
}

// The port mode is DERIVED from the generation, never declared apart: by_port binds the declared port, the hostname generations hand out an ephemeral one, none has none.
func TestPortModeDerivesFromGeneration(t *testing.T) {
	cases := []struct {
		gen  string
		want string
	}{
		{URLGenByPort, PortModeFixed},
		{URLGenByHostname, PortModeDynamic},
		{URLGenByWorkspaceHostname, PortModeDynamic},
		{URLGenByHostnameOrWorkspace, PortModeDynamic},
		{URLGenNone, PortModeNone},
	}
	for _, tc := range cases {
		if got := PortMode(tc.gen); got != tc.want {
			t.Errorf("PortMode(%q) = %q, want %q", tc.gen, got, tc.want)
		}
	}
}

// PublishesURL is the portless gate: the two non-URL generations must never resolve the binary, exactly like the retired route_mode = "off".
func TestPublishesURLGate(t *testing.T) {
	publishing := []string{URLGenByHostname, URLGenByWorkspaceHostname, URLGenByHostnameOrWorkspace}
	for _, gen := range publishing {
		if !PublishesURL(gen) {
			t.Errorf("PublishesURL(%q) must be true", gen)
		}
	}
	for _, gen := range []string{URLGenByPort, URLGenNone, "", "wat"} {
		if PublishesURL(gen) {
			t.Errorf("PublishesURL(%q) must be false", gen)
		}
	}
}

// HasPort tells "no port by design" apart from "not resolved yet": none has no port even if a port is declared.
func TestHasPort(t *testing.T) {
	if (&Manifest{Port: 8080}).HasPort() != true {
		t.Error("by_port (legacy default) must have port")
	}
	if (&Manifest{Port: 8080, URLGeneration: URLGenByHostname, RouteName: "api"}).HasPort() != true {
		t.Error("hostname generations declare the app default, so they have port")
	}
	if (&Manifest{Port: 8080, URLGeneration: URLGenNone}).HasPort() {
		t.Error("none has no port, even if port is declared")
	}
	if (&Manifest{}).HasPort() {
		t.Error("without port has no port")
	}
}

func TestValidateRejectsUnknownGeneration(t *testing.T) {
	for _, m := range []Manifest{
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, URLGeneration: "random"},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, Worktrees: Worktrees{URLGeneration: "random"}},
	} {
		if err := m.Validate(); err == nil {
			t.Fatalf("invalid url_generation must be rejected: %+v", m)
		}
	}
}

// A hostname generation IS its name, so route_name is mandatory there and rejected anywhere else instead of being silently ignored.
func TestValidateRouteNameRequiresANameGeneration(t *testing.T) {
	rejected := []Manifest{
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, RouteName: "api"}, // legacy by_port
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, URLGeneration: URLGenByWorkspaceHostname, RouteName: "api"},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, URLGeneration: URLGenNone, RouteName: "api"},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, URLGeneration: URLGenByHostname}, // name required, absent
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, URLGeneration: URLGenByHostnameOrWorkspace},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, URLGeneration: URLGenByPort, Worktrees: Worktrees{URLGeneration: URLGenByHostname}}, // override needs the name too
	}
	for i, m := range rejected {
		if err := m.Validate(); err == nil {
			t.Errorf("manifest %d should have been rejected: %+v", i, m)
		}
	}
	accepted := []Manifest{
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, URLGeneration: URLGenByHostname, RouteName: "api"},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, URLGeneration: URLGenByHostnameOrWorkspace, RouteName: "api"},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, URLGeneration: URLGenByPort, Worktrees: Worktrees{URLGeneration: URLGenByHostnameOrWorkspace}, RouteName: "api"},
	}
	for i, m := range accepted {
		if err := m.Validate(); err != nil {
			t.Errorf("manifest %d must be accepted: %v", i, err)
		}
	}
}

// Every generation but none needs a declared default port behind PORT=${PORT:-N}, and a worktree override is bound by the same rule.
func TestValidateGenerationRequiresAPort(t *testing.T) {
	rejected := []Manifest{
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, URLGeneration: URLGenByPort}, // explicit by_port without port
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, URLGeneration: URLGenByWorkspaceHostname},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, URLGeneration: URLGenByHostname, RouteName: "api"},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, URLGeneration: URLGenByPort, Worktrees: Worktrees{URLGeneration: URLGenByWorkspaceHostname}},
	}
	for i, m := range rejected {
		if err := m.Validate(); err == nil {
			t.Errorf("manifest %d without a declared port should have been rejected: %+v", i, m)
		}
	}
	if err := (&Manifest{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, URLGeneration: URLGenNone}).Validate(); err != nil {
		t.Errorf("none needs no port: %v", err)
	}
	if err := (&Manifest{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}}).Validate(); err != nil {
		t.Errorf("a bare manifest with no port is the legacy headless door and must validate: %v", err)
	}
}

// Riesgo 15: the cross-field rule the doc declared and the code never applied, now enforced as a genuine cross-field check.
func TestValidateHealthPathCrossField(t *testing.T) {
	rejected := []Manifest{
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, HealthPath: "/healthz"}, // without port
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, URLGeneration: URLGenNone, HealthPath: "/h"},
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

// A manifest that declares nothing behaves exactly as before the field existed: the backward-compatibility door is total.
func TestValidateBackwardsCompatible(t *testing.T) {
	legacy := []Manifest{
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 65535, ProcessPattern: "npm"},
		{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}, Port: 8080, HealthPath: "/healthz"},
	}
	for i, m := range legacy {
		if err := m.Validate(); err != nil {
			t.Errorf("legacy manifest %d rejected: %v", i, err)
		}
		if got := m.EffectiveURLGeneration(false); got != URLGenByPort {
			t.Errorf("legacy manifest %d must resolve to by_port, got %q", i, got)
		}
	}
	if err := (&Manifest{Name: "x", Commands: Commands{Start: StartCommand{Run: "run"}}}).Validate(); err != nil {
		t.Errorf("a headless legacy manifest must still validate: %v", err)
	}
}
