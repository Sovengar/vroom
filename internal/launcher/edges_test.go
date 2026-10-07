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
		env:  func(string) string { return "" }, // no HERDR_ENV
		look: func(string) (string, error) { return "/usr/bin/herdr", nil },
		run:  func(string, ...string) (string, error) { return "", nil },
	}

	strategy, warn := l.Resolve()
	if strategy != StrategyInline {
		t.Errorf("strategy = %q, want %q: without herdr the fallback is inline", strategy, StrategyInline)
	}
	if warn == "" {
		t.Fatal("without herdr and with explicit launcher=herdr there must be a warning: otherwise the user does not know their config is ignored")
	}
	if !strings.Contains(warn, "herdr") {
		t.Errorf("the warning does not name herdr: %q", warn)
	}
	if !strings.Contains(warn, "inline") {
		t.Errorf("the warning does not say what is done instead: %q", warn)
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
		t.Errorf("strategy = %q, want %q: with explicit herdr and a herdr session, herdr is used", strategy, StrategyHerdr)
	}
	if warn != "" {
		t.Errorf("warn = %q: herdr available is exactly what was asked, there is nothing to warn about", warn)
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
		t.Errorf("auto without herdr gave %q/%q, want inline and no warning: in auto the fallback is what was asked", strategy, warn)
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
		t.Errorf("auto with HERDR_ENV but no binary gave %q/%q, want inline without warning", s, w)
	}
	if s, w := newL("herdr").Resolve(); s != StrategyInline || w == "" {
		t.Errorf("explicit herdr with HERDR_ENV but no binary gave %q/%q, want inline with warning", s, w)
	}
}

func TestResolveConEstrategiaDesconocidaCaeAAuto(t *testing.T) {
	l := &Launcher{
		cfg:  config.AskConfig{Launcher: "made-up"},
		env:  func(string) string { return "" },
		look: func(string) (string, error) { return "", os.ErrNotExist },
		run:  func(string, ...string) (string, error) { return "", nil },
	}
	if s, w := l.Resolve(); s != StrategyInline || w != "" {
		t.Errorf("an unknown strategy gave %q/%q, want the behavior of auto", s, w)
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
			t.Errorf("Resolve(%q) warned: %q", want, warn)
		}
		if consulted {
			t.Errorf("Resolve(%q) consulted the environment: it must not, the strategy is decided", want)
		}
	}
}

// Inline must not go through Launch: it suspends the TUI via tea.ExecProcess and has nothing to dispatch in the background, so an explicit error beats a silent no-op.
func TestLaunchRechazaInlineYLoDesconocido(t *testing.T) {
	l := New(config.AskConfig{})

	for _, strategy := range []string{StrategyInline, "made-up", ""} {
		out, err := l.Launch(strategy, req())
		if err == nil {
			t.Errorf("Launch(%q) = %q without error: only herdr and custom are dispatchable in the background", strategy, out)
		}
		if out != "" {
			t.Errorf("Launch(%q) returned %q in addition to the error", strategy, out)
		}
	}
}

// herdr's own output is the only explanation of the failure, and the prefix names which step failed (split/create/run), which is what decides what to fix.
func TestLaunchPropagaElErrorDeHerdrConSuSalida(t *testing.T) {
	tests := []struct {
		name       string
		target     string // pane or tab: decides which command is called first
		failOn     string // subcommand that fails
		wantPre    string
		wantSalida string
	}{
		{"pane split", "pane", "split", "herdr pane split", "cannot split the pane"},
		{"tab create", "tab", "tab", "herdr tab create", "cannot create the tab"},
		{"pane run", "pane", "run", "herdr pane run", "the pane does not accept commands"},
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
				t.Fatal("a failing herdr must produce an error")
			}
			if !strings.Contains(err.Error(), tt.wantPre) {
				t.Errorf("err = %q, want it to say at which step it failed (%q)", err, tt.wantPre)
			}
			if !strings.Contains(err.Error(), tt.wantSalida) {
				t.Errorf("err = %q, want it to include herdr's output (%q)", err, tt.wantSalida)
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
			return `{"result":{"pane":{"name":"no-id"}}}`, nil
		},
	}

	out, err := l.Launch(StrategyHerdr, req())
	if err == nil {
		t.Fatalf("without pane id Launch returned %q without error", out)
	}
	if !strings.Contains(err.Error(), "pane id") {
		t.Errorf("err = %q, want it to say the pane id could not be resolved", err)
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
		{"simple key", `{"pane_id":"x"}`, "pane_id", "x"},
		{"not json", "this is not json", "result.pane.pane_id", ""},
		{"empty json", "", "result.pane.pane_id", ""},
		{"array", `["result"]`, "result", ""},
		{"nonexistent path", `{"result":{}}`, "result.pane.pane_id", ""},
		{"key with null value", `{"result":{"pane":null}}`, "result.pane.pane_id", ""},
		{"wrong type in the middle", `{"result":"texto"}`, "result.pane.pane_id", ""},
		{"non-string value", `{"result":{"pane":{"pane_id":9}}}`, "result.pane.pane_id", ""},
		{"truncated json", `{"result":{`, "result.pane.pane_id", ""},
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
		t.Errorf("output = %q: the tab must report the root_pane, not some other pane", out)
	}

	calls := readCalls(t, log)
	if len(calls) != 2 {
		t.Fatalf("herdr was called %d times, want 2 (create and run): %v", len(calls), calls)
	}
	if !strings.HasPrefix(calls[0], "tab create") {
		t.Errorf("the first call = %q", calls[0])
	}
	if !strings.HasPrefix(calls[1], "pane run w1:p3") {
		t.Errorf("the second call = %q: the agent must go to the pane that was just created", calls[1])
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
			LauncherCmd: "agent --dir {dir} --name {agent} -- {cmd}",
		},
		run: func(name string, args ...string) (string, error) {
			return runReal(name, args...)
		},
	}

	r := Request{
		Agent: "open code",
		Args:  []string{"opencode", "--prompt", "fix the bug; with a semicolon"},
		Dir:   "/srv/my project",
	}
	out, err := l.Launch(StrategyCustom, r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "custom") {
		t.Errorf("output = %q, want the custom confirmation", out)
	}

	got := readFileString(t, log)
	if !strings.Contains(got, "'/srv/my project'") {
		t.Errorf("the directory did not arrive quoted:\n%s", got)
	}
	if !strings.Contains(got, "'open code'") {
		t.Errorf("the agent did not arrive quoted:\n%s", got)
	}
	// MEASURED: {cmd} is expanded token by token, each quoted separately, which is what keeps a semicolon in the prompt from executing.
	if !strings.Contains(got, "'fix the bug; with a semicolon'") {
		t.Errorf("the prompt did not arrive quoted: a loose ';' would execute the rest\n%s", got)
	}
	if !strings.Contains(got, "'opencode' '--prompt'") {
		t.Errorf("the argv should arrive token by token quoted:\n%s", got)
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
		t.Fatal("a failing template must produce an error")
	}
	if !strings.Contains(err.Error(), "launcher_cmd") {
		t.Errorf("err = %q, want it to name launcher_cmd: it is what the user has to fix", err)
	}
	if !strings.Contains(err.Error(), "salida de error") {
		t.Errorf("err = %q, want it to include the command output", err)
	}
}

// The cwd is what makes "the agent works on this project" true without anyone writing it into the prompt.
func TestInlineCmdApuntaAlDirectorioDelProyecto(t *testing.T) {
	r := req()
	r.Dir = "/srv/proyecto"

	cmd := New(config.AskConfig{}).InlineCmd(r)
	if cmd.Dir != "/srv/proyecto" {
		t.Errorf("cmd.Dir = %q, want the project directory", cmd.Dir)
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
		t.Fatalf("could not read %s: %v", path, err)
	}
	return string(data)
}
