package launcher

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"vroom/internal/config"
)

// newTestLauncher construye un Launcher con herdr fake: el binario
// graba sus argv en un fichero y emite el JSON que herdr devolvería.
func newTestLauncher(t *testing.T, cfg config.AskConfig) (*Launcher, string) {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n" +
		"case \"$1 $2\" in\n" +
		"  \"pane split\") echo '{\"result\":{\"pane\":{\"pane_id\":\"w1:p9\"}}}' ;;\n" +
		"  \"tab create\") echo '{\"result\":{\"tab\":{\"tab_id\":\"w1:t2\"},\"root_pane\":{\"pane_id\":\"w1:p3\"}}}' ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "w1")

	l := New(cfg)
	return l, log
}

func req() Request {
	return Request{Agent: "opencode", Args: []string{"opencode", "--prompt", "fix the bug"}, Dir: "/proj"}
}

func readCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// auto dentro de herdr → estrategia herdr.
func TestResolveAutoInsideHerdr(t *testing.T) {
	l, _ := newTestLauncher(t, config.Defaults().Ask)
	strategy, warn := l.Resolve()
	if strategy != StrategyHerdr || warn != "" {
		t.Errorf("Resolve = %s, %q", strategy, warn)
	}
}

// herdr explícito sin sesión → inline con warn.
func TestResolveExplicitHerdrFallback(t *testing.T) {
	cfg := config.Defaults().Ask
	cfg.Launcher = "herdr"
	l, _ := newTestLauncher(t, cfg)
	l.env = func(string) string { return "" } // HERDR_ENV fuera
	strategy, warn := l.Resolve()
	if strategy != StrategyInline {
		t.Errorf("strategy = %s, want inline", strategy)
	}
	if !strings.Contains(warn, "herdr not available") {
		t.Errorf("warn = %q", warn)
	}
}

// Launch herdr con target=pane: split + pane run con comando quoteado.
func TestLaunchHerdrPane(t *testing.T) {
	l, log := newTestLauncher(t, config.Defaults().Ask)
	msg, err := l.Launch(StrategyHerdr, req())
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if !strings.Contains(msg, "opencode → herdr pane w1:p9") {
		t.Errorf("msg = %q", msg)
	}
	calls := readCalls(t, log)
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want 2", calls)
	}
	if !strings.Contains(calls[0], "pane split --current --direction right --cwd /proj --no-focus") {
		t.Errorf("split = %q", calls[0])
	}
	if !strings.Contains(calls[1], "pane run w1:p9 'opencode' '--prompt' 'fix the bug'") {
		t.Errorf("run = %q", calls[1])
	}
}

// Target=tab: tab create + run en el root pane; focus=true omite --no-focus.
func TestLaunchHerdrTab(t *testing.T) {
	cfg := config.Defaults().Ask
	cfg.Target = "tab"
	cfg.Focus = true
	l, log := newTestLauncher(t, cfg)
	msg, err := l.Launch(StrategyHerdr, req())
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if !strings.Contains(msg, "herdr tab w1:p3") {
		t.Errorf("msg = %q", msg)
	}
	calls := readCalls(t, log)
	if len(calls) != 2 || !strings.Contains(calls[0], "tab create --workspace w1") {
		t.Fatalf("calls = %v", calls)
	}
	if strings.Contains(calls[0], "--no-focus") {
		t.Error("focus=true no debe añadir --no-focus")
	}
	if !strings.Contains(calls[1], "pane run w1:p3") {
		t.Errorf("run = %q", calls[1])
	}
}

// Direction=down se propaga al split.
func TestLaunchHerdrDirectionDown(t *testing.T) {
	cfg := config.Defaults().Ask
	cfg.Direction = "down"
	l, log := newTestLauncher(t, cfg)
	if _, err := l.Launch(StrategyHerdr, req()); err != nil {
		t.Fatal(err)
	}
	if calls := readCalls(t, log); !strings.Contains(calls[0], "--direction down") {
		t.Errorf("split = %q", calls[0])
	}
}

// El prompt con comillas sobrevive el shell-quoting de pane run.
func TestShellQuote(t *testing.T) {
	got := shellQuote([]string{"pi", "it's a 'test'"})
	if got != `'pi' 'it'\''s a '\''test'\'''` {
		t.Errorf("shellQuote = %q", got)
	}
}

// Inline: el cmd lleva el cwd del proyecto.
func TestInlineCmd(t *testing.T) {
	l := New(config.Defaults().Ask)
	cmd := l.InlineCmd(req())
	if !reflect.DeepEqual(cmd.Args, []string{"opencode", "--prompt", "fix the bug"}) {
		t.Errorf("cmd.Args = %v", cmd.Args)
	}
	if cmd.Dir != "/proj" {
		t.Errorf("cmd.Dir = %q", cmd.Dir)
	}
}

// Custom: plantilla con placeholders quoteados, corre vía sh -c.
func TestLaunchCustom(t *testing.T) {
	cfg := config.Defaults().Ask
	cfg.Launcher = "custom"
	cfg.LauncherCmd = "tmux new-window -c {dir} -n vroom-{agent} -- {cmd}"
	l := New(cfg)
	var got string
	l.run = func(name string, args ...string) (string, error) {
		got = name + " " + strings.Join(args, " ")
		return "", nil
	}
	msg, err := l.Launch(StrategyCustom, req())
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if !strings.Contains(msg, "custom") {
		t.Errorf("msg = %q", msg)
	}
	want := `sh -c tmux new-window -c '/proj' -n vroom-'opencode' -- 'opencode' '--prompt' 'fix the bug'`
	if got != want {
		t.Errorf("script = %q, want %q", got, want)
	}
}

// Custom con fallo del comando → error con salida.
func TestLaunchCustomFailure(t *testing.T) {
	cfg := config.Defaults().Ask
	cfg.Launcher = "custom"
	cfg.LauncherCmd = "exit 3"
	l := New(cfg)
	if _, err := l.Launch(StrategyCustom, req()); err == nil || !strings.Contains(err.Error(), "launcher_cmd") {
		t.Errorf("err = %v", err)
	}
}

// herdr con salida sin pane_id → error claro.
func TestLaunchHerdrBadOutput(t *testing.T) {
	l, _ := newTestLauncher(t, config.Defaults().Ask)
	l.run = func(string, ...string) (string, error) { return "{}", nil }
	if _, err := l.Launch(StrategyHerdr, req()); err == nil || !strings.Contains(err.Error(), "pane id") {
		t.Errorf("err = %v", err)
	}
}
