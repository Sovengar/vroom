// Package launcher despacha la acción de ask AI según la configuración
// global: herdr (pane/tab nuevo del multiplexer),
// inline (suspende vroom y corre el agente en primer plano, patrón wt)
// o custom (plantilla de shell). El launcher "auto" usa herdr si vroom
// corre dentro de una sesión herdr y cae a inline si no.
//
// El despacho es EXTERNO y configurable sin recompilar: mañana puede
// ser tmux con ask.launcher = "custom".
package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"vroom/internal/config"
)

// Request describe el despacho de un ask AI.
type Request struct {
	Agent string   // nombre del agente (mensajes y plantillas)
	Args  []string // argv del agente con el prompt ya expandido
	Dir   string   // directorio del proyecto (cwd del agente)
}

// Estrategias válidas.
const (
	StrategyHerdr  = "herdr"
	StrategyInline = "inline"
	StrategyCustom = "custom"
)

// Launcher resuelve y ejecuta la estrategia configurada. Los campos
// env/look/run son inyectables para tests.
type Launcher struct {
	cfg config.AskConfig

	env  func(string) string
	look func(string) (string, error)
	run  func(name string, args ...string) (string, error) // CombinedOutput
}

// New construye el launcher con las implementaciones reales.
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

// herdrAvailable reporta si vroom corre dentro de una sesión herdr y
// el binario está en PATH.
func (l *Launcher) herdrAvailable() bool {
	if l.env("HERDR_ENV") != "1" {
		return false
	}
	_, err := l.look("herdr")
	return err == nil
}

// Resolve decide la estrategia efectiva. warn != "" indica un fallback
// que la TUI debe notificar (config explícita herdr sin sesión herdr).
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

// InlineCmd construye el exec.Cmd del agente para tea.ExecProcess
// (estrategia inline: vroom se suspende mientras el agente corre).
func (l *Launcher) InlineCmd(req Request) *exec.Cmd {
	cmd := exec.Command(req.Args[0], req.Args[1:]...)
	cmd.Dir = req.Dir
	return cmd
}

// Launch ejecuta una estrategia en background (herdr o custom): se
// llama dentro de una goroutine (tea.Cmd) y devuelve el mensaje de
// éxito. Inline NO pasa por aquí.
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

// launchHerdr abre un pane/tab de herdr y lanza el agente dentro.
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

// launchCustom expande la plantilla del config y la corre con sh -c.
// Los placeholders se insertan shell-quoteados para que la plantilla
// sea texto de shell seguro de componer.
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

// parsePaneID extrae una ruta JSON punteada ("result.pane.pane_id")
// de la salida de herdr.
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

// shellQuote compone los argv en una línea de shell POSIX segura:
// cada argumento entre comillas simples con escape de comillas.
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
