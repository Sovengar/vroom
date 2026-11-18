// Package config loads vroom's global config from $VROOM_CONFIG or $XDG_CONFIG_HOME/vroom/config.toml; a missing file is not an error but a malformed one yields defaults plus Config.Err so the TUI can report it.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"vroom/internal/manifest"
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
	Ask     AskConfig     `toml:"ask"`
	Scanner ScannerConfig `toml:"scanner"`
	// Repos holds whole manifests keyed by absolute directory, so a project can be declared (or corrected) without committing .vroom.toml — the case of a public repo. The key wins over the file: presence of an entry REPLACES the manifest, it does not merge field by field, because a half-merged manifest is a manifest nobody can read off a single file.
	Repos       map[string]manifest.Manifest `toml:"repos"`
	Keybindings map[string]string            `toml:"keybindings"`

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
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return withDefaults(Config{Err: fmt.Errorf("invalid config %s: %w", path, err)})
	}
	if err := checkRepoKeys(md); err != nil {
		return withDefaults(Config{Err: fmt.Errorf("invalid config %s: %w", path, err)})
	}
	repos, err := normalizeRepos(cfg.Repos)
	if err != nil {
		return withDefaults(Config{Err: fmt.Errorf("invalid config %s: %w", path, err)})
	}
	cfg.Repos = repos
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

// normalizeRepos resolves the directory keys once, at load time: `~` is expanded and a relative path is made absolute against the CWD, so every later comparison with a scanned project path is a plain string equality. Two keys that resolve to the same directory are an error instead of last-one-wins, because the loser would silently lose its manifest.
func normalizeRepos(repos map[string]manifest.Manifest) (map[string]manifest.Manifest, error) {
	if len(repos) == 0 {
		return repos, nil
	}
	out := make(map[string]manifest.Manifest, len(repos))
	for key, m := range repos {
		abs, err := resolveDir(key)
		if err != nil {
			return nil, fmt.Errorf("repos.%q: %w", key, err)
		}
		if _, dup := out[abs]; dup {
			return nil, fmt.Errorf("repos: %q and another key both resolve to %s", key, abs)
		}
		out[abs] = m
	}
	return out, nil
}

func resolveDir(key string) (string, error) {
	if !filepath.IsAbs(key) {
		if key == "~" || strings.HasPrefix(key, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("could not resolve home directory: %w", err)
			}
			key = filepath.Join(home, strings.TrimPrefix(key, "~"))
		} else {
			abs, err := filepath.Abs(key)
			if err != nil {
				return "", fmt.Errorf("could not resolve path: %w", err)
			}
			key = abs
		}
	}
	return filepath.Clean(key), nil
}

// checkRepoKeys rejects the keys TOML would swallow silently inside a [repos] entry: a typo, or a top-level field written after a [repos."<dir>".commands.*] header (it becomes commands.start.port and the port visually present in the file disappears). Same trap the manifest rejects, one level deeper.
func checkRepoKeys(md toml.MetaData) error {
	for _, key := range md.Undecoded() {
		name := key.String()
		if !strings.HasPrefix(name, "repos.") {
			continue
		}
		return fmt.Errorf("repos: unknown key %q (check the spelling; a field written after a [repos.\"<dir>\".commands.*] header lands inside that table — put the [commands] tables last)", name)
	}
	return nil
}

// RepoManifests is the scanner's view of [repos]: the pointer map it overrides .vroom.toml with.
func (c Config) RepoManifests() map[string]*manifest.Manifest {
	if len(c.Repos) == 0 {
		return nil
	}
	out := make(map[string]*manifest.Manifest, len(c.Repos))
	for dir, m := range c.Repos {
		entry := m
		out[dir] = &entry
	}
	return out
}

func (c *Config) Validate() error {
	for dir, m := range c.Repos {
		entry := m
		if err := entry.Validate(); err != nil {
			return fmt.Errorf("repos.%q: %w", dir, err)
		}
	}
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
