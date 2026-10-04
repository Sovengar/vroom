package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/config"
)

// The warning must say both what happened (no herdr session) and what will happen instead (inline), or the user cannot tell whether the config is ignored or something failed.
func TestResolveConConfigExplicitoYSinSesionHerdrAvisa(t *testing.T) {
	l := &Launcher{
		cfg:  config.AskConfig{Launcher: "herdr"},
		env:  func(string) string { return "" }, // sin HERDR_ENV
		look: func(string) (string, error) { return "/usr/bin/herdr", nil },
		run:  func(string, ...string) (string, error) { return "", nil },
	}

	strategy, warn := l.Resolve()
	if strategy != StrategyInline {
		t.Errorf("estrategia = %q, want %q: sin herdr el fallback es inline", strategy, StrategyInline)
	}
	if warn == "" {
		t.Fatal("sin herdr y con launcher=herdr explícito tiene que haber aviso: si no, el usuario no sabe que su config se ignora")
	}
	if !strings.Contains(warn, "herdr") {
		t.Errorf("el aviso no nombra herdr: %q", warn)
	}
	if !strings.Contains(warn, "inline") {
		t.Errorf("el aviso no dice qué se hace en su lugar: %q", warn)
	}
}

// The warning exists only for the fallback, so a herdr user in a herdr session must never see one.
func TestResolveConHerdrExplicitoYSesionHerdrNoAvisa(t *testing.T) {
	l := &Launcher{
		cfg:  config.AskConfig{Launcher: "herdr"},
		env:  func(k string) string { return "1" },
		look: func(string) (string, error) { return "/usr/bin/herdr", nil },
		run:  func(string, ...string) (string, error) { return "", nil },
	}

	strategy, warn := l.Resolve()
	if strategy != StrategyHerdr {
		t.Errorf("estrategia = %q, want %q: con herdr explícito y sesión herdr se usa herdr", strategy, StrategyHerdr)
	}
	if warn != "" {
		t.Errorf("warn = %q: herdr disponible es exactamente lo pedido, no hay nada que avisar", warn)
	}
}

// In auto the inline fallback is what was asked for, so warning there would train the user to ignore every warning.
func TestResolveAutoSinSesionNoAvisa(t *testing.T) {
	l := &Launcher{
		cfg:  config.AskConfig{Launcher: "auto"},
		env:  func(string) string { return "" },
		look: func(string) (string, error) { return "", os.ErrNotExist },
		run:  func(string, ...string) (string, error) { return "", nil },
	}

	strategy, warn := l.Resolve()
	if strategy != StrategyInline || warn != "" {
		t.Errorf("auto sin herdr dio %q/%q, want inline y sin aviso: en auto el fallback es lo pedido", strategy, warn)
	}
}

// HERDR_ENV without the binary is checked as a pair on purpose, so the result matches the no-session case.
func TestResolveConSesionHerdrPeroSinBinario(t *testing.T) {
	newL := func(strategy string) *Launcher {
		return &Launcher{
			cfg: config.AskConfig{Launcher: strategy},
			env: func(k string) string {
				if k == "HERDR_ENV" {
					return "1"
				}
				return ""
			},
			look: func(string) (string, error) { return "", os.ErrNotExist },
			run:  func(string, ...string) (string, error) { return "", nil },
		}
	}

	if s, w := newL("auto").Resolve(); s != StrategyInline || w != "" {
		t.Errorf("auto con HERDR_ENV pero sin binario dio %q/%q, want inline sin aviso", s, w)
	}
	if s, w := newL("herdr").Resolve(); s != StrategyInline || w == "" {
		t.Errorf("herdr explícito con HERDR_ENV pero sin binario dio %q/%q, want inline con aviso", s, w)
	}
}

func TestResolveConEstrategiaDesconocidaCaeAAuto(t *testing.T) {
	l := &Launcher{
		cfg:  config.AskConfig{Launcher: "inventada"},
		env:  func(string) string { return "" },
		look: func(string) (string, error) { return "", os.ErrNotExist },
		run:  func(string, ...string) (string, error) { return "", nil },
	}
	if s, w := l.Resolve(); s != StrategyInline || w != "" {
		t.Errorf("una estrategia desconocida dio %q/%q, want el comportamiento de auto", s, w)
	}
}

// Inline and custom must not consult the environment, otherwise a shell launcher whose command happens to mention "herdr" would depend on the multiplexer.
func TestResolveConInlineYCustomNoPreguntaPorHerdr(t *testing.T) {
	for _, want := range []string{StrategyInline, StrategyCustom} {
		consulted := false
		l := &Launcher{
			cfg:  config.AskConfig{Launcher: want},
			env:  func(string) string { consulted = true; return "1" },
			look: func(string) (string, error) { consulted = true; return "/usr/bin/herdr", nil },
			run:  func(string, ...string) (string, error) { return "", nil },
		}
		got, warn := l.Resolve()
		if got != want {
			t.Errorf("Resolve(%q) = %q, want %q", want, got, want)
		}
		if warn != "" {
			t.Errorf("Resolve(%q) avisó: %q", want, warn)
		}
		if consulted {
			t.Errorf("Resolve(%q) consultó el entorno: no debe, la estrategia está decidida", want)
		}
	}
}

// Inline must not go through Launch: it suspends the TUI via tea.ExecProcess and has nothing to dispatch in the background, so an explicit error beats a silent no-op.
func TestLaunchRechazaInlineYLoDesconocido(t *testing.T) {
	l := New(config.AskConfig{})

	for _, strategy := range []string{StrategyInline, "inventada", ""} {
		out, err := l.Launch(strategy, req())
		if err == nil {
			t.Errorf("Launch(%q) = %q sin error: sólo herdr y custom son despachables en background", strategy, out)
		}
		if out != "" {
			t.Errorf("Launch(%q) devolvió %q además del error", strategy, out)
		}
	}
}

// herdr's own output is the only explanation of the failure, and the prefix names which step failed (split/create/run), which is what decides what to fix.
func TestLaunchPropagaElErrorDeHerdrConSuSalida(t *testing.T) {
	tests := []struct {
		name       string
		target     string // pane o tab: decide qué comando se llama primero
		failOn     string // subcomando que falla
		wantPre    string
		wantSalida string
	}{
		{"pane split", "pane", "split", "herdr pane split", "no se puede dividir el pane"},
		{"tab create", "tab", "tab", "herdr tab create", "no se puede crear el tab"},
		{"pane run", "pane", "run", "herdr pane run", "el pane no acepta comandos"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &Launcher{
				cfg:  config.AskConfig{Target: tt.target, Direction: "right"},
				env:  func(string) string { return "1" },
				look: func(string) (string, error) { return "/usr/bin/herdr", nil },
				run: func(name string, args ...string) (string, error) {
					for _, a := range args {
						if a == tt.failOn {
							return tt.wantSalida, os.ErrNotExist
						}
					}
					if tt.target == "tab" {
						return `{"result":{"root_pane":{"pane_id":"w1:p3"}}}`, nil
					}
					return `{"result":{"pane":{"pane_id":"w1:p9"}}}`, nil
				},
			}

			_, err := l.Launch(StrategyHerdr, req())
			if err == nil {
				t.Fatal("un herdr que falla tiene que dar error")
			}
			if !strings.Contains(err.Error(), tt.wantPre) {
				t.Errorf("err = %q, want que diga en qué paso falló (%q)", err, tt.wantPre)
			}
			if !strings.Contains(err.Error(), tt.wantSalida) {
				t.Errorf("err = %q, want que incluya la salida de herdr (%q)", err, tt.wantSalida)
			}
		})
	}
}

// Real case: a different herdr version, or a changed --json, yields exit 0 with no pane_id, and continuing would leave an agent that never started and no warning.
func TestLaunchHerdrSinPaneIdDaErrorAccionable(t *testing.T) {
	l := &Launcher{
		cfg: config.AskConfig{Target: "pane", Direction: "right"},
		env: func(string) string { return "1" },
		look: func(string) (string, error) {
			return "/usr/bin/herdr", nil
		},
		run: func(string, ...string) (string, error) {
			return `{"result":{"pane":{"nombre":"sin-id"}}}`, nil
		},
	}

	out, err := l.Launch(StrategyHerdr, req())
	if err == nil {
		t.Fatalf("sin pane id Launch devolvió %q sin error", out)
	}
	if !strings.Contains(err.Error(), "pane id") {
		t.Errorf("err = %q, want que diga que no se pudo resolver el pane id", err)
	}
}

// herdr's output comes from an external program this repo does not control, so parsePaneID must return "" on any surprise and let the caller raise an actionable error.
func TestParsePaneIDAguentaLasFormasQueNoSonLoEsperado(t *testing.T) {
	tests := []struct {
		name string
		out  string
		path string
		want string
	}{
		{"normal pane", `{"result":{"pane":{"pane_id":"w1:p9"}}}`, "result.pane.pane_id", "w1:p9"},
		{"normal root_pane", `{"result":{"root_pane":{"pane_id":"w1:p3"}}}`, "result.root_pane.pane_id", "w1:p3"},
		{"clave simple", `{"pane_id":"x"}`, "pane_id", "x"},
		{"no es json", "esto no es json", "result.pane.pane_id", ""},
		{"json vacío", "", "result.pane.pane_id", ""},
		{"array", `["result"]`, "result", ""},
		{"ruta inexistente", `{"result":{}}`, "result.pane.pane_id", ""},
		{"clave con valor null", `{"result":{"pane":null}}`, "result.pane.pane_id", ""},
		{"tipo incorrecto en medio", `{"result":"texto"}`, "result.pane.pane_id", ""},
		{"valor no string", `{"result":{"pane":{"pane_id":9}}}`, "result.pane.pane_id", ""},
		{"json truncado", `{"result":{`, "result.pane.pane_id", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePaneID(tt.out, tt.path); got != tt.want {
				t.Errorf("parsePaneID(%q, %q) = %q, want %q", tt.out, tt.path, got, tt.want)
			}
		})
	}
}

func TestParsePaneIDConLaSalidaRealDeHerdrTab(t *testing.T) {
	l, log := newTestLauncher(t, config.AskConfig{Target: "tab"})
	if out, err := l.Launch(StrategyHerdr, req()); err != nil {
		t.Fatalf("Launch: %v", err)
	} else if !strings.Contains(out, "w1:p3") {
		t.Errorf("salida = %q: el tab tiene que reportar el root_pane, no un pane cualquiera", out)
	}

	calls := readCalls(t, log)
	if len(calls) != 2 {
		t.Fatalf("se llamaron %d veces a herdr, want 2 (create y run): %v", len(calls), calls)
	}
	if !strings.HasPrefix(calls[0], "tab create") {
		t.Errorf("la primera llamada = %q", calls[0])
	}
	if !strings.HasPrefix(calls[1], "pane run w1:p3") {
		t.Errorf("la segunda llamada = %q: el agente tiene que ir al pane que se acaba de crear", calls[1])
	}
}

// A fake sh on PATH records its argv because the goal is what REACHES the template, not whether sh works.
func TestLaunchCustomExpandeLosPlaceholdersYLosProtege(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "out.log")
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s' \"$*\" > " + log + "\n"
	if err := os.WriteFile(filepath.Join(bin, "sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	l := &Launcher{
		cfg: config.AskConfig{
			Launcher:    "custom",
			LauncherCmd: "agente --dir {dir} --nombre {agent} -- {cmd}",
		},
		run: func(name string, args ...string) (string, error) {
			return runReal(name, args...)
		},
	}

	r := Request{
		Agent: "open code",
		Args:  []string{"opencode", "--prompt", "arregla el bug; con punto y coma"},
		Dir:   "/srv/mi proyecto",
	}
	out, err := l.Launch(StrategyCustom, r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "custom") {
		t.Errorf("salida = %q, want la confirmación de custom", out)
	}

	got := readFileString(t, log)
	if !strings.Contains(got, "'/srv/mi proyecto'") {
		t.Errorf("el directorio no llegó entrecomillado:\n%s", got)
	}
	if !strings.Contains(got, "'open code'") {
		t.Errorf("el agente no llegó entrecomillado:\n%s", got)
	}
	// MEDIDO: {cmd} is expanded token by token, each quoted separately, which is what keeps a semicolon in the prompt from executing.
	if !strings.Contains(got, "'arregla el bug; con punto y coma'") {
		t.Errorf("el prompt no llegó entrecomillado: un ';' suelto ejecutaría el resto\n%s", got)
	}
	if !strings.Contains(got, "'opencode' '--prompt'") {
		t.Errorf("el argv debería llegar token a token entrecomillado:\n%s", got)
	}
}

// Naming launcher_cmd is what connects "the agent did not start" with the half of the message the user can actually fix.
func TestLaunchCustomPropagaElErrorDeLaPlantilla(t *testing.T) {
	l := &Launcher{
		cfg: config.AskConfig{Launcher: "custom", LauncherCmd: "exit 3"},
		run: func(string, ...string) (string, error) {
			return "salida de error", os.ErrNotExist
		},
	}

	_, err := l.Launch(StrategyCustom, req())
	if err == nil {
		t.Fatal("una plantilla que falla tiene que dar error")
	}
	if !strings.Contains(err.Error(), "launcher_cmd") {
		t.Errorf("err = %q, want que nombre launcher_cmd: es lo que el usuario tiene que arreglar", err)
	}
	if !strings.Contains(err.Error(), "salida de error") {
		t.Errorf("err = %q, want que incluya la salida del comando", err)
	}
}

// The cwd is what makes "the agent works on this project" true without anyone writing it into the prompt.
func TestInlineCmdApuntaAlDirectorioDelProyecto(t *testing.T) {
	r := req()
	r.Dir = "/srv/proyecto"

	cmd := New(config.AskConfig{}).InlineCmd(r)
	if cmd.Dir != "/srv/proyecto" {
		t.Errorf("cmd.Dir = %q, want el directorio del proyecto", cmd.Dir)
	}
	// The request argv already carries the binary, so the agent name must not be prepended again.
	if got := strings.Join(cmd.Args, " "); got != "opencode --prompt fix the bug" {
		t.Errorf("cmd.Args = %q", got)
	}
}

func runReal(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no se pudo leer %s: %v", path, err)
	}
	return string(data)
}
