package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Path, KeyFor y Load: las tres funciones que deciden qué configuración se está
// usando y qué tecla hace qué.
//
// Path decide el FICHERO, y resolverlo mal tiene dos consecuencias simétricas: si
// lee el del developer, un test lee su configuración real; si inventa una ruta,
// un usuario con VROOM_CONFIG mal escrito no vería ningún cambio. Por eso el
// orden de las tres fuentes está fijado aquí.
//
// KeyFor decide qué tecla dispara qué acción, y la regla es que el default se
// aplica cuando el usuario NO lo dice — nunca cuando lo dice mal. Un binding
// inválido se descarta en la validación y se vuelve al default, y KeyFor es donde
// ese "se vuelve" ocurre.
// ---------------------------------------------------------------------------

// TestPathSigueElOrdenDeLasTresFuentes: $VROOM_CONFIG gana sobre
// $XDG_CONFIG_HOME, que gana sobre ~/.config.
//
// El orden importa porque las tres fuentes están pensadas para-premises
// distintos: VROOM_CONFIG para un proceso concreto (el runner, un servicio), XDG
// para un usuario, y ~/.config para el caso normal. Invertir las dos primeras haría
// que un servicio con VROOM_CONFIG apuntara al config equivocado.
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
		// UserHomeDir falla cuando no hay HOME en el entorno, que es lo que pasa
		// bajo un servicio con el entorno vacío. Un path inventado sería un
		// fichero de configuración de otro usuario.
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

// TestLoadConUnFicheroIlegibleDevuelveDefaultsYElError: un config que no se
// puede parsear NO es un fallo de arranque, es defaults más un aviso.
//
// Es la decisión que hace que un typo en el config no deje al usuario sin TUI: la
// configuración malformada se reporta y vroom arranca con los defaults. Lo que no
// puede pasar es arrancar callado, porque el usuario creería que su configuración
// se está aplicando.
func TestLoadConUnFicheroIlegibleDevuelveDefaultsYElError(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"toml roto", "name = [ esto no es toml\n"},
		// Campos que existen de verdad en la config y con valores inválidos.
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
			// Y son defaults con los campos rellenados, no un cero: la TUI tiene
			// que funcionar igual.
			if cfg.Ask.Launcher != "auto" {
				t.Errorf("Ask.Launcher = %q, want auto: con un config roto se arranca con defaults", cfg.Ask.Launcher)
			}
			if len(cfg.Keybindings) == 0 {
				t.Error("sin keybindings la TUI no tendría ninguna tecla asignada")
			}
		})
	}
}

// TestLoadSinFicheroNoEsError: un config ausente es el caso NORMAL, no un fallo.
//
// Es lo que pasa la primera vez que alguien instala vroom. Dejarlo como error
// haría que la TUI se quejara de algo que el usuario no ha hecho mal.
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

// TestKeyForUsaElBindingYCaeAlDefault: la tecla activa gana, y si no hay binding
// —o el binding está vacío— se usa el default.
//
// El caso del binding VACÍO importa: un usuario puede escribir `ask = ""` sin
// querer, y devolver "" haría que la acción no tuviera tecla. El default es la
// respuesta honesta: "no has cambiado esto".
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
		// Una acción que no está en el mapa NI tiene default: no hay tecla para
		// ella, y "" lo dice. Un default inventado sería peor: asignaría una tecla
		// a una acción que no existe.
		c := Config{Keybindings: map[string]string{"start_stop": "S"}}
		if got := c.KeyFor("accion-inventada"); got != "" {
			t.Errorf("KeyFor de una acción sin default = %q, want cadena vacía", got)
		}
	})
}

// TestKeyByActionEsElInversoYNoPierdeBindings: el mapa inverso es lo que usa la
// TUI para resolver una tecla en O(1), y tiene que traer TODOS los bindings
// activos, no solo los que difieren del default.
//
// Si se perdiera uno, esa tecla dejaría de hacer nada y el usuario no tendría ni
// aviso ni forma de saber por qué.
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
