package launcher

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"vroom/internal/config"
)

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

func TestResolveAutoInsideHerdr(t *testing.T) {
	l, _ := newTestLauncher(t, config.Defaults().Ask)
	strategy, warn := l.Resolve()
	if strategy != StrategyHerdr || warn != "" {
		t.Errorf("Resolve = %s, %q", strategy, warn)
	}
}

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

func TestShellQuote(t *testing.T) {
	got := shellQuote([]string{"pi", "it's a 'test'"})
	if got != `'pi' 'it'\''s a '\''test'\'''` {
		t.Errorf("shellQuote = %q", got)
	}
}

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

func TestLaunchCustomFailure(t *testing.T) {
	cfg := config.Defaults().Ask
	cfg.Launcher = "custom"
	cfg.LauncherCmd = "exit 3"
	l := New(cfg)
	if _, err := l.Launch(StrategyCustom, req()); err == nil || !strings.Contains(err.Error(), "launcher_cmd") {
		t.Errorf("err = %v", err)
	}
}

func TestLaunchHerdrBadOutput(t *testing.T) {
	l, _ := newTestLauncher(t, config.Defaults().Ask)
	l.run = func(string, ...string) (string, error) { return "{}", nil }
	if _, err := l.Launch(StrategyHerdr, req()); err == nil || !strings.Contains(err.Error(), "pane id") {
		t.Errorf("err = %v", err)
	}
}
