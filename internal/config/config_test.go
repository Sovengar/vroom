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

func TestLoadDefaults(t *testing.T) {
	cfg := withConfig(t, "")
	if cfg.Ask.Launcher != "auto" || cfg.Ask.Direction != "right" || cfg.Ask.Target != "pane" || cfg.Ask.Focus {
		t.Errorf("incorrect defaults: %+v", cfg.Ask)
	}
	if cfg.Ask.Prompt == "" {
		t.Error("default prompt must not be empty")
	}
	if !strings.Contains(cfg.Ask.Prompt, "{name}") || !strings.Contains(cfg.Ask.Prompt, "{logs}") {
		t.Errorf("default prompt without placeholders: %q", cfg.Ask.Prompt)
	}
	if cfg.Err != nil {
		t.Errorf("Err = %v, want nil", cfg.Err)
	}
}

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
		t.Errorf("ask parsed incorrectly: %+v", ask)
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
	if cfg.Ask.Launcher != "auto" {
		t.Errorf("malformed must return defaults, got %+v", cfg.Ask)
	}
}

func TestLoadInvalidEnums(t *testing.T) {
	tests := []struct{ name, content, want string }{
		{"launcher", "[ask]\nlauncher = \"magic\"", "ask.launcher"},
		{"direction", "[ask]\ndirection = \"up\"", "ask.direction"},
		{"target", "[ask]\ntarget = \"window\"", "ask.target"},
		{"custom without cmd", "[ask]\nlauncher = \"custom\"", "launcher_cmd"},
		{"agent without cmd", "[ask.agents.x]\n", "ask.agents.x"},
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

// An explicit prompt = "" disables the prefill, so it must not fall back to the default template.
func TestLoadPromptTemplate(t *testing.T) {
	custom := withConfig(t, `[ask]
prompt = "About {name} in {dir}, logs {logs}: "
`)
	if custom.Err != nil {
		t.Fatalf("Err = %v", custom.Err)
	}
	if custom.Ask.Prompt != "About {name} in {dir}, logs {logs}: " {
		t.Errorf("custom prompt = %q", custom.Ask.Prompt)
	}

	empty := withConfig(t, `[ask]
prompt = ""
`)
	if empty.Err != nil {
		t.Fatalf("Err = %v", empty.Err)
	}
	if empty.Ask.Prompt != "" {
		t.Errorf("explicit empty prompt must stay empty, got %q", empty.Ask.Prompt)
	}

	broken := withConfig(t, "esto no es [toml")
	if broken.Ask.Prompt == "" {
		t.Error("malformed config must return the default prompt")
	}
}

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

func TestKeybindingsOverridePartial(t *testing.T) {
	cfg := withConfig(t, "[keybindings]\nstart_stop = \"x\"\n")
	if cfg.Err != nil {
		t.Fatalf("Err = %v", cfg.Err)
	}
	if got := cfg.KeyFor("start_stop"); got != "x" {
		t.Errorf("KeyFor(start_stop) = %q, want x", got)
	}
	if got := cfg.KeyFor("build"); got != "b" {
		t.Errorf("KeyFor(build) = %q, want b (default intact)", got)
	}
	if got := cfg.KeyByAction()["x"]; got != "start_stop" {
		t.Errorf("KeyByAction[x] = %q, want start_stop", got)
	}
}

func TestKeybindingsReserved(t *testing.T) {
	cfg := withConfig(t, "[keybindings]\nask = \"q\"\n")
	if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), "reserved") {
		t.Errorf("Err = %v, want reserved key", cfg.Err)
	}
	if got := cfg.KeyFor("ask"); got != "a" {
		t.Errorf("KeyFor(ask) = %q, want a (default)", got)
	}
}

func TestKeybindingsCollision(t *testing.T) {
	cfg := withConfig(t, "[keybindings]\nbuild = \"x\"\ninstall = \"x\"\n")
	if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), "duplicate") {
		t.Errorf("Err = %v, want duplicate key", cfg.Err)
	}
	if cfg.KeyFor("build") != "b" || cfg.KeyFor("install") != "i" {
		t.Errorf("defaults not restored: build=%q install=%q", cfg.KeyFor("build"), cfg.KeyFor("install"))
	}
}

func TestKeybindingsFormat(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr string // "" = valid
	}{
		{"single rune", "z", ""},
		{"uppercase rune", "Z", ""},
		{"special", "space", ""},
		{"ctrl other", "ctrl+k", ""},
		{"multi rune", "abc", "invalid"},
		{"empty", "", "empty"},
		{"ctrl+c reserved", "ctrl+c", "reserved"},
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

func TestKeybindingsUnknownAction(t *testing.T) {
	for _, action := range []string{"filter", "shell", "fiilter"} {
		cfg := withConfig(t, "[keybindings]\n"+action+" = \"f\"\n")
		if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), "unknown") {
			t.Errorf("action %q: Err = %v, want unknown", action, cfg.Err)
		}
	}
}

// The whole point of [repos] is a manifest that lives outside the repository, so the entry must decode to a full manifest — commands included — and not to a bag of overrides.
func TestReposDecodeWholeManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := withConfig(t, `
[repos."~/dev/work/api"]
name = "api"
primary_group = "vsocial"
port = 8080
url_generation = "by_hostname"
route_name = "api"

commands.start.run = "mise run start"
commands.start.hooks.pre_run = "mise install"

[repos."~/dev/work/api".commands.build]
run = "mise run build"
`)
	if cfg.Err != nil {
		t.Fatalf("Err = %v", cfg.Err)
	}
	dir := filepath.Join(home, "dev", "work", "api")
	m, ok := cfg.Repos[dir]
	if !ok {
		t.Fatalf("repos key = %v, want the expanded %s", cfg.Repos, dir)
	}
	if m.Name != "api" || m.Port != 8080 || m.RouteName != "api" || m.PrimaryGroup != "vsocial" {
		t.Errorf("manifest decoded wrong: %+v", m)
	}
	if m.Commands.Start.Run != "mise run start" || m.Commands.Start.Hooks.PreRun != "mise install" || m.Commands.Build.Run != "mise run build" {
		t.Errorf("commands decoded wrong: %+v", m.Commands)
	}
	if got := cfg.RepoManifests()[dir]; got == nil || got.Name != "api" {
		t.Errorf("RepoManifests() = %v, want the same entry", got)
	}
}

func TestReposRelativeKeyResolvedAgainstCwd(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cfg := withConfig(t, `
[repos."../proyecto/api"]
name = "api"
commands.start.run = "go run ."
`)
	if cfg.Err != nil {
		t.Fatalf("Err = %v", cfg.Err)
	}
	want := filepath.Join(filepath.Dir(cwd), "proyecto", "api")
	if _, ok := cfg.Repos[want]; !ok {
		t.Errorf("repos keys = %v, want %s", cfg.Repos, want)
	}
}

// Two spellings of one directory would make the loser's manifest vanish with no error, which is exactly the silent loss [repos] must not have.
func TestReposDuplicateResolvedKeysRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := withConfig(t, `
[repos."~/dup"]
name = "a"
commands.start.run = "echo a"

[repos."`+filepath.Join(home, "dup")+`"]
name = "b"
commands.start.run = "echo b"
`)
	if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), "resolve to") {
		t.Errorf("Err = %v, want a duplicate-path error", cfg.Err)
	}
}

func TestReposEntryIsValidated(t *testing.T) {
	tests := []struct {
		name, body, wantErr string
	}{
		{"missing name", "[repos.\"/tmp/a\"]\ncommands.start.run = \"echo\"\n", "name"},
		{"missing start", "[repos.\"/tmp/a\"]\nname = \"a\"\n", "commands.start.run"},
		{"bad generation", "[repos.\"/tmp/a\"]\nname = \"a\"\nport = 80\nurl_generation = \"by_port-ish\"\ncommands.start.run = \"echo\"\n", "url_generation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := withConfig(t, tt.body)
			if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), "repos.") || !strings.Contains(cfg.Err.Error(), tt.wantErr) {
				t.Errorf("Err = %v, want it to name repos. and %q", cfg.Err, tt.wantErr)
			}
		})
	}
}

// A field written after a [repos."<dir>".commands.*] header lands inside that table: TOML accepts it, so only this check keeps the port the user sees in the file from disappearing.
func TestReposUnknownKeyRejected(t *testing.T) {
	cfg := withConfig(t, "[repos.\"/tmp/a\"]\nname = \"a\"\ncommands.start.run = \"echo\"\ncomands = 1\n")
	if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), "unknown key") {
		t.Errorf("Err = %v, want unknown key", cfg.Err)
	}
}

// Without HOME a `~` key cannot be resolved, and silently keeping the literal "~" would point the manifest at a directory that does not exist.
func TestReposTildeWithoutHomeIsAnError(t *testing.T) {
	t.Setenv("HOME", "")
	cfg := withConfig(t, "[repos.\"~/api\"]\nname = \"api\"\ncommands.start.run = \"echo\"\n")
	if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), "home") {
		t.Errorf("Err = %v, want a home-directory error", cfg.Err)
	}
}

// A relative key is resolved against the CWD, and a CWD that no longer exists must fail loudly instead of resolving to something arbitrary.
func TestReposRelativeKeyWithDeadCwdIsAnError(t *testing.T) {
	dead := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("VROOM_CONFIG", cfgPath)
	if err := os.WriteFile(cfgPath, []byte("[repos.\"sub/api\"]\nname = \"api\"\ncommands.start.run = \"echo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dead)
	if err := os.RemoveAll(dead); err != nil {
		t.Fatal(err)
	}
	cfg := Load()
	if cfg.Err == nil || !strings.Contains(cfg.Err.Error(), "path") {
		t.Errorf("Err = %v, want a path-resolution error", cfg.Err)
	}
}

// Only undecoded keys UNDER repos. matter: an unknown key anywhere else keeps being ignored, because a config written before a field existed must keep working.
func TestUnknownKeyOutsideReposIsIgnored(t *testing.T) {
	cfg := withConfig(t, "futuro = 1\n[repos.\"/tmp/a\"]\nname = \"a\"\ncommands.start.run = \"echo\"\n")
	if cfg.Err != nil {
		t.Errorf("Err = %v, want nil", cfg.Err)
	}
}

// A config with no [repos] must hand the scanner a nil map: an empty non-nil one would take the override path on every scan for nothing.
func TestRepoManifestsNilWithoutEntries(t *testing.T) {
	cfg := withConfig(t, "")
	if got := cfg.RepoManifests(); got != nil {
		t.Errorf("RepoManifests() = %v, want nil", got)
	}
}
