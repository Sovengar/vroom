// Package config loads vroom's global config from $VROOM_CONFIG or $XDG_CONFIG_HOME/vroom/config.toml; a missing file is not an error but a malformed one yields defaults plus Config.Err so the TUI can report it.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

const FileName = "config.toml"

// {name}, {dir} and {logs} are substituted; empty disables the prefill.
const defaultAskPrompt = "Given the app {name} with logs in {logs}, "

type AgentConfig struct {
	Cmd string `toml:"cmd"`
}

type AskConfig struct {
	Launcher    string `toml:"launcher"`
	Direction   string `toml:"direction"`
	Target      string `toml:"target"`
	Focus       bool   `toml:"focus"`
	LauncherCmd string `toml:"launcher_cmd"`
	Prompt      string `toml:"prompt"`

	Agents map[string]AgentConfig `toml:"agents"`
}

type ScannerConfig struct {
	Root  string `toml:"root"`
	Depth int    `toml:"depth"`
}

type Config struct {
	Ask         AskConfig         `toml:"ask"`
	Scanner     ScannerConfig     `toml:"scanner"`
	Keybindings map[string]string `toml:"keybindings"`

	Err error
}

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

var reservedKeys = map[string]bool{
	"q": true, "ctrl+c": true, "esc": true, "enter": true, "tab": true,
	"j": true, "k": true, "up": true, "down": true,
	"pgup": true, "pgdown": true, "1": true, "2": true,
	"/": true, "!": true,
}

var specialKeyNames = map[string]bool{
	"space": true, "home": true, "end": true,
	"delete": true, "backspace": true, "left": true, "right": true,
}

func validKey(key string) bool {
	if specialKeyNames[key] {
		return true
	}
	if rest, ok := strings.CutPrefix(key, "ctrl+"); ok {
		return rest != "c" && utf8.RuneCountInString(rest) == 1
	}
	return utf8.RuneCountInString(key) == 1
}

func (c Config) KeyFor(action string) string {
	if k, ok := c.Keybindings[action]; ok && k != "" {
		return k
	}
	return DefaultKeybindings()[action]
}

// The TUI builds this once at startup because iterating the map is not deterministic.
func (c Config) KeyByAction() map[string]string {
	inv := make(map[string]string, len(c.Keybindings))
	for action, key := range c.Keybindings {
		inv[key] = action
	}
	return inv
}

func Defaults() Config {
	return Config{
		Ask: AskConfig{
			Launcher:  "auto",
			Direction: "right",
			Target:    "pane",
			Focus:     false,
			Prompt:    defaultAskPrompt,
		},
		Scanner: ScannerConfig{
			Depth: 4,
		},
		Keybindings: DefaultKeybindings(),
	}
}

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

func Load() Config {
	cfg := Defaults()
	path, err := Path()
	if err != nil {
		cfg.Err = err
		return cfg
	}
	if _, err := os.Stat(path); err != nil {
		return cfg
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return withDefaults(Config{Err: fmt.Errorf("invalid config %s: %w", path, err)})
	}
	if err := cfg.Validate(); err != nil {
		return withDefaults(Config{Err: fmt.Errorf("invalid config %s: %w", path, err)})
	}
	return cfg
}

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
	if cfg.Scanner.Depth <= 0 {
		cfg.Scanner.Depth = 4
	}
	if cfg.Keybindings == nil {
		cfg.Keybindings = DefaultKeybindings()
	}
	return cfg
}

func (c *Config) Validate() error {
	switch c.Ask.Launcher {
	case "auto", "herdr", "inline", "custom":
	default:
		return fmt.Errorf("ask.launcher %q invalid (auto|herdr|inline|custom)", c.Ask.Launcher)
	}
	switch c.Ask.Direction {
	case "right", "down":
	default:
		return fmt.Errorf("ask.direction %q invalid (right|down)", c.Ask.Direction)
	}
	switch c.Ask.Target {
	case "pane", "tab":
	default:
		return fmt.Errorf("ask.target %q invalid (pane|tab)", c.Ask.Target)
	}
	if c.Ask.Launcher == "custom" && c.Ask.LauncherCmd == "" {
		return fmt.Errorf("ask.launcher = custom requires ask.launcher_cmd")
	}
	for name, a := range c.Ask.Agents {
		if a.Cmd == "" {
			return fmt.Errorf("ask.agents.%s without cmd", name)
		}
	}
	return validateKeybindings(c.Keybindings)
}

func validateKeybindings(kb map[string]string) error {
	defaults := DefaultKeybindings()
	seen := make(map[string]string, len(defaults))
	for action, key := range kb {
		if _, ok := defaults[action]; !ok {
			return fmt.Errorf("keybindings.%s: unknown action", action)
		}
		if key == "" {
			return fmt.Errorf("keybindings.%s: empty key", action)
		}
		if reservedKeys[key] {
			return fmt.Errorf("keybindings.%s: %q is a reserved key (not remappable)", action, key)
		}
		if !validKey(key) {
			return fmt.Errorf("keybindings.%s: key %q invalid (1 rune, ctrl+<rune> or space|home|end|delete|backspace|left|right)", action, key)
		}
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("keybindings: key %q duplicated between %s and %s", key, prev, action)
		}
		seen[key] = action
	}
	return nil
}
