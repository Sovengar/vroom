// Package launcher dispatches the ask AI action to herdr, inline or a custom shell template, resolved from config so the multiplexer can be swapped without recompiling.
package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"vroom/internal/config"
)

type Request struct {
	Agent string
	Args  []string
	Dir   string
}

const (
	StrategyHerdr  = "herdr"
	StrategyInline = "inline"
	StrategyCustom = "custom"
)

type Launcher struct {
	cfg config.AskConfig

	env  func(string) string
	look func(string) (string, error)
	run  func(name string, args ...string) (string, error)
}

func New(cfg config.AskConfig) *Launcher {
	return &Launcher{
		cfg:  cfg,
		env:  os.Getenv,
		look: exec.LookPath,
		run: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return string(out), err
		},
	}
}

func (l *Launcher) herdrAvailable() bool {
	if l.env("HERDR_ENV") != "1" {
		return false
	}
	_, err := l.look("herdr")
	return err == nil
}

// A non-empty warn means an explicit herdr config fell back and the TUI must notify.
func (l *Launcher) Resolve() (strategy, warn string) {
	switch l.cfg.Launcher {
	case "herdr":
		if l.herdrAvailable() {
			return StrategyHerdr, ""
		}
		return StrategyInline, "herdr not available — running agent inline (foreground)"
	case "inline":
		return StrategyInline, ""
	case "custom":
		return StrategyCustom, ""
	default: // auto
		if l.herdrAvailable() {
			return StrategyHerdr, ""
		}
		return StrategyInline, ""
	}
}

func (l *Launcher) InlineCmd(req Request) *exec.Cmd {
	cmd := exec.Command(req.Args[0], req.Args[1:]...)
	cmd.Dir = req.Dir
	return cmd
}

// Inline must never reach here: it suspends vroom, so only herdr and custom run in the background.
func (l *Launcher) Launch(strategy string, req Request) (string, error) {
	switch strategy {
	case StrategyHerdr:
		return l.launchHerdr(req)
	case StrategyCustom:
		return l.launchCustom(req)
	default:
		return "", fmt.Errorf("strategy %q no es despachable en background", strategy)
	}
}

func (l *Launcher) launchHerdr(req Request) (string, error) {
	var paneID string
	if l.cfg.Target == "tab" {
		out, err := l.run("herdr", "tab", "create", "--workspace", l.env("HERDR_WORKSPACE_ID"))
		if err != nil {
			return "", fmt.Errorf("herdr tab create: %v (%s)", err, strings.TrimSpace(out))
		}
		paneID = parsePaneID(out, "result.root_pane.pane_id")
	} else {
		args := []string{"pane", "split", "--current", "--direction", l.cfg.Direction, "--cwd", req.Dir}
		if !l.cfg.Focus {
			args = append(args, "--no-focus")
		}
		out, err := l.run("herdr", args...)
		if err != nil {
			return "", fmt.Errorf("herdr pane split: %v (%s)", err, strings.TrimSpace(out))
		}
		paneID = parsePaneID(out, "result.pane.pane_id")
	}
	if paneID == "" {
		return "", fmt.Errorf("no se pudo resolver el pane id en la salida de herdr")
	}

	cmdStr := shellQuote(req.Args)
	if out, err := l.run("herdr", "pane", "run", paneID, cmdStr); err != nil {
		return "", fmt.Errorf("herdr pane run: %v (%s)", err, strings.TrimSpace(out))
	}
	return fmt.Sprintf("%s → herdr %s %s", req.Agent, l.cfg.Target, paneID), nil
}

// Placeholders are shell-quoted so a prompt with spaces or quotes cannot inject shell syntax.
func (l *Launcher) launchCustom(req Request) (string, error) {
	tpl := l.cfg.LauncherCmd
	repl := strings.NewReplacer(
		"{dir}", shellQuote([]string{req.Dir}),
		"{agent}", shellQuote([]string{req.Agent}),
		"{cmd}", shellQuote(req.Args),
	)
	script := repl.Replace(tpl)
	if out, err := l.run("sh", "-c", script); err != nil {
		return "", fmt.Errorf("launcher_cmd: %v (%s)", err, strings.TrimSpace(out))
	}
	return fmt.Sprintf("%s launched (custom)", req.Agent), nil
}

func parsePaneID(out, path string) string {
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return ""
	}
	cur := any(doc)
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur, ok = m[seg]
		if !ok {
			return ""
		}
	}
	s, _ := cur.(string)
	return s
}

func shellQuote(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
		b.WriteByte('\'')
	}
	return b.String()
}
