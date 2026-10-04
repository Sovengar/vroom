package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The three sources target different premises: VROOM_CONFIG for one process (a runner, a service), XDG for a user, ~/.config for the normal case.
func TestPathSigueElOrdenDeLasTresFuentes(t *testing.T) {
	t.Run("VROOM_CONFIG manda", func(t *testing.T) {
		t.Setenv("VROOM_CONFIG", "/opt/vroom/config.toml")
		t.Setenv("XDG_CONFIG_HOME", "/xdg")
		t.Setenv("HOME", "/home/u")
		got, err := Path()
		if err != nil {
			t.Fatal(err)
		}
		if got != "/opt/vroom/config.toml" {
			t.Errorf("Path = %q, want la de VROOM_CONFIG", got)
		}
	})

	t.Run("XDG_CONFIG_HOME cuando no hay VROOM_CONFIG", func(t *testing.T) {
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

	t.Run("HOME como ultimo recurso", func(t *testing.T) {
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

	t.Run("sin HOME no hay ruta, y se dice por que", func(t *testing.T) {
		// UserHomeDir fails with no HOME in the environment, which is what a service with an empty environment looks like.
		t.Setenv("VROOM_CONFIG", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "")
		got, err := Path()
		if err == nil {
			t.Fatalf("sin HOME Path devolvió %q, want error: un path de usuario concreto sería un modo de fallo silencioso", got)
		}
		if !strings.Contains(err.Error(), "home") {
			t.Errorf("err = %q, want que diga que falta el home", err)
		}
	})
}

// An unreadable config is defaults plus a warning, never a boot failure: booting silently would leave the user believing their config is applied.
func TestLoadConUnFicheroIlegibleDevuelveDefaultsYElError(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"toml roto", "name = [ esto no es toml\n"},
		{"direccion de ask inválida", "[ask]\ndirection = \"diagonal\"\n"},
		{"target de ask inválido", "[ask]\ntarget = \"ventana\"\n"},
		{"custom sin launcher_cmd", "[ask]\nlauncher = \"custom\"\n"},
		{"agente sin cmd", "[ask.agents]\n[ask.agents.mi-agente]\n"},
		{"tecla inválida", "[keybindings]\nrefresh = \"no-es-una-tecla\"\n"},
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
				t.Fatal("una configuración inválida tiene que bring Err: sin él el usuario creería que su config se aplica")
			}
			if cfg.Ask.Launcher != "auto" {
				t.Errorf("Ask.Launcher = %q, want auto: con un config roto se arranca con defaults", cfg.Ask.Launcher)
			}
			if len(cfg.Keybindings) == 0 {
				t.Error("sin keybindings la TUI no tendría ninguna tecla asignada")
			}
		})
	}
}

// A missing config is the normal state of a fresh install, so it must not be reported as an error.
func TestLoadSinFicheroNoEsError(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))

	cfg := Load()
	if cfg.Err != nil {
		t.Errorf("un config ausente dio error %v: es el caso normal de una instalación nueva", cfg.Err)
	}
	if cfg.Scanner.Depth != Defaults().Scanner.Depth {
		t.Errorf("Scanner.Depth = %d, want el default %d", cfg.Scanner.Depth, Defaults().Scanner.Depth)
	}
}

// An empty binding means "you did not change this", so it falls back to the default instead of leaving the action keyless.
func TestKeyForUsaElBindingYCaeAlDefault(t *testing.T) {
	defaults := DefaultKeybindings()

	t.Run("binding propio", func(t *testing.T) {
		c := Config{Keybindings: map[string]string{"start_stop": "S"}}
		if got := c.KeyFor("start_stop"); got != "S" {
			t.Errorf("KeyFor = %q, want S", got)
		}
	})

	t.Run("sin binding para esa accion", func(t *testing.T) {
		c := Config{Keybindings: map[string]string{"otra": "z"}}
		if got, want := c.KeyFor("start_stop"), defaults["start_stop"]; got != want {
			t.Errorf("KeyFor = %q, want el default %q", got, want)
		}
	})

	t.Run("binding vacio cae al default", func(t *testing.T) {
		c := Config{Keybindings: map[string]string{"start_stop": ""}}
		if got, want := c.KeyFor("start_stop"), defaults["start_stop"]; got != want {
			t.Errorf("KeyFor = %q con un binding vacío, want el default %q", got, want)
		}
	})

	t.Run("mapa nil", func(t *testing.T) {
		var c Config
		if got, want := c.KeyFor("start_stop"), defaults["start_stop"]; got != want {
			t.Errorf("KeyFor con mapa nil = %q, want el default %q", got, want)
		}
	})

	t.Run("accion sin binding y sin default", func(t *testing.T) {
		// An action with no binding and no default stays keyless: an invented default would bind a key to an action that does not exist.
		c := Config{Keybindings: map[string]string{"start_stop": "S"}}
		if got := c.KeyFor("accion-inventada"); got != "" {
			t.Errorf("KeyFor de una acción sin default = %q, want cadena vacía", got)
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
		t.Errorf("el inverso tiene %d entradas, want 2", len(inv))
	}
}
