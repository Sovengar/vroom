// Package config carga la configuración global de vroom desde
// $XDG_CONFIG_HOME/vroom/config.toml (default ~/.config/vroom/config.toml),
// con override vía $VROOM_CONFIG (spec 0004 R32).
//
// Sin fichero se aplican los defaults; un fichero malformado devuelve
// defaults + error (la TUI lo notifica al arrancar).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// FileName es el nombre del fichero de configuración global.
const FileName = "config.toml"

// defaultAskPrompt es el template con el que se prellena el input del
// prompt de ask AI (spec 0005 R35). Placeholders: {name} (nombre del
// proyecto), {dir} (ruta del proyecto) y {logs} (directorio del servicio
// con stdout.log/stderr.log). Vacío desactiva el prefill.
const defaultAskPrompt = "Given the app {name} with logs in {logs}, "

// AgentConfig define un agente de IA ejecutable (spec 0004 R32):
// plantilla de comando donde {prompt} ocupa un argumento argv completo.
type AgentConfig struct {
	Cmd string `toml:"cmd"`
}

// AskConfig configura la acción de ask AI (tecla a).
type AskConfig struct {
	// Launcher: auto | herdr | inline | custom.
	Launcher string `toml:"launcher"`
	// Direction del split de herdr: right | down.
	Direction string `toml:"direction"`
	// Target de herdr: pane | tab.
	Target string `toml:"target"`
	// Focus: si false, el split usa --no-focus (vroom conserva el foco).
	Focus bool `toml:"focus"`
	// LauncherCmd es la plantilla para launcher = "custom".
	// Placeholders: {dir} (proyecto, quoteado), {agent} (nombre),
	// {cmd} (comando del agente, quoteado).
	LauncherCmd string `toml:"launcher_cmd"`
	// Prompt es el template con el que se prellena el input del prompt
	// (spec 0005 R35). Vacío desactiva el prefill.
	Prompt string `toml:"prompt"`

	// Agents reemplaza los agentes built-in si tiene entradas.
	Agents map[string]AgentConfig `toml:"agents"`
}

// Config es la configuración global de vroom.
type Config struct {
	Ask AskConfig `toml:"ask"`

	// Keybindings mapea nombre de acción → tecla (espec 0008 R45): una
	// sola rune, ctrl+<rune> o nombre especial (space, home, end…). Se
	// decodifica FUSIONANDO sobre los defaults, de modo que el usuario
	// solo overridea lo que cambia.
	Keybindings map[string]string `toml:"keybindings"`

	// Err acumula el error de parseo, si lo hubo (defaults aplicados).
	Err error
}

// defaultKeybindings son las 12 acciones remapeables de la TUI con sus
// teclas por defecto (espec 0008 R46). Las teclas universales (navegación,
// especiales) y las permanentes "/" (filter) y "!" (shell futuro) no
// aparecen: no son remapeables (R47).
func DefaultKeybindings() map[string]string {
	return map[string]string{
		"start_stop": "s",
		"restart":    "R",
		"build":      "b",
		"install":    "i",
		"tasks":      "t",
		"ask":        "a",
		"clear":      "C",
		"stream":     "c",
		"top":        "g",
		"bottom":     "G",
		"logs":       "l",
		"refresh":    "r",
	}
}

// reservedKeys son las teclas universales de la TUI, no remapeables
// (espec 0008 R47): navegación, especiales y las permanentes "/" y "!".
var reservedKeys = map[string]bool{
	"q": true, "ctrl+c": true, "esc": true, "enter": true, "tab": true,
	"j": true, "k": true, "up": true, "down": true,
	"pgup": true, "pgdown": true, "1": true, "2": true,
	"/": true, "!": true,
}

// specialKeyNames son los nombres de tecla no imprimible admitidos como
// valor de un keybinding (espec 0008 R49).
var specialKeyNames = map[string]bool{
	"space": true, "home": true, "end": true,
	"delete": true, "backspace": true, "left": true, "right": true,
}

// validKey reporta si key tiene el formato admitido (espec 0008 R49): una
// sola rune, ctrl+<rune> (≠ ctrl+c, reservada) o un nombre especial.
func validKey(key string) bool {
	if specialKeyNames[key] {
		return true
	}
	if rest, ok := strings.CutPrefix(key, "ctrl+"); ok {
		return rest != "c" && utf8.RuneCountInString(rest) == 1
	}
	return utf8.RuneCountInString(key) == 1
}

// KeyFor devuelve la tecla activa para una acción, o su default si la
// acción no está en el mapa (espec 0008 R45).
func (c Config) KeyFor(action string) string {
	if k, ok := c.Keybindings[action]; ok && k != "" {
		return k
	}
	return DefaultKeybindings()[action]
}

// KeyByAction construye el mapa inverso tecla → acción a partir de los
// bindings activos (espec 0008 R50). La TUI lo precalcula al arrancar:
// la resolución por tecla es O(1) y determinista.
func (c Config) KeyByAction() map[string]string {
	inv := make(map[string]string, len(c.Keybindings))
	for action, key := range c.Keybindings {
		inv[key] = action
	}
	return inv
}

// Defaults devuelve la configuración por defecto.
func Defaults() Config {
	return Config{
		Ask: AskConfig{
			Launcher:  "auto",
			Direction: "right",
			Target:    "pane",
			Focus:     false,
			Prompt:    defaultAskPrompt,
		},
		Keybindings: DefaultKeybindings(),
	}
}

// Path resuelve la ruta del fichero de configuración: $VROOM_CONFIG,
// si no $XDG_CONFIG_HOME/vroom/config.toml, si no ~/.config/vroom/config.toml.
func Path() (string, error) {
	if p := os.Getenv("VROOM_CONFIG"); p != "" {
		return p, nil
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "vroom", FileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "vroom", FileName), nil
}

// Load lee el fichero de configuración (si existe) sobre los defaults.
// Un fichero ausente NO es error; uno malformado sí (Config.Err).
func Load() Config {
	cfg := Defaults()
	path, err := Path()
	if err != nil {
		cfg.Err = err
		return cfg
	}
	if _, err := os.Stat(path); err != nil {
		return cfg // sin fichero: defaults limpios
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return withDefaults(Config{Err: fmt.Errorf("invalid config %s: %w", path, err)})
	}
	if err := cfg.Validate(); err != nil {
		return withDefaults(Config{Err: fmt.Errorf("invalid config %s: %w", path, err)})
	}
	return cfg
}

// Defaults rellena los campos vacíos de cfg con los valores por defecto.
func withDefaults(cfg Config) Config {
	if cfg.Ask.Launcher == "" {
		cfg.Ask.Launcher = "auto"
	}
	if cfg.Ask.Direction == "" {
		cfg.Ask.Direction = "right"
	}
	if cfg.Ask.Target == "" {
		cfg.Ask.Target = "pane"
	}
	if cfg.Ask.Prompt == "" {
		cfg.Ask.Prompt = defaultAskPrompt
	}
	if cfg.Keybindings == nil {
		cfg.Keybindings = DefaultKeybindings()
	}
	return cfg
}

// Validate aplica los enums válidos de la sección [ask].
func (c *Config) Validate() error {
	switch c.Ask.Launcher {
	case "auto", "herdr", "inline", "custom":
	default:
		return fmt.Errorf("ask.launcher %q inválido (auto|herdr|inline|custom)", c.Ask.Launcher)
	}
	switch c.Ask.Direction {
	case "right", "down":
	default:
		return fmt.Errorf("ask.direction %q inválido (right|down)", c.Ask.Direction)
	}
	switch c.Ask.Target {
	case "pane", "tab":
	default:
		return fmt.Errorf("ask.target %q inválido (pane|tab)", c.Ask.Target)
	}
	if c.Ask.Launcher == "custom" && c.Ask.LauncherCmd == "" {
		return fmt.Errorf("ask.launcher = custom requiere ask.launcher_cmd")
	}
	for name, a := range c.Ask.Agents {
		if a.Cmd == "" {
			return fmt.Errorf("ask.agents.%s sin cmd", name)
		}
	}
	return validateKeybindings(c.Keybindings)
}

// validateKeybindings aplica las reglas de [keybindings] (espec 0008
// R47-R49): acción conocida (los typos no pasan), valor con formato
// válido, teclas reservadas no remapeables y sin colisiones entre
// acciones.
func validateKeybindings(kb map[string]string) error {
	defaults := DefaultKeybindings()
	seen := make(map[string]string, len(defaults))
	for action, key := range kb {
		if _, ok := defaults[action]; !ok {
			return fmt.Errorf("keybindings.%s: acción desconocida", action)
		}
		if key == "" {
			return fmt.Errorf("keybindings.%s: tecla vacía", action)
		}
		if reservedKeys[key] {
			return fmt.Errorf("keybindings.%s: %q es tecla reservada (no remapeable)", action, key)
		}
		if !validKey(key) {
			return fmt.Errorf("keybindings.%s: tecla %q inválida (1 rune, ctrl+<rune> o space|home|end|delete|backspace|left|right)", action, key)
		}
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("keybindings: tecla %q duplicada entre %s y %s", key, prev, action)
		}
		seen[key] = action
	}
	return nil
}
