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

	"github.com/BurntSushi/toml"
)

// FileName es el nombre del fichero de configuración global.
const FileName = "config.toml"

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

	// Agents reemplaza los agentes built-in si tiene entradas.
	Agents map[string]AgentConfig `toml:"agents"`
}

// Config es la configuración global de vroom.
type Config struct {
	Ask AskConfig `toml:"ask"`

	// Err acumula el error de parseo, si lo hubo (defaults aplicados).
	Err error
}

// Defaults devuelve la configuración por defecto.
func Defaults() Config {
	return Config{
		Ask: AskConfig{
			Launcher:  "auto",
			Direction: "right",
			Target:    "pane",
			Focus:     false,
		},
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
	return nil
}
