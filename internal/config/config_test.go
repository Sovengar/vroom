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
		t.Error("prompt default no debe estar vacío")
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
// respeta tal cual.
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
	_ = os.Unsetenv("VROOM_CONFIG")
	p, err = Path()
	if err != nil || p != "/xdg/vroom/config.toml" {
		t.Errorf("Path = %s, %v", p, err)
	}
}

// ---- [keybindings] ----

// Sin [keybindings], las 12 acciones tienen sus defaults.
func TestKeybindingsDefaults(t *testing.T) {
	cfg := withConfig(t, "")
	if cfg.Err != nil {
		t.Fatalf("Err = %v", cfg.Err)
	}
	want := map[string]string{
		"start_stop": "s", "restart": "R", "build": "b", "install": "i",
		"tasks": "t", "ask": "a", "clear": "C", "stream": "c",
		"top": "g", "bottom": "G", "logs": "l", "refresh": "r",
	}
	for action, key := range want {
		if got := cfg.KeyFor(action); got != key {
			t.Errorf("KeyFor(%q) = %q, want %q", action, got, key)
		}
	}
	inv := cfg.KeyByAction()
	for action, key := range want {
		if got := inv[key]; got != action {
			t.Errorf("KeyByAction[%q] = %q, want %q", key, got, action)
		}
	}
}

// Override parcial — solo cambia lo declarado, el resto conserva
// default.
func TestKeybindingsOverridePartial(t *testing.T) {
	cfg := withConfig(t, "[keybindings]\nstart_stop = \"x\"\n")
	if cfg.Err != nil {
		t.Fatalf("Err = %v", cfg.Err)
	}
	if got := cfg.KeyFor("start_stop"); got != "x" {
		t.Errorf("KeyFor(start_stop) = %q, want x", got)
	}
	if got := cfg.KeyFor("build"); got != "b" {
		t.Errorf("KeyFor(build) = %q, want b (default intacto)", got)
	}
	if got := cfg.KeyByAction()["x"]; got != "start_stop" {
		t.Errorf("KeyByAction[x] = %q, want start_stop", got)
	}
}

// Tecla reservada → config inválida, defaults restaurados.
func TestKeybindingsReserved(t *testing.T) {
	cfg := withConfig(t, "[keybindings]\nask = \"q\"\n")
	if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), "reservada") {
		t.Errorf("Err = %v, want tecla reservada", cfg.Err)
	}
	if got := cfg.KeyFor("ask"); got != "a" {
		t.Errorf("KeyFor(ask) = %q, want a (default)", got)
	}
}

// Dos acciones con la misma tecla → config inválida.
func TestKeybindingsCollision(t *testing.T) {
	cfg := withConfig(t, "[keybindings]\nbuild = \"x\"\ninstall = \"x\"\n")
	if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), "duplicada") {
		t.Errorf("Err = %v, want tecla duplicada", cfg.Err)
	}
	if cfg.KeyFor("build") != "b" || cfg.KeyFor("install") != "i" {
		t.Errorf("defaults no restaurados: build=%q install=%q", cfg.KeyFor("build"), cfg.KeyFor("install"))
	}
}

// Formato de tecla válida e inválida.
func TestKeybindingsFormat(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr string // "" = válido
	}{
		{"rune simple", "z", ""},
		{"rune mayúscula", "Z", ""},
		{"especial", "space", ""},
		{"ctrl otra", "ctrl+k", ""},
		{"multi rune", "abc", "inválida"},
		{"vacía", "", "vacía"},
		{"ctrl+c reservada", "ctrl+c", "reservada"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := withConfig(t, "[keybindings]\nrestart = \""+tt.value+"\"\n")
			if tt.wantErr == "" {
				if cfg.Err != nil {
					t.Errorf("Err = %v, want nil", cfg.Err)
				}
				if cfg.KeyFor("restart") != tt.value {
					t.Errorf("KeyFor(restart) = %q, want %q", cfg.KeyFor("restart"), tt.value)
				}
				return
			}
			if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), tt.wantErr) {
				t.Errorf("Err = %v, want %q", cfg.Err, tt.wantErr)
			}
		})
	}
}

// Acción desconocida (typo o permanente como filter/shell) →
// config inválida.
func TestKeybindingsUnknownAction(t *testing.T) {
	for _, action := range []string{"filter", "shell", "fiilter"} {
		cfg := withConfig(t, "[keybindings]\n"+action+" = \"f\"\n")
		if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), "desconocida") {
			t.Errorf("acción %q: Err = %v, want desconocida", action, cfg.Err)
		}
	}
}
