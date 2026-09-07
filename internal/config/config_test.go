package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withConfig(t *testing.T, content string) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("VROOM_CONFIG", path)
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Load()
}

// Sin fichero: defaults limpios, sin error.
func TestLoadDefaults(t *testing.T) {
	cfg := withConfig(t, "")
	if cfg.Ask.Launcher != "auto" || cfg.Ask.Direction != "right" || cfg.Ask.Target != "pane" || cfg.Ask.Focus {
		t.Errorf("defaults incorrectos: %+v", cfg.Ask)
	}
	if cfg.Ask.Prompt == "" {
		t.Error("prompt default no debe estar vacío (prefill R35)")
	}
	if !strings.Contains(cfg.Ask.Prompt, "{name}") || !strings.Contains(cfg.Ask.Prompt, "{logs}") {
		t.Errorf("prompt default sin placeholders: %q", cfg.Ask.Prompt)
	}
	if cfg.Err != nil {
		t.Errorf("Err = %v, want nil", cfg.Err)
	}
}

// Parse completo de la sección [ask].
func TestLoadFull(t *testing.T) {
	cfg := withConfig(t, `
[ask]
launcher = "custom"
direction = "down"
target = "tab"
focus = true
launcher_cmd = "tmux new-window -c {dir} -- {cmd}"

[ask.agents.opencode]
cmd = "opencode --prompt {prompt}"
`)
	if cfg.Err != nil {
		t.Fatalf("Err = %v", cfg.Err)
	}
	ask := cfg.Ask
	if ask.Launcher != "custom" || ask.Direction != "down" || ask.Target != "tab" || !ask.Focus {
		t.Errorf("ask mal parseado: %+v", ask)
	}
	if ask.LauncherCmd != "tmux new-window -c {dir} -- {cmd}" {
		t.Errorf("launcher_cmd = %q", ask.LauncherCmd)
	}
	if got := ask.Agents["opencode"].Cmd; got != "opencode --prompt {prompt}" {
		t.Errorf("agent cmd = %q", got)
	}
}

func TestLoadMalformed(t *testing.T) {
	cfg := withConfig(t, "esto no es [toml")
	if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), "invalid config") {
		t.Errorf("Err = %v, want invalid config", cfg.Err)
	}
	// Con defaults aplicados a pesar del error.
	if cfg.Ask.Launcher != "auto" {
		t.Errorf("malformado debe devolver defaults, got %+v", cfg.Ask)
	}
}

func TestLoadInvalidEnums(t *testing.T) {
	tests := []struct{ name, content, want string }{
		{"launcher", "[ask]\nlauncher = \"magic\"", "ask.launcher"},
		{"direction", "[ask]\ndirection = \"up\"", "ask.direction"},
		{"target", "[ask]\ntarget = \"window\"", "ask.target"},
		{"custom sin cmd", "[ask]\nlauncher = \"custom\"", "launcher_cmd"},
		{"agent sin cmd", "[ask.agents.x]\n", "ask.agents.x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := withConfig(t, tt.content)
			if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), tt.want) {
				t.Errorf("Err = %v, want %q", cfg.Err, tt.want)
			}
		})
	}
}

// prompt = "" explícito desactiva el prefill; un template custom se
// respeta tal cual (spec 0005 R35).
func TestLoadPromptTemplate(t *testing.T) {
	custom := withConfig(t, `[ask]
prompt = "About {name} in {dir}, logs {logs}: "
`)
	if custom.Err != nil {
		t.Fatalf("Err = %v", custom.Err)
	}
	if custom.Ask.Prompt != "About {name} in {dir}, logs {logs}: " {
		t.Errorf("prompt custom = %q", custom.Ask.Prompt)
	}

	empty := withConfig(t, `[ask]
prompt = ""
`)
	if empty.Err != nil {
		t.Fatalf("Err = %v", empty.Err)
	}
	if empty.Ask.Prompt != "" {
		t.Errorf("prompt explícito vacío debe quedarse vacío, got %q", empty.Ask.Prompt)
	}

	broken := withConfig(t, "esto no es [toml")
	if broken.Ask.Prompt == "" {
		t.Error("config malformado debe devolver el prompt default")
	}
}

// Path respeta $VROOM_CONFIG sobre $XDG_CONFIG_HOME.
func TestPathPriority(t *testing.T) {
	t.Setenv("VROOM_CONFIG", "/tmp/vroom-custom.toml")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	p, err := Path()
	if err != nil || p != "/tmp/vroom-custom.toml" {
		t.Errorf("Path = %s, %v", p, err)
	}
	os.Unsetenv("VROOM_CONFIG")
	p, err = Path()
	if err != nil || p != "/xdg/vroom/config.toml" {
		t.Errorf("Path = %s, %v", p, err)
	}
}
