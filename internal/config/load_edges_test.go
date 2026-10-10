package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/group"
	"vroom/internal/manifest"
	"vroom/internal/scanner"
)

// Stat under a missing dir is NotExist, so a typo in $VROOM_CONFIG must be indistinguishable from no config at all.
func TestLoadConUnConfigEnUnDirectorioInexistenteArrancaConDefaults(t *testing.T) {
	noExiste := filepath.Join(t.TempDir(), "no-existe", "config.toml")
	t.Setenv("VROOM_CONFIG", noExiste)

	cfg := Load()
	if cfg.Err != nil {
		t.Errorf("a config under a non-existent directory gave error %v: it is the same case as having no config", cfg.Err)
	}
	if cfg.Ask.Launcher != "auto" {
		t.Errorf("Ask.Launcher = %q, want auto", cfg.Ask.Launcher)
	}
	if len(cfg.Keybindings) == 0 {
		t.Error("without keybindings the TUI would have no assigned keys")
	}
}

// $VROOM_CONFIG pointing at a directory is the likeliest misconfiguration: Stat succeeds, the decode fails, and the TUI must still boot.
func TestLoadConUnConfigQueEsUnDirectorioArrancaConDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VROOM_CONFIG", dir)

	cfg := Load()
	if cfg.Err == nil {
		t.Error("a config that is a directory must give an error: the user wrote the folder path")
	}
	if cfg.Ask.Launcher != "auto" || cfg.Scanner.Depth != Defaults().Scanner.Depth {
		t.Errorf("with an unreadable config you must boot with defaults, got %+v", cfg)
	}
	if len(cfg.Keybindings) == 0 {
		t.Error("without keybindings the TUI would have no assigned keys")
	}
}

// Unknown keys are ignored so a config written for an older vroom survives a version that dropped a field.
func TestLoadIgnoraLasClavesQueNoExistenEnElSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`
[ask]
launcher = "inline"
clave_del_futuro = "lo que sea"

[seccion_inventada]
lo_que_sea = 1

[scanner]
depth = 7
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VROOM_CONFIG", path)

	cfg := Load()
	if cfg.Err != nil {
		t.Errorf("an unknown key cannot invalidate the config: %v", cfg.Err)
	}
	if cfg.Ask.Launcher != "inline" {
		t.Errorf("Ask.Launcher = %q: known keys must be applied even if there are unknown ones", cfg.Ask.Launcher)
	}
	if cfg.Scanner.Depth != 7 {
		t.Errorf("Scanner.Depth = %d, want 7", cfg.Scanner.Depth)
	}
}

// These accessors run over every scanned project, including bare directories with no manifest, so nil must yield "" instead of blanking the TUI.
func TestSecondaryOfConManifiestoNilEsCadenaVacia(t *testing.T) {
	if got := group.SecondaryOf(scannerProject(nil)); got != "" {
		t.Errorf("SecondaryOf without manifest = %q, want empty string", got)
	}
	if got := group.PrimaryOf(scannerProject(nil)); got != "" {
		t.Errorf("PrimaryOf without manifest = %q, want empty string", got)
	}
	if got := group.SecondaryOf(scannerProject(conGrupo("tienda", "backend"))); got != "backend" {
		t.Errorf("SecondaryOf = %q, want backend", got)
	}
	// An empty secondary means "directly under the primary", not "unset".
	if got := group.SecondaryOf(scannerProject(conGrupo("tienda", ""))); got != "" {
		t.Errorf("SecondaryOf without secondary = %q, want empty string", got)
	}
	for _, g := range []string{"", "tienda"} {
		if got := group.PrimaryOf(scannerProject(conGrupo(g, ""))); got != g {
			t.Errorf("PrimaryOf(%q) = %q", g, got)
		}
	}
}

// The inverse map must be built from the fully merged bindings, or default keys silently die as soon as the user touches one.
func TestKeyByActionEsElInversoRealYNoPierdeLasTeclasPorDefecto(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("[keybindings]\nrestart = \"R\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VROOM_CONFIG", path)

	cfg := Load()
	if cfg.Err != nil {
		t.Fatal(cfg.Err)
	}

	inv := cfg.KeyByAction()
	defaults := DefaultKeybindings()

	if inv["R"] != "restart" {
		t.Errorf("inv[R] = %q, want restart", inv["R"])
	}
	for accion, tecla := range defaults {
		if accion == "restart" {
			continue
		}
		if inv[tecla] == "" {
			t.Errorf("the key %q of action %q is not in the inverse: touching one binding, "+
				"the other actions would stop working", tecla, accion)
		}
	}
	if len(inv) != len(defaults) {
		t.Errorf("the inverse has %d entries, want %d", len(inv), len(defaults))
	}
}

// Reject an agent with no cmd, since the picker would offer it and fail on press, but never block the boot.
func TestLoadConUnAgentsSinCmdLoRechazaPeroArrancaConDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(`
[ask]
launcher = "inline"

[ask.agents.mi-agente]
prompt = "sin cmd"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VROOM_CONFIG", path)

	cfg := Load()
	if cfg.Err == nil {
		t.Fatal("an agent without cmd must be rejected: the picker would offer it and fail when pressed")
	}
	if !strings.Contains(cfg.Err.Error(), "cmd") {
		t.Errorf("err = %q, want it to say cmd is missing", cfg.Err)
	}
	if len(cfg.Keybindings) == 0 {
		t.Error("an invalid config cannot leave the TUI without keys")
	}
}

func scannerProject(m *manifest.Manifest) scanner.Project {
	return scanner.Project{Path: "/p", Name: "p", Manifest: m}
}

func conGrupo(primary, secondary string) *manifest.Manifest {
	return &manifest.Manifest{
		Name: "p", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "./p"}}, PrimaryGroup: primary, SecondaryGroup: secondary,
	}
}

// MEASURED: Load returns defaults together with the error, because there is no other config to read and the TUI must still boot.
func TestLoadSinHomeDevuelveElErrorYDefaults(t *testing.T) {
	t.Setenv("VROOM_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	cfg := Load()
	if cfg.Err == nil {
		t.Fatal("without HOME there is no config to read, and that must reach the user")
	}
	if !strings.Contains(cfg.Err.Error(), "home") {
		t.Errorf("err = %q, want it to say home is missing", cfg.Err)
	}
	if cfg.Ask.Launcher != "auto" || cfg.Scanner.Depth != Defaults().Scanner.Depth {
		t.Errorf("without a readable config you must boot with defaults, got %+v", cfg)
	}
	if len(cfg.Keybindings) == 0 {
		t.Error("without keybindings the TUI would have no assigned keys")
	}
}
