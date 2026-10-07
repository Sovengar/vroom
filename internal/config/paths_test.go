package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three sources target different premises: VROOM_CONFIG for one process (a runner, a service), XDG for a user, ~/.config for the normal case.
func TestPathSigueElOrdenDeLasTresFuentes(t *testing.T) {
	t.Run("VROOM_CONFIG takes precedence", func(t *testing.T) {
		t.Setenv("VROOM_CONFIG", "/opt/vroom/config.toml")
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		t.Setenv("HOME", "/home/u")
		got, err := Path()
		if err != nil {
			t.Fatal(err)
		}
		if got != "/opt/vroom/config.toml" {
			t.Errorf("Path = %q, want the VROOM_CONFIG one", got)
		}
	})

	t.Run("XDG_CONFIG_HOME when no VROOM_CONFIG", func(t *testing.T) {
		t.Setenv("VROOM_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		t.Setenv("HOME", "/home/u")
		got, err := Path()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join("/xdg", "vroom", FileName); got != want {
			t.Errorf("Path = %q, want %q", got, want)
		}
	})

	t.Run("HOME as last resort", func(t *testing.T) {
		t.Setenv("VROOM_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "/home/u")
		got, err := Path()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join("/home/u", ".config", "vroom", FileName); got != want {
			t.Errorf("Path = %q, want %q", got, want)
		}
	})

	t.Run("no HOME means no path, and it says why", func(t *testing.T) {
		// UserHomeDir fails with no HOME in the environment, which is what a service with an empty environment looks like.
		t.Setenv("VROOM_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "")
		got, err := Path()
		if err == nil {
			t.Fatalf("without HOME Path returned %q, want error: a concrete user path would be a silent failure mode", got)
		}
		if !strings.Contains(err.Error(), "home") {
			t.Errorf("err = %q, want it to say home is missing", err)
		}
	})
}

// An unreadable config is defaults plus a warning, never a boot failure: booting silently would leave the user believing their config is applied.
func TestLoadConUnFicheroIlegibleDevuelveDefaultsYElError(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"broken toml", "name = [ esto no es toml\n"},
		{"invalid ask direction", "[ask]\ndirection = \"diagonal\"\n"},
		{"invalid ask target", "[ask]\ntarget = \"ventana\"\n"},
		{"custom without launcher_cmd", "[ask]\nlauncher = \"custom\"\n"},
		{"agent without cmd", "[ask.agents]\n[ask.agents.mi-agente]\n"},
		{"invalid key", "[keybindings]\nrefresh = \"no-es-una-tecla\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("VROOM_CONFIG", path)

			cfg := Load()
			if cfg.Err == nil {
				t.Fatal("an invalid config must bring Err: without it the user would believe their config is applied")
			}
			if cfg.Ask.Launcher != "auto" {
				t.Errorf("Ask.Launcher = %q, want auto: with a broken config you boot with defaults", cfg.Ask.Launcher)
			}
			if len(cfg.Keybindings) == 0 {
				t.Error("without keybindings the TUI would have no assigned keys")
			}
		})
	}
}

// A missing config is the normal state of a fresh install, so it must not be reported as an error.
func TestLoadSinFicheroNoEsError(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))

	cfg := Load()
	if cfg.Err != nil {
		t.Errorf("a missing config gave error %v: it is the normal case of a new install", cfg.Err)
	}
	if cfg.Scanner.Depth != Defaults().Scanner.Depth {
		t.Errorf("Scanner.Depth = %d, want the default %d", cfg.Scanner.Depth, Defaults().Scanner.Depth)
	}
}

// An empty binding means "you did not change this", so it falls back to the default instead of leaving the action keyless.
func TestKeyForUsaElBindingYCaeAlDefault(t *testing.T) {
	defaults := DefaultKeybindings()

	t.Run("own binding", func(t *testing.T) {
		c := Config{Keybindings: map[string]string{"start_stop": "S"}}
		if got := c.KeyFor("start_stop"); got != "S" {
			t.Errorf("KeyFor = %q, want S", got)
		}
	})

	t.Run("no binding for that action", func(t *testing.T) {
		c := Config{Keybindings: map[string]string{"otra": "z"}}
		if got, want := c.KeyFor("start_stop"), defaults["start_stop"]; got != want {
			t.Errorf("KeyFor = %q, want the default %q", got, want)
		}
	})

	t.Run("empty binding falls back to default", func(t *testing.T) {
		c := Config{Keybindings: map[string]string{"start_stop": ""}}
		if got, want := c.KeyFor("start_stop"), defaults["start_stop"]; got != want {
			t.Errorf("KeyFor = %q with an empty binding, want the default %q", got, want)
		}
	})

	t.Run("nil map", func(t *testing.T) {
		var c Config
		if got, want := c.KeyFor("start_stop"), defaults["start_stop"]; got != want {
			t.Errorf("KeyFor with nil map = %q, want the default %q", got, want)
		}
	})

	t.Run("action with no binding and no default", func(t *testing.T) {
		// An action with no binding and no default stays keyless: an invented default would bind a key to an action that does not exist.
		c := Config{Keybindings: map[string]string{"start_stop": "S"}}
		if got := c.KeyFor("accion-inventada"); got != "" {
			t.Errorf("KeyFor of an action without default = %q, want empty string", got)
		}
	})
}

// A binding missing from the inverse map makes that key silently do nothing, with no warning.
func TestKeyByActionEsElInversoYNoPierdeBindings(t *testing.T) {
	c := Config{Keybindings: map[string]string{
		"start_stop": "S",
		"refresh":    "r",
	}}
	inv := c.KeyByAction()
	if inv["S"] != "start_stop" {
		t.Errorf("inv[S] = %q, want start_stop", inv["S"])
	}
	if inv["r"] != "refresh" {
		t.Errorf("inv[r] = %q, want refresh", inv["r"])
	}
	if len(inv) != 2 {
		t.Errorf("the inverse has %d entries, want 2", len(inv))
	}
}
