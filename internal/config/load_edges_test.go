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
		t.Errorf("un config bajo un directorio inexistente dio error %v: es el mismo caso que no tener config", cfg.Err)
	}
	if cfg.Ask.Launcher != "auto" {
		t.Errorf("Ask.Launcher = %q, want auto", cfg.Ask.Launcher)
	}
	if len(cfg.Keybindings) == 0 {
		t.Error("sin keybindings la TUI no tendría ninguna tecla asignada")
	}
}

// $VROOM_CONFIG pointing at a directory is the likeliest misconfiguration: Stat succeeds, the decode fails, and the TUI must still boot.
func TestLoadConUnConfigQueEsUnDirectorioArrancaConDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VROOM_CONFIG", dir)

	cfg := Load()
	if cfg.Err == nil {
		t.Error("un config que es un directorio tiene que dar error: el usuario escribió la ruta de la carpeta")
	}
	if cfg.Ask.Launcher != "auto" || cfg.Scanner.Depth != Defaults().Scanner.Depth {
		t.Errorf("con un config ilegible hay que arrancar con defaults, got %+v", cfg)
	}
	if len(cfg.Keybindings) == 0 {
		t.Error("sin keybindings la TUI no tendría ninguna tecla asignada")
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
		t.Errorf("una clave desconocida no puede invalidar el config: %v", cfg.Err)
	}
	if cfg.Ask.Launcher != "inline" {
		t.Errorf("Ask.Launcher = %q: las claves conocidas tienen que aplicarse aunque haya desconocidas", cfg.Ask.Launcher)
	}
	if cfg.Scanner.Depth != 7 {
		t.Errorf("Scanner.Depth = %d, want 7", cfg.Scanner.Depth)
	}
}

// These accessors run over every scanned project, including bare directories with no manifest, so nil must yield "" instead of blanking the TUI.
func TestSecondaryOfConManifiestoNilEsCadenaVacia(t *testing.T) {
	if got := group.SecondaryOf(scannerProject(nil)); got != "" {
		t.Errorf("SecondaryOf sin manifiesto = %q, want cadena vacía", got)
	}
	if got := group.PrimaryOf(scannerProject(nil)); got != "" {
		t.Errorf("PrimaryOf sin manifiesto = %q, want cadena vacía", got)
	}
	if got := group.SecondaryOf(scannerProject(conGrupo("tienda", "backend"))); got != "backend" {
		t.Errorf("SecondaryOf = %q, want backend", got)
	}
	// An empty secondary means "directly under the primary", not "unset".
	if got := group.SecondaryOf(scannerProject(conGrupo("tienda", ""))); got != "" {
		t.Errorf("SecondaryOf sin secundario = %q, want cadena vacía", got)
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
			t.Errorf("la tecla %q de la acción %q no está en el inverso: al tocar un binding, "+
				"las demás acciones dejarían de funcionar", tecla, accion)
		}
	}
	if len(inv) != len(defaults) {
		t.Errorf("el inverso tiene %d entradas, want %d", len(inv), len(defaults))
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
		t.Fatal("un agente sin cmd tiene que rechazarse: el picker lo ofrecería y fallaría al pulsarlo")
	}
	if !strings.Contains(cfg.Err.Error(), "cmd") {
		t.Errorf("err = %q, want que diga que falta el cmd", cfg.Err)
	}
	if len(cfg.Keybindings) == 0 {
		t.Error("un config inválido no puede dejar la TUI sin teclas")
	}
}

func scannerProject(m *manifest.Manifest) scanner.Project {
	return scanner.Project{Path: "/p", Name: "p", Manifest: m}
}

func conGrupo(primary, secondary string) *manifest.Manifest {
	return &manifest.Manifest{
		Name: "p", Command: "./p", PrimaryGroup: primary, SecondaryGroup: secondary,
	}
}

// MEDIDO: Load returns defaults together with the error, because there is no other config to read and the TUI must still boot.
func TestLoadSinHomeDevuelveElErrorYDefaults(t *testing.T) {
	t.Setenv("VROOM_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	cfg := Load()
	if cfg.Err == nil {
		t.Fatal("sin HOME no hay config que leer, y eso tiene que llegar al usuario")
	}
	if !strings.Contains(cfg.Err.Error(), "home") {
		t.Errorf("err = %q, want que diga que falta el home", cfg.Err)
	}
	if cfg.Ask.Launcher != "auto" || cfg.Scanner.Depth != Defaults().Scanner.Depth {
		t.Errorf("sin config legible hay que arrancar con defaults, got %+v", cfg)
	}
	if len(cfg.Keybindings) == 0 {
		t.Error("sin keybindings la TUI no tendría ninguna tecla asignada")
	}
}
