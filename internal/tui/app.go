package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"vroom/internal/agents"
	"vroom/internal/config"
	"vroom/internal/gitinfo"
	"vroom/internal/group"
	"vroom/internal/launcher"
	"vroom/internal/mise"
	"vroom/internal/orchestrate"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
	"vroom/internal/tail"
)

const (
	pollInterval    = 2 * time.Second        // spec 0001 R13
	consoleTick     = 400 * time.Millisecond // tail de la consola (0002 R19)
	maxConsoleBytes = 192 * 1024             // cap del buffer por stream (S19.6)
	treeWidth       = 30                     // ancho fijo de la columna de árbol (R18)
	detailsWidthMin = 40                     // ancho mínimo de la zona derecha para detalles (S18.3)
	detailsHeight   = 12                     // alto fijo del panel de detalles
	wheelLines      = 3                      // líneas por click de rueda en la consola
	askMinHeight    = 6                      // filas iniciales del textarea del ask (R35: grande de inicio)
	askMaxHeightCap = 16                     // cap absoluto del textarea del ask
)

// groupConsoleHint es el placeholder de consola con un grupo seleccionado.
const groupConsoleHint = "group selected — pick a service to view its console"

// tabKind es la pestaña activa del panel inferior (R18).
type tabKind int

const (
	tabConsole tabKind = iota
	tabThreads
)

// streamMode es el stream mostrado en la consola (S19.3): mergeado por
// defecto, con toggle para aislar stdout o stderr.
type streamMode int

const (
	streamMerged streamMode = iota
	streamStdout
	streamStderr
)

func (s streamMode) String() string {
	switch s {
	case streamStdout:
		return "stdout"
	case streamStderr:
		return "stderr"
	default:
		return "merged"
	}
}

// uiStatus es el estado mostrado en la UI, incluyendo los transitorios
// starting/stopping (spec 0001 R14).
type uiStatus string

const (
	statusUnconfigured uiStatus = "sin configurar"
	statusStopped      uiStatus = "stopped"
	statusRunning      uiStatus = "running"
	statusUnknown      uiStatus = "unknown"
	statusStarting     uiStatus = "starting"
	statusStopping     uiStatus = "stopping"
)

// ServiceState es el estado en memoria de un proyecto gestionable.
type ServiceState struct {
	Status uiStatus
	Meta   state.Meta
}

// Model es el modelo principal de la TUI: un único dashboard (0002 R18).
type Model struct {
	width, height int
	root          string
	store         *state.Store
	manager       process.Manager

	projects []scanner.Project
	entries  []group.Entry
	services map[string]*ServiceState // clave: ruta absoluta del proyecto
	usedFD   bool                     // true si el scan usó fd

	cursor  int // índice en tree (grupos y proyectos, cíclico)
	treeTop int // primera línea visible del árbol (auto-scroll, S18.5)

	activeTab     tabKind
	stream        streamMode // modo de la consola (merged/stdout/stderr)
	consoleFollow bool       // auto-scroll de la consola (S19.4)

	tree          []treeItem               // filas navegables del árbol
	collapsed     map[string]bool          // grupos colapsados (R24)
	consoleStates map[string]*consoleState // buffers/offsets por servicio
	threads       map[string][]threadRow   // tabla de hilos por servicio
	threadPrev    map[string]*threadSample // muestra previa para CPU%
	branches      map[string]string        // rama git por servicio

	message          string
	messageExpiresAt time.Time // cero = sin expiración
	pendingRestart   map[string]bool

	jobs map[string]string // job one-shot en curso por proyecto: build/install/task (0003 R27)

	pickerOpen   bool // modal de selección (tasks de mise / agentes, 0003 R28 + 0004 R32)
	pickerKind   pickerKind
	pickerItems  []pickerItem
	pickerCursor int

	// Ask AI (0004 R32): dispatch configurable vía config global.
	cfg           config.Config
	askLauncher   *launcher.Launcher
	askAgents     []agents.Agent
	askAgent      agents.Agent
	askPromptOpen bool
	promptInput   textarea.Model // multi-línea: alto dinámico + scroll (0005 R35)

	// Keybindings configurables (0008 R50): mapa inverso tecla → acción
	// precalculado desde la config; la resolución por tecla es O(1) y
	// determinista.
	keyActions map[string]string

	// Filtro del árbol con "/" (0007 R40): barra inline en la primera
	// línea de la columna del árbol; el texto filtra en vivo.
	filterOpen  bool            // box abierto: captura las teclas
	filterInput textinput.Model // prompt "/", placeholder "filter…"
	filterText  string          // texto aplicado ("" = sin filtro)

	// Terminal embebida con "!" (0009 R52): modal con el shell del
	// usuario en un PTY renderizado por un emulador VT. Ocultar el
	// modal NO mata la sesión (R56): term apunta a la sesión viva,
	// termOpen solo controla la vista.
	termOpen bool         // modal visible: captura las teclas
	term     *termSession // sesión del shell (nil hasta el primer !)

	// Orquestación de stacks (0010): compose file y engine.
	composeFile *orchestrate.ComposeFile // nil si no hay compose file
	engine      *orchestrate.Engine      // motor de orquestación

	bodyH        int // alto de la zona de cuerpo (árbol + panel derecho)
	rightW       int // ancho del panel derecho
	contentH     int // alto del contenido de la pestaña (bajo la barra de pestañas)
	detailsShown bool
	detailsTop   int           // scroll offset del panel de detalles
	consoleView  viewport.Model

	// Spinner animado para servicios con estado desconocido.
	spinner spinner.Model
	// Spinner animado para servicios en arranque.
	startSpinner spinner.Model
}

// notify muestra un mensaje en la barra de estado con expiración
// automática (el polling no debe borrarlo antes de tiempo).
func (m *Model) notify(s string) {
	m.message = s
	m.messageExpiresAt = time.Now().Add(5 * time.Second)
}

func (m *Model) clearMessage() {
	m.message, m.messageExpiresAt = "", time.Time{}
}

// findComposeFile busca .vroom-compose.toml subiendo por los directorios
// padre de cada proyecto escaneado, sin salir de root. El compose file
// vive al mismo nivel que los directorios de proyecto que orquesta.
func findComposeFile(root string, projects []scanner.Project) (*orchestrate.ComposeFile, error) {
	seen := make(map[string]bool)
	for _, p := range projects {
		dir := filepath.Dir(p.Path)
		for {
			if seen[dir] {
				if dir == root {
					break
				}
				dir = filepath.Dir(dir)
				continue
			}
			seen[dir] = true
			if cf, err := orchestrate.ParseComposeFile(dir); err == nil {
				return cf, nil
			}
			if dir == root {
				break
			}
			dir = filepath.Dir(dir)
		}
	}
	return nil, fmt.Errorf("no %s found in any project directory under %s", orchestrate.ComposeFileName, root)
}

// New construye el modelo: escanea root (CWD o config), agrupa y fija estados iniciales.
func New(store *state.Store, manager process.Manager, root string) Model {
	cfg := config.Load()

	// Resolver root: si config tiene scanner.root, usarlo; si no, CWD.
	scanRoot := root
	if cfg.Scanner.Root != "" {
		scanRoot = cfg.Scanner.Root
		// Expandir ~ al home directory
		if strings.HasPrefix(scanRoot, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				scanRoot = filepath.Join(home, scanRoot[1:])
			}
		}
		if !filepath.IsAbs(scanRoot) {
			scanRoot = filepath.Join(root, scanRoot)
		}
	}
	scanResult, err := scanner.Scan(scanRoot, cfg.Scanner.Depth)
	projects := scanResult.Projects
	ta := textarea.New()
	ta.Placeholder = "what should the agent do?"
	ta.Prompt = "› "
	ta.ShowLineNumbers = false
	ta.DynamicHeight = true // crece con el contenido hasta MaxHeight; luego scroll
	ta.MinHeight = askMinHeight
	fi := textinput.New()
	fi.Prompt = "/"
	fi.Placeholder = "filter…"
	fi.SetWidth(treeWidth - 4)
	m := Model{
		root:           root,
		store:          store,
		manager:        manager,
		cfg:            cfg,
		keyActions:     cfg.KeyByAction(),
		askLauncher:    launcher.New(cfg.Ask),
		promptInput:    ta,
		filterInput:    fi,
		services:       make(map[string]*ServiceState, len(projects)),
		pendingRestart: make(map[string]bool),
		jobs:           make(map[string]string),
		collapsed:      make(map[string]bool),
		consoleStates:  make(map[string]*consoleState),
		threads:        make(map[string][]threadRow),
		threadPrev:     make(map[string]*threadSample),
		branches:       make(map[string]string),
		width:          80,
		height:         24,
		activeTab:      tabConsole,
		stream:         streamMerged,
		consoleFollow:  true,
		consoleView:    viewport.New(),
		usedFD:         scanResult.UsedFD,
		spinner: spinner.New(
			spinner.WithSpinner(spinner.Dot),
			spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("11"))),
		),
		startSpinner: spinner.New(
			spinner.WithSpinner(spinner.Dot),
			spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("12"))),
		),
	}
	if err != nil {
		m.notify("error scanning projects: " + err.Error())
	}
	if cfg.Err != nil {
		m.notify(cfg.Err.Error()) // config malformado: defaults + aviso
	}
	m.projects = projects
	for _, p := range projects {
		if p.Configured {
			m.services[p.Path] = &ServiceState{Status: statusStopped}
		} else {
			m.services[p.Path] = &ServiceState{Status: statusUnconfigured}
		}
		m.branches[p.Path] = gitinfo.Branch(p.Path)
	}
	// Degradación por repo (0011): si git falló o falta, avisar sin
	// ocultar proyectos ni romper la TUI.
	for _, p := range projects {
		if p.WorktreeErr != "" {
			m.notify("worktree topology unavailable: " + p.WorktreeErr)
			break
		}
	}
	m.entries = group.Arrange(projects)
	// Cargar compose file: buscar en scanRoot y sus subdirectores
	// directos (0010). Los stacks se manejan por separado — no se
	// mezclan con Arrange.
	if cf, err := findComposeFile(scanRoot, projects); err == nil {
		m.composeFile = cf
		m.engine = orchestrate.NewEngine(manager, store)
	}
	// Restaurar el estado de plegado persistido (0006 R38).
	if persisted := store.LoadCollapsed(); len(persisted) > 0 {
		for k, v := range persisted {
			m.collapsed[k] = v
		}
	}
	m.tree = m.buildTree()
	m.updateLayout()
	// Wrap de líneas largas en la consola (spec 0005 R33): el viewport
	// corta ANSI-aware y conserva el estilo en las líneas de continuación.
	m.consoleView.SoftWrap = true
	return m
}

// updateLayout recalcula las dimensiones del dashboard (R18): cuerpo,
// panel derecho y viewport de consola.
func (m *Model) updateLayout() {
	bodyH := m.height - 5 // header + separator + 2 help lines + blank
	if bodyH < 3 {
		bodyH = 3
	}
	m.bodyH = bodyH
	rightW := m.width - treeWidth - 1
	if rightW < 10 {
		rightW = 10
	}
	m.rightW = rightW
	// El panel de detalles es fijo (0004 R30): solo se oculta si no cabe.
	m.detailsShown = rightW >= detailsWidthMin && bodyH >= detailsHeight+4
	contentH := bodyH - 1 // barra de pestañas
	if m.detailsShown {
		contentH -= detailsHeight + 1 // panel de detalles + separador
	}
	if contentH < 3 {
		contentH = 3
	}
	m.contentH = contentH
	m.consoleView.SetWidth(rightW)
	m.consoleView.SetHeight(contentH)
}

// ---- Mensajes ----

type tickMsg time.Time

type consoleTickMsg time.Time

type refreshResult struct {
	status process.Status
	meta   state.Meta
	warn   string
	branch string
}

type refreshedMsg struct{ results map[string]refreshResult }

type startedMsg struct {
	path string
	res  process.StartResult
	err  error
}

type stoppedMsg struct {
	path string
	err  error
}

// consoleDeltaMsg transporta los bytes nuevos de ambos streams desde el
// último tick de consola (spec 0002 R19).
type consoleDeltaMsg struct {
	path   string
	stdout string
	stderr string
	offS   int64
	offE   int64
	errS   error
	errE   error
}

type threadsMsg struct {
	path    string
	threads []process.ThreadInfo
	err     error
}

type statusMsg struct{ message string }

// stackResultMsg es el resultado de la orquestación de un stack (0010).
type stackResultMsg struct {
	result orchestrate.LaunchResult
	err    error
}

// jobMsg es el resultado de un comando one-shot (build/install/task,
// spec 0003 R27): exitCode 0 = ok, err = fallo al lanzar.
type jobMsg struct {
	path     string
	kind     string
	command  string
	exitCode int
	elapsed  time.Duration
	err      error
}

// ---- Comandos ----

func tickCmd() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func consoleTickCmd() tea.Cmd {
	return tea.Tick(consoleTick, func(t time.Time) tea.Msg { return consoleTickMsg(t) })
}

// refreshCmd re-verifica liveness de todos los servicios configurados
// leyendo meta.json del disco (soporta re-adjunta, spec 0001 S7.2/R9) y
// actualiza la rama git cacheada de cada proyecto.
func refreshCmd(store *state.Store, manager process.Manager, projects []scanner.Project) tea.Cmd {
	return func() tea.Msg {
		results := make(map[string]refreshResult, len(projects))
		for _, p := range projects {
			if !p.Configured {
				continue
			}
			r := refreshResult{branch: gitinfo.Branch(p.Path)}
			meta, err := store.LoadMeta(p.Path)
			switch {
			case errors.Is(err, os.ErrNotExist):
				r.status = process.StatusStopped
			case err != nil:
				// S5.1: meta corrupto → stopped + warning, sin crashear.
				r.status = process.StatusStopped
				r.warn = fmt.Sprintf("%s: meta.json ilegible, marcado stopped (%v)", p.Name, err)
			default:
				r.meta = meta
				r.status = manager.Evaluate(process.EvalSpec{
					Pid:            meta.Pid,
					CreationTimeMs: meta.CreationTimeMs,
					Port:           meta.Port,
					ProcessPattern: meta.ProcessPattern,
				})
			}
			results[p.Path] = r
		}
		return refreshedMsg{results: results}
	}
}

func startCmd(store *state.Store, manager process.Manager, p scanner.Project) tea.Cmd {
	return func() tea.Msg {
		if _, err := store.EnsureServiceDir(p.Path); err != nil {
			return startedMsg{path: p.Path, err: err}
		}
		res, err := manager.Start(process.StartSpec{
			Command:    p.Manifest.Command,
			WorkDir:    p.Path,
			StdoutPath: store.StdoutLog(p.Path),
			StderrPath: store.StderrLog(p.Path),
		})
		if err != nil {
			return startedMsg{path: p.Path, err: err}
		}
		meta := state.Meta{
			Name:           p.Manifest.Name,
			ProjectPath:    p.Path,
			Port:           p.Manifest.Port,
			ProcessPattern: p.Manifest.ProcessPattern,
			Command:        p.Manifest.Command,
			Pid:            res.Pid,
			Pgid:           res.Pgid,
			CreationTimeMs: res.CreationTimeMs,
			StartedAt:      time.Now().Format(time.RFC3339),
			State:          state.StateRunning,
		}
		if err := store.SaveMeta(p.Path, meta); err != nil {
			return startedMsg{path: p.Path, err: err}
		}
		if err := store.RegisterPid(p.Path, res.Pid, res.Pgid); err != nil {
			return startedMsg{path: p.Path, err: err}
		}
		return startedMsg{path: p.Path, res: res}
	}
}

// stopCmd para un servicio: si el manifiesto define command_stop lo
// ejecuta primero (parada graciosa para servicios donde matar el PGID
// no basta, ej. `docker stop`), y después aplica siempre el shutdown
// de limpieza (S8.1/S8.2: SIGTERM al PGID, timeout 5s, SIGKILL).
func stopCmd(store *state.Store, manager process.Manager, path, stopCommand string) tea.Cmd {
	return func() tea.Msg {
		var cmdErr error
		if stopCommand != "" {
			if _, _, err := runLogged("stop", stopCommand, path, store.StdoutLog(path), store.StderrLog(path)); err != nil {
				cmdErr = err // se notifica, pero el stop de limpieza sigue
			}
		}
		meta, err := store.LoadMeta(path)
		if err == nil && (meta.Pgid > 0 || meta.Port > 0) {
			_ = manager.Stop(process.StopSpec{Pgid: meta.Pgid, Port: meta.Port, Timeout: process.DefaultStopTimeout})
		}
		if err := store.ClearPid(path); err != nil {
			return stoppedMsg{path: path, err: err}
		}
		if err == nil {
			meta.State = state.StateStopped
			meta.Pid = 0
			meta.Pgid = 0
			_ = store.SaveMeta(path, meta)
		}
		_ = appendLine(store.StderrLog(path), fmt.Sprintf("── vroom ▶ stop: service stopped ──"))
		return stoppedMsg{path: path, err: cmdErr}
	}
}

// consoleTailCmd lee los bytes nuevos de ambos streams desde los offsets
// actuales (spec R19). El strip de ANSI se hace aquí, una sola vez.
func consoleTailCmd(path string, offS, offE int64, stdoutPath, stderrPath string) tea.Cmd {
	return func() tea.Msg {
		msg := consoleDeltaMsg{path: path, offS: offS, offE: offE}
		msg.stdout, msg.offS, msg.errS = readNewStripped(stdoutPath, offS)
		msg.stderr, msg.offE, msg.errE = readNewStripped(stderrPath, offE)
		return msg
	}
}

func readNewStripped(path string, offset int64) (string, int64, error) {
	data, off, err := tail.ReadNew(path, offset)
	if err != nil {
		return "", offset, err
	}
	return tail.StripANSI(data), off, nil
}

// threadsCmd muestrea los hilos del PID (spec R20).
func threadsCmd(path string, pid int) tea.Cmd {
	return func() tea.Msg {
		threads, err := process.ListThreads(pid)
		return threadsMsg{path: path, threads: threads, err: err}
	}
}

// appendLine añade line al fichero en append (creándolo si falta).
func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}

// jobBanner compone el separador que marca el inicio de un job
// one-shot dentro del log (0003 R27).
func jobBanner(kind, text string) string {
	return fmt.Sprintf("── vroom ▶ %s: %s ──", kind, text)
}

// runLogged ejecuta un comando one-shot con `sh -c` en workDir (0003
// R27): escribe un banner, lanza el comando con salida en append a los
// logs del servicio (visibles en la pestaña Console vía el tail
// existente) y añade un footer con el resultado. Devuelve la duración,
// el exit code (0 si ok o fallo de lanzamiento) y el error de ejecución.
func runLogged(kind, command, workDir, stdoutPath, stderrPath string) (time.Duration, int, error) {
	if dir := filepath.Dir(stdoutPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, 0, err
		}
	}
	if err := appendLine(stdoutPath, jobBanner(kind, command)); err != nil {
		return 0, 0, err
	}
	start := time.Now()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workDir
	out, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, 0, err
	}
	defer out.Close()
	errF, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, 0, err
	}
	defer errF.Close()
	cmd.Stdout = out
	cmd.Stderr = errF
	runErr := cmd.Run()
	elapsed := time.Since(start).Round(10 * time.Millisecond)
	if runErr != nil {
		exitCode := 0
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		_ = appendLine(stdoutPath, fmt.Sprintf("── vroom ✗ %s failed (exit %d, %s) ──", kind, exitCode, elapsed))
		return elapsed, exitCode, runErr
	}
	_ = appendLine(stdoutPath, fmt.Sprintf("── vroom ✓ %s ok (%s) ──", kind, elapsed))
	return elapsed, 0, nil
}

// jobCmd ejecuta un comando one-shot (build/install/task, spec 0003
// R27) sobre runLogged y devuelve jobMsg con el exit code.
func jobCmd(path, kind, command, workDir, stdoutPath, stderrPath string) tea.Cmd {
	return func() tea.Msg {
		elapsed, exitCode, err := runLogged(kind, command, workDir, stdoutPath, stderrPath)
		msg := jobMsg{path: path, kind: kind, command: command, exitCode: exitCode, elapsed: elapsed}
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				msg.err = err // fallo de lanzamiento (I/O), no del comando
			}
		}
		return msg
	}
}

// editLogsCmd suspende la TUI y abre ambos ficheros de log en el editor
// del usuario (spec 0001 R17): $VISUAL/$EDITOR, default nvim. Para
// vim/nvim añade -O (split vertical) con el foco en el stream activo.
func editLogsCmd(editor, stdoutPath, stderrPath string, stderrFirst bool) tea.Cmd {
	cmd := buildEditorCmd(editor, stdoutPath, stderrPath, stderrFirst)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return statusMsg{message: "editor exited with error: " + err.Error()}
		}
		return statusMsg{message: "editor closed"}
	})
}

// buildEditorCmd compone el comando del editor: `-O` (split vertical)
// solo para editores tipo vim; el resto recibe los ficheros a secas.
func buildEditorCmd(editor, stdoutPath, stderrPath string, stderrFirst bool) *exec.Cmd {
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		parts = []string{"nvim"}
	}
	args := parts[1:]
	switch filepath.Base(parts[0]) {
	case "nvim", "vim", "vi":
		args = append(args, "-O")
	}
	first, second := stdoutPath, stderrPath
	if stderrFirst {
		first, second = stderrPath, stdoutPath
	}
	return exec.Command(parts[0], append(args, first, second)...)
}

// resolveEditor devuelve el editor configurado: $VISUAL, $EDITOR o nvim.
func resolveEditor() string {
	if v := os.Getenv("VISUAL"); v != "" {
		return v
	}
	if v := os.Getenv("EDITOR"); v != "" {
		return v
	}
	return "nvim"
}

// ---- Init / Update / View ----

func (m Model) View() tea.View {
	content := m.renderDashboard()
	if m.askPromptOpen { // modal del prompt de ask AI (0004 R32)
		content = overlay(content, m.askBox(), m.width, m.height)
	} else if m.pickerOpen { // modal de selección centrado (0003 R28)
		content = overlay(content, m.pickerBox(), m.width, m.height)
	} else if m.termOpen { // modal de terminal embebida (0009 R52)
		content = overlay(content, m.termBox(), m.width, m.height)
	}
	v := tea.NewView(content)
	v.AltScreen = true                    // dashboard a pantalla completa (0002 R18)
	v.MouseMode = tea.MouseModeCellMotion // rueda del mouse: scroll de consola
	return v
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		refreshCmd(m.store, m.manager, m.projects),
		tickCmd(),
		consoleTickCmd(),
		func() tea.Msg { return m.spinner.Tick() },
		func() tea.Msg { return m.startSpinner.Tick() },
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.updateLayout()
		// S18.5: reajustar la ventana del árbol al nuevo alto.
		if len(m.entries) > 0 {
			_, cl := m.treeLines()
			if cl < m.treeTop {
				m.treeTop = cl
			} else if cl >= m.treeTop+m.treeVis() {
				m.treeTop = cl - m.treeVis() + 1
			}
		}
		// 0009 R55: la terminal embebida sigue las nuevas dimensiones.
		if s := m.term; s != nil && s.alive() {
			w, h := m.termW(), m.termH()
			if cw, ch := s.dims(); cw != w || ch != h {
				s.resize(w, h)
			}
		}
		return m, m.syncConsoleView()

	case spinner.TickMsg:
		var cmd1, cmd2 tea.Cmd
		m.spinner, cmd1 = m.spinner.Update(msg)
		m.startSpinner, cmd2 = m.startSpinner.Update(msg)
		return m, tea.Batch(cmd1, cmd2)

	case tickMsg:
		if !m.messageExpiresAt.IsZero() && time.Now().After(m.messageExpiresAt) {
			m.clearMessage() // el polling no borra mensajes antes de expirar
		}
		cmds := []tea.Cmd{refreshCmd(m.store, m.manager, m.projects), tickCmd()}
		if p := m.selected(); p != nil && p.Configured && m.isRunning(p.Path) {
			cmds = append(cmds, threadsCmd(p.Path, m.services[p.Path].Meta.Pid))
		}
		return m, tea.Batch(cmds...)

	case consoleTickMsg:
		cmds := []tea.Cmd{consoleTickCmd()}
		if p := m.selected(); p != nil && p.Configured && m.activeTab == tabConsole {
			cmds = append(cmds, m.tailCmd())
		}
		return m, tea.Batch(cmds...)

	case refreshedMsg:
		for path, r := range msg.results {
			sv, ok := m.services[path]
			if !ok {
				continue
			}
			if r.branch != "" {
				m.branches[path] = r.branch
			}
			if sv.Status == statusStarting || sv.Status == statusStopping {
				continue // transitorios: los resuelven startedMsg/stoppedMsg
			}
			sv.Status = mapUIStatus(r.status)
			sv.Meta = r.meta
		}
		for _, r := range msg.results {
			if r.warn != "" {
				m.notify(r.warn) // S5.1: warning visible
				break
			}
		}
		return m, nil

	case startedMsg:
		sv := m.services[msg.path]
		if msg.err != nil {
			if sv != nil {
				sv.Status = statusStopped
			}
			m.notify("error starting: " + msg.err.Error())
			return m, nil
		}
		if sv != nil {
			sv.Status = statusRunning
		}
		// Los logs se truncan en daemon_unix.go Start(); limpiar los
		// buffers en memoria para que la vista no muestre contenido viejo.
		cs := m.consoleStateFor(msg.path)
		cs.stdout, cs.stderr, cs.merged = "", "", ""
		cs.off[0] = fileSizeOrZero(m.store.StdoutLog(msg.path))
		cs.off[1] = fileSizeOrZero(m.store.StderrLog(msg.path))
		if p := m.selected(); p != nil && p.Path == msg.path {
			m.setConsoleContent(cs.view(m.stream))
		}
		return m, nil

	case stoppedMsg:
		sv := m.services[msg.path]
		if msg.err != nil {
			if sv != nil {
				sv.Status = statusStopped
			}
			m.notify("error stopping: " + msg.err.Error())
			return m, nil
		}
		if m.pendingRestart[msg.path] { // S14.2: stop → start
			delete(m.pendingRestart, msg.path)
			if p := m.projectByPath(msg.path); p != nil && sv != nil {
				sv.Status = statusStarting
				return m, startCmd(m.store, m.manager, *p)
			}
		}
		if sv != nil {
			sv.Status = statusStopped
		}
		return m, nil

	case consoleDeltaMsg:
		m.applyConsoleDelta(msg)
		return m, nil

	case threadsMsg:
		m.applyThreads(msg)
		return m, nil

	case statusMsg:
		m.notify(msg.message)
		return m, nil

	case jobMsg:
		delete(m.jobs, msg.path) // R27: libera el bloqueo del proyecto
		switch {
		case msg.err != nil:
			m.notify(msg.kind + " error: " + msg.err.Error())
		case msg.exitCode != 0:
			m.notify(fmt.Sprintf("%s failed (exit %d, %s)", msg.kind, msg.exitCode, msg.elapsed))
		default:
			m.notify(fmt.Sprintf("%s ok (%s)", msg.kind, msg.elapsed))
		}
		return m, nil

	case ptyDataMsg: // 0009 R54: bytes del shell → emulador; re-arma el loop
		if s := m.term; s != nil {
			s.write(msg.data)
			return m, readPtyCmd(s)
		}
		return m, nil

	case ptyEOFMsg: // 0009 R56: el reaper (armado al abrir) hace el cleanup
		return m, nil

	case ptyExitMsg: // sesión reaped: cleanup + aviso (0009 R56)
		if s := m.term; s != nil {
			s.shutdown() // idempotente; desbloquea el read loop si quedó
			m.termOpen = false
			if code := exitCode(msg.err); code != 0 {
				m.notify(fmt.Sprintf("terminal exited (%d)", code))
			} else {
				m.notify("terminal closed")
			}
			m.term = nil
		}
		return m, nil

	case stackResultMsg: // 0010: resultado de orquestación de stack
		if msg.err != nil {
			m.notify("stack error: " + msg.err.Error())
		} else if !msg.result.OK {
			m.notify("stack failed: " + msg.result.Error)
		} else {
			m.notify(fmt.Sprintf("stack %s launched", msg.result.Stack))
		}
		return m, nil

	case composersResultMsg: // 0010: resultado de lanzar todos los stacks de un group
		failed := 0
		for _, r := range msg.results {
			if !r.OK {
				failed++
			}
		}
		if failed > 0 {
			m.notify(fmt.Sprintf("%d stack(s) failed in %s", failed, msg.primary))
		} else {
			m.notify(fmt.Sprintf("all stacks launched in %s", msg.primary))
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

// handleMouse procesa la rueda del mouse: sobre el panel de detalles
// (si stack/group seleccionado) scrollea detalles; sobre la consola
// scrollea el console viewport.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Si details visible y cursor sobre stack/group, scrollear details
	if m.detailsShown && m.selectedItemKind() != itemProject {
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			m.scrollDetails(-wheelLines)
			return m, nil
		case tea.MouseWheelDown:
			m.scrollDetails(wheelLines)
			return m, nil
		}
	}
	if m.activeTab != tabConsole {
		return m, nil
	}
	switch msg.Mouse().Button {
	case tea.MouseWheelUp:
		m.consoleFollow = false
		m.consoleView.ScrollUp(wheelLines)
	case tea.MouseWheelDown:
		m.consoleView.ScrollDown(wheelLines)
		if m.consoleView.AtBottom() {
			m.consoleFollow = true
		}
	}
	return m, nil
}

func mapUIStatus(s process.Status) uiStatus {
	switch s {
	case process.StatusRunning:
		return statusRunning
	case process.StatusUnknown:
		return statusUnknown
	default:
		return statusStopped
	}
}

func (m Model) handleKey(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg := msg.(tea.KeyMsg)
	key := keyMsg.String()
	if m.termOpen { // modal de terminal captura las teclas (0009 R54)
		return m.termKey(keyMsg)
	}
	if m.askPromptOpen { // modal del prompt captura las teclas (0004 R32)
		return m.askKey(keyMsg)
	}
	if m.pickerOpen { // modal: el picker captura las teclas (0003 R28)
		return m.pickerKey(key)
	}
	if m.filterOpen { // barra de filtro captura las teclas (0007 R40)
		return m.filterKey(keyMsg)
	}
	// Teclas universales (0008 R47): navegación, especiales y las
	// permanentes "/" (filter) y "!" (terminal, 0009 R52). No
	// remapeables.
	switch key {
	case "q", "ctrl+c":
		return m, m.quitCmd()
	case "esc": // 0004 R30: sin panel que cerrar; esc sale
		if m.filterText != "" { // 0007 S42.3: con filtro, esc limpia (no sale)
			return m.applyFilter("")
		}
		return m, m.quitCmd()
	case "!": // 0009 R52: abre/muestra la terminal embebida
		return m.openTerm()
	case "/": // 0007 R40: abre el filtro del árbol
		return m.openFilter()
	case "enter": // R24: colapsar/expandir el grupo seleccionado
		return m.enterSelection()
	case "j", "k", "up", "down": // S12.1 + S18.5
		return m.navigate(key)
	case "tab":
		if m.activeTab == tabConsole {
			return m.switchTab(tabThreads)
		}
		return m.switchTab(tabConsole)
	case "1":
		return m.switchTab(tabConsole)
	case "2":
		return m.switchTab(tabThreads)
	case "pgup": // S19.4: scroll pausa el follow; si stack/group seleccionado, scroll details
		if m.detailsShown && m.selectedItemKind() != itemProject {
			m.scrollDetails(-detailsHeight)
			return m, nil
		}
		m.consoleFollow = false
		m.consoleView.PageUp()
		return m, nil
	case "pgdown":
		if m.detailsShown && m.selectedItemKind() != itemProject {
			m.scrollDetails(detailsHeight)
			return m, nil
		}
		m.consoleView.PageDown()
		return m, nil
	}
	// Acciones configurables (0008 R46/R50): tecla → acción vía el mapa
	// inverso precalculado. Con defaults coincide con el comportamiento
	// histórico.
	switch m.keyActions[key] {
	case "start_stop":
		return m.toggleSelected()
	case "restart":
		return m.restartSelected()
	case "build": // 0003 R26: build one-shot
		if it, ok := m.selectedItem(); ok && it.kind == itemStack {
			m.notify("not available for stacks")
			return m, nil
		}
		return m.runBuild()
	case "install": // 0003 R26: install one-shot
		if it, ok := m.selectedItem(); ok && it.kind == itemStack {
			m.notify("not available for stacks")
			return m, nil
		}
		return m.runInstall()
	case "tasks": // 0003 R28: picker de tasks de mise
		return m.openPicker()
	case "ask": // 0004 R32: ask AI (dispatch configurable)
		return m.openAsk()
	case "clear": // 0004 R31: limpiar la consola en memoria
		return m.clearConsole()
	case "stream": // S19.3: cicla merged → stdout → stderr
		m.stream = (m.stream + 1) % 3
		return m, m.syncConsoleView()
	case "top": // S19.4: goto top pausa el follow
		m.consoleFollow = false
		m.consoleView.GotoTop()
		return m, nil
	case "bottom": // reactiva el follow (S19.4)
		m.consoleFollow = true
		m.consoleView.GotoBottom()
		return m, nil
	case "logs": // S22.1: abre los logs en el editor
		if it, ok := m.selectedItem(); ok && it.kind == itemStack {
			m.notify("not available for stacks")
			return m, nil
		}
		return m.openLogEditor()
	case "refresh": // S22.2: refresh forzado sin cambiar de vista
		return m, m.refreshBatch()
	}
	if key == "o" { // alias fijo de logs, salvo que el config lo reclame
		return m.openLogEditor()
	}
	return m, nil
}

// navigate mueve el cursor cíclicamente por el árbol (grupos y
// proyectos) y ajusta la ventana visible para mantener el cursor en
// pantalla (S12.1 + S18.5).
func (m Model) navigate(key string) (tea.Model, tea.Cmd) {
	if len(m.tree) == 0 {
		return m, nil
	}
	switch key {
	case "j", "down":
		m.cursor = (m.cursor + 1) % len(m.tree)
	case "k", "up":
		m.cursor = (m.cursor - 1 + len(m.tree)) % len(m.tree)
	}
	m.detailsTop = 0 // reset scroll al cambiar de item
	_, cursorLine := m.treeLines()
	visH := m.treeVis() // 0007 R44: la barra consume una línea del árbol
	if cursorLine < m.treeTop {
		m.treeTop = cursorLine
	} else if cursorLine >= m.treeTop+visH {
		m.treeTop = cursorLine - visH + 1
	}
	return m.onSelect()
}

// enterSelection es la acción de `enter` (R24 + 0006 R38): sobre un
// header alterna su propio nivel (primario o secundario); sobre un
// proyecto pliega el contenedor más interno al que pertenece (su
// secundario si tiene, si no su primario). El header conserva su índice
// al reconstruir el árbol.
func (m Model) enterSelection() (tea.Model, tea.Cmd) {
	it, ok := m.selectedItem()
	if !ok {
		return m, nil
	}
	switch it.kind {
	case itemPrimary:
		m.collapsed[it.primary] = !m.collapsed[it.primary]
	case itemSecondary:
		key := m.secondaryKey(it.primary, it.secondary)
		m.collapsed[key] = !m.collapsed[key]
	case itemStack:
		return m, nil // stacks no se pliegan (0010 R56 S56.3)
	case itemRepo:
		m.toggleRepoCollapse(it.repoPath) // contenedor sintetizado (0011)
	case itemProject:
		if it.hasKids { // fila de repo: pliega/expande sus worktrees (0011)
			m.toggleRepoCollapse(it.repoPath)
			break
		}
		if it.secondary != "" {
			key := m.secondaryKey(it.primary, it.secondary)
			m.collapsed[key] = !m.collapsed[key]
		} else if it.primary != "" {
			m.collapsed[it.primary] = !m.collapsed[it.primary]
		} else {
			return m, nil // proyecto inline sin contenedor
		}
	}
	m.tree = m.buildTree()
	// Persistir el estado de plegado (best-effort: no bloquear la UI).
	_ = m.store.SaveCollapsed(m.collapsed)
	return m, nil
}

// onSelect refresca la consola (y threads) al cambiar la selección
// (S19.2); sobre un grupo o stack muestra un placeholder.
func (m Model) onSelect() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		if m.onHeader() || m.selectedStack() != nil {
			m.setConsoleContent(groupConsoleHint)
		}
		return m, nil
	}
	if !p.Configured {
		return m, nil
	}
	cs := m.consoleStateFor(p.Path)
	m.setConsoleContent(cs.view(m.stream))
	var cmds []tea.Cmd
	if m.activeTab == tabConsole {
		cmds = append(cmds, m.tailCmd())
	}
	if m.activeTab == tabThreads && m.isRunning(p.Path) {
		cmds = append(cmds, threadsCmd(p.Path, m.services[p.Path].Meta.Pid))
	}
	return m, tea.Batch(cmds...)
}

// switchTab cambia la pestaña activa y rellena el contenido.
func (m Model) switchTab(tab tabKind) (tea.Model, tea.Cmd) {
	m.activeTab = tab
	if tab != tabConsole {
		return m, m.refreshThreads()
	}
	if p := m.selected(); p != nil && p.Configured {
		cs := m.consoleStateFor(p.Path)
		m.setConsoleContent(cs.view(m.stream))
		return m, tea.Batch(m.tailCmd())
	}
	return m, nil
}

// refreshBatch re-verifica liveness + tail + muestreo de threads (S22.2).
func (m Model) refreshBatch() tea.Cmd {
	cmds := []tea.Cmd{refreshCmd(m.store, m.manager, m.projects), m.refreshThreads()}
	if p := m.selected(); p != nil && p.Configured {
		cmds = append(cmds, m.tailCmd())
	}
	return tea.Batch(cmds...)
}

// refreshThreads lanza el muestreo si procede (servicio running).
func (m Model) refreshThreads() tea.Cmd {
	p := m.selected()
	if p == nil || !p.Configured || !m.isRunning(p.Path) {
		return nil
	}
	return threadsCmd(p.Path, m.services[p.Path].Meta.Pid)
}

func (m Model) isRunning(path string) bool {
	sv, ok := m.services[path]
	return ok && sv.Meta.Pid > 0 && (sv.Status == statusRunning || sv.Status == statusUnknown)
}

// tailCmd construye el comando de tail con los offsets actuales del
// servicio seleccionado.
func (m Model) tailCmd() tea.Cmd {
	p := m.selected()
	if p == nil {
		return nil
	}
	cs := m.consoleStateFor(p.Path)
	return consoleTailCmd(p.Path, cs.off[0], cs.off[1], m.store.StdoutLog(p.Path), m.store.StderrLog(p.Path))
}

// applyConsoleDelta integra los bytes nuevos en los buffers del servicio
// (S19.1/S23.1) y actualiza el viewport si es visible.
func (m *Model) applyConsoleDelta(msg consoleDeltaMsg) {
	cs := m.consoleStateFor(msg.path)
	cs.off[0], cs.off[1] = msg.offS, msg.offE
	if msg.errS == nil {
		cs.stdout = tail.CapBuffer(cs.stdout+msg.stdout, maxConsoleBytes)
	}
	if msg.errE == nil {
		cs.stderr = tail.CapBuffer(cs.stderr+msg.stderr, maxConsoleBytes)
	}
	cs.merged = tail.CapBuffer(cs.merged+msg.stdout+msg.stderr, maxConsoleBytes)

	if m.activeTab != tabConsole || m.selected() == nil || m.selected().Path != msg.path {
		return
	}
	m.setConsoleContent(cs.view(m.stream))
}

// applyThreads computa la tabla de hilos con CPU% por delta entre
// muestras (S20.1/S20.2).
func (m *Model) applyThreads(msg threadsMsg) {
	if msg.err != nil { // S20.5: proceso muerto entre ticks
		m.threads[msg.path] = nil
		return
	}
	now := time.Now()
	rows := make([]threadRow, len(msg.threads))
	prev := m.threadPrev[msg.path]
	for i, t := range msg.threads {
		row := threadRow{Name: t.Name, TID: t.TID, State: t.State}
		if prev != nil {
			if delta := t.Ticks - prev.ticks[t.TID]; delta > 0 {
				row.CPU = process.CPUPercent(delta, now.Sub(prev.at).Seconds())
			}
		}
		rows[i] = row
	}
	sortRows(rows)
	m.threads[msg.path] = rows
	m.threadPrev[msg.path] = &threadSample{at: now, ticks: ticksOf(msg.threads)}
}

func ticksOf(threads []process.ThreadInfo) map[int]uint64 {
	ticks := make(map[int]uint64, len(threads))
	for _, t := range threads {
		ticks[t.TID] = t.Ticks
	}
	return ticks
}

// consoleStateFor devuelve (creando si falta) el estado de consola del
// servicio: los buffers persisten mientras corre la TUI (S19.2).
func (m *Model) consoleStateFor(path string) *consoleState {
	cs, ok := m.consoleStates[path]
	if !ok {
		cs = &consoleState{}
		m.consoleStates[path] = cs
	}
	return cs
}

// setConsoleContent vuelca el buffer al viewport respetando el modo de
// follow: si está pausado se conserva el offset de scroll (S19.4). El
// contenido pasa por el saneo de CR y el resaltado de niveles antes de
// renderizarse (S19.8).
func (m *Model) setConsoleContent(content string) {
	if content == "" {
		content = "No logs available" // S16.2 heredado
	}
	y := m.consoleView.YOffset()
	m.consoleView.SetContent(highlightConsole(sanitizeConsole(content)))
	if m.consoleFollow {
		m.consoleView.GotoBottom()
	} else {
		m.consoleView.SetYOffset(y)
	}
}

// syncConsoleView rellena el viewport con el buffer actual (cambio de
// tamaño, de modo de stream o de visibilidad del panel de detalles).
func (m Model) syncConsoleView() tea.Cmd {
	if m.activeTab != tabConsole {
		return nil
	}
	p := m.selected()
	if p == nil {
		if m.onHeader() {
			m.consoleView.SetContent(groupConsoleHint)
		} else {
			m.consoleView.SetContent("")
		}
		return nil
	}
	if !p.Configured {
		m.consoleView.SetContent("")
		return nil
	}
	m.setConsoleContent(m.consoleStateFor(p.Path).view(m.stream))
	return nil
}

// ---- Acciones de servicio (spec 0001 R14/R15) ----

// toggleSelected es la acción contextual de `s`: start si está parado,
// stop si está corriendo (o unknown). Nunca hace doble start: si el
// estado es running el toggle SIEMPRE para (S15.1).
func (m Model) toggleSelected() (tea.Model, tea.Cmd) {
	it, ok := m.selectedItem()
	if !ok {
		return m, nil
	}
	switch {
	case it.kind == itemPrimary:
		return m.toggleNode(it.primary, it.secondary)
	case it.kind == itemSecondary && it.secondary == composersGroup:
		return m.toggleComposers(it.primary)
	case it.kind == itemSecondary:
		return m.toggleNode(it.primary, it.secondary)
	case it.kind == itemStack && it.stack != nil:
		return m.toggleStack(it.stack)
	case it.kind == itemRepo:
		m.notify("repository container — expand it to operate its worktrees")
		return m, nil
	}
	p := m.selected()
	if p == nil {
		return m, nil
	}
	if !p.Configured { // S11.2
		m.notify("No manifest — create a .vroom.toml to enable")
		return m, nil
	}
	sv := m.services[p.Path]
	switch sv.Status {
	case statusRunning, statusUnknown:
		sv.Status = statusStopping
		m.clearMessage()
		return m, stopCmd(m.store, m.manager, p.Path, p.Manifest.Stop)
	case statusStarting, statusStopping:
		return m, nil // en tránsito: ignorar
	default: // stopped
		sv.Status = statusStarting // S14.1
		m.clearMessage()
		// Limpiar la consola en memoria inmediatamente al pulsar start
		// para que no se vean los logs viejos mientras arranca el proceso.
		cs := m.consoleStateFor(p.Path)
		cs.stdout, cs.stderr, cs.merged = "", "", ""
		cs.off[0] = fileSizeOrZero(m.store.StdoutLog(p.Path))
		cs.off[1] = fileSizeOrZero(m.store.StderrLog(p.Path))
		m.setConsoleContent("")
		return m, startCmd(m.store, m.manager, *p)
	}
}

// toggleNode aplica el toggle contextual a los miembros del nodo (R24;
// 0006 R39): de un primario a todos sus miembros (incluidos los de todos
// sus secundarios); de un secundario, solo a los suyos. Si hay parados
// arranca los parados; si no, para los running.
func (m Model) toggleNode(primary, secondary string) (tea.Model, tea.Cmd) {
	members := m.nodeMembers(primary, secondary)
	if len(members) == 0 {
		return m, nil
	}
	anyStopped := false
	for _, p := range members {
		if sv := m.services[p.Path]; sv != nil && sv.Status == statusStopped {
			anyStopped = true
			break
		}
	}
	var cmds []tea.Cmd
	sel := m.selected()
	for _, p := range members {
		sv := m.services[p.Path]
		if sv == nil {
			continue
		}
		switch {
		case anyStopped && sv.Status == statusStopped:
			sv.Status = statusStarting
			// Limpiar consola en memoria al arrancar cada servicio del grupo.
			cs := m.consoleStateFor(p.Path)
			cs.stdout, cs.stderr, cs.merged = "", "", ""
			cs.off[0] = fileSizeOrZero(m.store.StdoutLog(p.Path))
			cs.off[1] = fileSizeOrZero(m.store.StderrLog(p.Path))
			// Actualizar la vista si es el servicio seleccionado.
			if sel != nil && sel.Path == p.Path {
				m.setConsoleContent("")
			}
			cmds = append(cmds, startCmd(m.store, m.manager, p))
		case !anyStopped && (sv.Status == statusRunning || sv.Status == statusUnknown):
			sv.Status = statusStopping
			cmds = append(cmds, stopCmd(m.store, m.manager, p.Path, manifestStop(p)))
		}
	}
	return m, tea.Batch(cmds...)
}

// toggleComposers alterna el estado de todos los stacks de un primary
// group. Si hay stacks stopped, lanza todos; si todos están running, para.
func (m Model) toggleComposers(primary string) (tea.Model, tea.Cmd) {
	if m.engine == nil {
		m.notify("orchestration engine not available")
		return m, nil
	}
	stacks := m.stacksForPrimary(primary)
	if len(stacks) == 0 {
		m.notify("no stacks found for " + primary)
		return m, nil
	}
	// Check if all stacks are running
	allRunning := true
	for i := range stacks {
		r, n := m.stackStats(&stacks[i])
		if n == 0 || r < n {
			allRunning = false
			break
		}
	}
	if allRunning {
		// Stop all stacks
		for i := range stacks {
			_ = m.engine.StopStack(&stacks[i], m.projects, nil)
		}
		m.notify(fmt.Sprintf("stopping all stacks in %s", primary))
		return m, nil
	}
	// Launch all stacks async
	m.notify(fmt.Sprintf("launching stacks in %s...", primary))
	return m, func() tea.Msg {
		var results []orchestrate.LaunchResult
		for i := range stacks {
			result, err := m.engine.Launch(&stacks[i], m.projects)
			if err != nil {
				return stackResultMsg{err: err}
			}
			results = append(results, *result)
		}
		return composersResultMsg{primary: primary, results: results}
	}
}

// composersResultMsg es el resultado de lanzar todos los stacks de un
// primary group desde el header Composers.
type composersResultMsg struct {
	primary string
	results []orchestrate.LaunchResult
}

// toggleStack alterna el estado de un stack (0010 R56): lanza la
// orquestación si está stopped, para todos los servicios si está running.
func (m Model) toggleStack(s *orchestrate.Stack) (tea.Model, tea.Cmd) {
	if m.engine == nil {
		m.notify("orchestration engine not available")
		return m, nil
	}
	running, _ := m.stackStats(s)
	total := 0
	seen := make(map[string]bool)
	for _, stage := range s.Stages {
		for _, name := range stage.Services {
			if !seen[name] {
				seen[name] = true
				total++
			}
		}
	}
	if running == total && total > 0 {
		// Stack running: stop all services
		for _, stage := range s.Stages {
			for _, name := range stage.Services {
				for _, e := range m.entries {
					if e.Project.Configured && e.Project.Manifest != nil && e.Project.Manifest.Name == name {
						sv := m.services[e.Project.Path]
						if sv != nil && (sv.Status == statusRunning || sv.Status == statusUnknown) {
							sv.Status = statusStopping
						}
						_ = m.engine.StopStack(s, m.projects, nil)
						break
					}
				}
			}
		}
		m.notify(fmt.Sprintf("stopping stack %s", s.Name))
		return m, nil
	}
	// Stack stopped: launch orchestration
	if m.engine == nil {
		m.notify("no compose file loaded")
		return m, nil
	}
	m.notify(fmt.Sprintf("launching stack %s...", s.Name))
	return m, func() tea.Msg {
		result, err := m.engine.Launch(s, m.projects)
		if err != nil {
			return stackResultMsg{err: err}
		}
		return stackResultMsg{result: *result}
	}
}

// nodeMembers devuelve los proyectos del nodo en orden de aparición:
// de un primario, todos sus miembros (incluidos los de todos sus
// secundarios); de un secundario, solo los del par primario/secundario.
func (m Model) nodeMembers(primary, secondary string) []scanner.Project {
	var out []scanner.Project
	for _, e := range m.entries {
		if e.Primary != primary {
			continue
		}
		if secondary != "" && e.Secondary != secondary {
			continue
		}
		out = append(out, e.Project)
	}
	return out
}

// nodeStats cuenta miembros y servicios en ejecución del nodo (R24);
// el conteo del primario suma todos sus secundarios (S38.5).
// Los stacks no se cuentan (0010).
func (m Model) nodeStats(primary, secondary string) (running, total int) {
	for _, p := range m.nodeMembers(primary, secondary) {
		total++
		if sv := m.services[p.Path]; sv != nil && sv.Status == statusRunning {
			running++
		}
	}
	return running, total
}

func (m Model) restartSelected() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		return m, nil
	}
	if !p.Configured {
		m.notify("No manifest — create a .vroom.toml to enable")
		return m, nil
	}
	sv := m.services[p.Path]
	if sv == nil || sv.Status != statusRunning {
		m.notify("only a running service can be restarted")
		return m, nil
	}
	m.pendingRestart[p.Path] = true
	sv.Status = statusStopping
	return m, stopCmd(m.store, m.manager, p.Path, p.Manifest.Stop)
}

// manifestStop devuelve el command_stop del manifiesto ("" si no hay
// manifiesto; defenses para llamadas por grupo).
func manifestStop(p scanner.Project) string {
	if p.Manifest == nil {
		return ""
	}
	return p.Manifest.Stop
}

// openLogEditor abre ambos logs del servicio seleccionado en el editor
// (S22.1): el foco sigue el modo de stream actual.
func (m Model) openLogEditor() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		if m.onHeader() {
			m.notify("select a service to open its logs")
		}
		return m, nil
	}
	if !p.Configured {
		m.notify("no manifest — no service logs")
		return m, nil
	}
	return m, editLogsCmd(
		resolveEditor(),
		m.store.StdoutLog(p.Path),
		m.store.StderrLog(p.Path),
		m.stream == streamStderr,
	)
}

// ---- Jobs one-shot (spec 0003 R26/R27) ----

// runInstall lanza el comando install del manifiesto (tecla i).
func (m Model) runInstall() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		if m.onHeader() {
			m.notify("select a service to run install")
		}
		return m, nil
	}
	if !p.Configured {
		m.notify("No manifest — create a .vroom.toml to enable")
		return m, nil
	}
	if p.Manifest.Install == "" {
		m.notify(`no install command — set command_install = "..." in .vroom.toml`)
		return m, nil
	}
	return m.launchJob("install", p.Manifest.Install)
}

// runBuild lanza el comando build del manifiesto (tecla b).
func (m Model) runBuild() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		if m.onHeader() {
			m.notify("select a service to run build")
		}
		return m, nil
	}
	if !p.Configured {
		m.notify("No manifest — create a .vroom.toml to enable")
		return m, nil
	}
	if p.Manifest.Build == "" {
		m.notify(`no build command — set command_build = "..." in .vroom.toml`)
		return m, nil
	}
	return m.launchJob("build", p.Manifest.Build)
}

// launchJob valida el bloqueo de jobs concurrentes sobre el mismo
// proyecto (R27) y despacha jobCmd.
func (m Model) launchJob(kind, command string) (tea.Model, tea.Cmd) {
	p := m.selected()
	if m.jobs[p.Path] != "" {
		m.notify(m.jobs[p.Path] + " already running in " + p.Name)
		return m, nil
	}
	m.jobs[p.Path] = kind
	m.clearMessage()
	return m, jobCmd(p.Path, kind, command, p.Path, m.store.StdoutLog(p.Path), m.store.StderrLog(p.Path))
}

// ---- Picker genérico: tasks de mise y agentes de IA ----

// openPicker abre el modal de tasks del mise.toml del servicio
// seleccionado; sin mise.toml o sin tasks notifica y no abre nada.
func (m Model) openPicker() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		if m.onHeader() {
			m.notify("select a service to pick a task")
		}
		return m, nil
	}
	if !p.Configured {
		m.notify("No manifest — create a .vroom.toml to enable")
		return m, nil
	}
	if !mise.HasMiseToml(p.Path) {
		m.notify("no mise.toml in " + p.Name)
		return m, nil
	}
	tasks, err := mise.Tasks(p.Path)
	if err != nil {
		m.notify(err.Error())
		return m, nil
	}
	if len(tasks) == 0 {
		m.notify("no tasks defined in " + p.Name + "/mise.toml")
		return m, nil
	}
	items := make([]pickerItem, 0, len(tasks))
	for _, tk := range tasks {
		items = append(items, pickerItem{Name: tk.Name, Description: tk.Description})
	}
	m.pickerKind = pickerTasks
	m.pickerItems = items
	m.pickerCursor = 0
	m.pickerOpen = true
	return m, nil
}

// pickerKey maneja las teclas mientras el modal está abierto: j/k
// navegan, enter ejecuta la acción del item seleccionado, esc cierra.
func (m Model) pickerKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.pickerOpen = false
		return m, nil
	case "j", "down":
		if len(m.pickerItems) > 0 {
			m.pickerCursor = (m.pickerCursor + 1) % len(m.pickerItems)
		}
		return m, nil
	case "k", "up":
		if len(m.pickerItems) > 0 {
			m.pickerCursor = (m.pickerCursor - 1 + len(m.pickerItems)) % len(m.pickerItems)
		}
		return m, nil
	case "enter":
		if len(m.pickerItems) == 0 {
			return m, nil
		}
		it := m.pickerItems[m.pickerCursor]
		switch m.pickerKind {
		case pickerAgents: // 0004 R32: agente elegido → input del prompt
			m.pickerOpen = false
			return m.startAskPrompt(agents.Agent{Name: it.Name, Cmd: it.agentCmd})
		default: // pickerTasks: ejecutar `mise run <task>` como job
			m.pickerOpen = false
			return m.launchJob("task", "mise run "+it.Name)
		}
	}
	return m, nil // modal: el resto de teclas se ignoran
}

// ---- Filtro del árbol (spec 0007 R40) ----

// openFilter abre la barra de filtro: conserva el texto ya aplicado para
// que `/` sirva de "editar el filtro" (S40.4).
func (m Model) openFilter() (tea.Model, tea.Cmd) {
	m.filterOpen = true
	m.filterInput.SetValue(m.filterText)
	return m, m.filterInput.Focus()
}

// filterKey maneja las teclas del box abierto: el texto va al input y
// recalcula el árbol en vivo (S40.3); enter cierra aplicando el filtro
// actual; esc cierra limpiando (S42.2); ctrl+c sigue saliendo.
func (m Model) filterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.filterOpen = false
		m.filterInput.Reset()
		return m.applyFilter("")
	case "enter":
		m.filterOpen = false // S42.1: el filtro ya está aplicado en vivo
		return m, nil
	}
	ni, cmd := m.filterInput.Update(msg)
	m.filterInput = ni
	if v := ni.Value(); v != m.filterText { // S40.3: filtrado en vivo
		next, _ := m.applyFilter(v)
		m = next.(Model)
	}
	return m, cmd
}

// applyFilter recompone el árbol con los proyectos que matchean q
// (S41.*): re-ejecuta group.Arrange sobre los filtrados (los headers de
// grupo solo aparecen con miembros que matchean) y resetea cursor y
// treeTop (S43.2). q vacío restaura el árbol completo.
func (m Model) applyFilter(q string) (tea.Model, tea.Cmd) {
	m.filterText = q
	projects := m.projects
	if q != "" {
		projects = nil
		for _, p := range m.projects {
			if filterMatch(p, q) {
				projects = append(projects, p)
			}
		}
	}
	m.entries = group.Arrange(projects)
	m.tree = m.buildTree()
	m.cursor, m.treeTop = 0, 0
	return m, nil
}

// ---- Ask AI (spec 0004 R32) ----

// openAsk inicia el flujo de ask AI sobre el proyecto seleccionado:
// picker de agentes (solo instalados) o directo al prompt si hay uno.
func (m Model) openAsk() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		if m.onHeader() {
			m.notify("select a service to ask the AI")
		}
		return m, nil
	}
	m.askAgents = agents.Available(agents.Resolve(m.cfg.Ask.Agents))
	if len(m.askAgents) == 0 {
		m.notify("no AI agent found in PATH (opencode, pi, hermes, jcode)")
		return m, nil
	}
	if len(m.askAgents) == 1 {
		return m.startAskPrompt(m.askAgents[0])
	}
	items := make([]pickerItem, 0, len(m.askAgents))
	for _, ag := range m.askAgents {
		items = append(items, pickerItem{Name: ag.Name, Description: strings.Join(ag.Cmd, " "), agentCmd: ag.Cmd})
	}
	m.pickerKind = pickerAgents
	m.pickerItems = items
	m.pickerCursor = 0
	m.pickerOpen = true
	return m, nil
}

// startAskPrompt abre el modal de prompt para el agente elegido. El
// input (textarea multi-línea) se prellena con el template del config
// (spec 0005 R35): [ask] prompt con placeholders {name}/{dir}/{logs};
// vacío → sin prefill. El alto crece con el contenido hasta el cap de
// pantalla; más allá, el textarea hace scroll interno.
func (m Model) startAskPrompt(ag agents.Agent) (tea.Model, tea.Cmd) {
	m.askAgent = ag
	m.askPromptOpen = true
	m.promptInput.Reset()
	m.sizeAskPrompt()
	if tmpl := m.cfg.Ask.Prompt; tmpl != "" {
		if p := m.selected(); p != nil {
			m.promptInput.SetValue(expandAskPrompt(tmpl, p.Name, p.Path, m.store.ServiceDir(p.Path)))
			m.promptInput.MoveToEnd()
		}
	}
	return m, m.promptInput.Focus()
}

// sizeAskPrompt dimensiona el textarea del prompt al modal actual: el
// ancho interior (el prompt "› " lo reserva SetWidth internamente) y el
// cap de alto según la pantalla (MinHeight fija el tamaño inicial).
// Debe llamarse tras cualquier cambio de Prompt/ancho y antes del Focus.
func (m *Model) sizeAskPrompt() {
	m.promptInput.SetWidth(askInnerW(m.width))
	maxH := m.height - 8 // título + blank + hint + bordes + margen
	if maxH > askMaxHeightCap {
		maxH = askMaxHeightCap
	}
	if maxH < askMinHeight {
		maxH = askMinHeight
	}
	m.promptInput.MaxHeight = maxH
}

// expandAskPrompt sustituye los placeholders del template de ask: {name}
// (proyecto), {dir} (ruta del proyecto) y {logs} (directorio del servicio
// con stdout.log/stderr.log). Los placeholders desconocidos quedan tal
// cual para no sorprender.
func expandAskPrompt(tmpl, name, dir, logs string) string {
	r := strings.NewReplacer(
		"{name}", name,
		"{dir}", dir,
		"{logs}", logs,
	)
	return r.Replace(tmpl)
}

// askKey maneja las teclas del modal de prompt: el texto va al input,
// enter despacha, esc cancela.
func (m Model) askKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		if m.promptInput.Value() == "" {
			return m, tea.Quit
		}
	case "esc":
		m.askPromptOpen = false
		m.promptInput.Reset()
		return m, nil
	case "enter":
		return m.dispatchAsk()
	}
	ni, cmd := m.promptInput.Update(msg)
	m.promptInput = ni
	return m, cmd
}

// dispatchAsk lanza el agente con el prompt vía el launcher
// configurado (herdr/inline/custom, 0004 R32).
func (m Model) dispatchAsk() (tea.Model, tea.Cmd) {
	prompt := strings.TrimSpace(m.promptInput.Value())
	if prompt == "" {
		m.notify("empty prompt — type a request or esc to cancel")
		return m, nil
	}
	p := m.selected()
	if p == nil {
		m.notify("select a service to ask the AI")
		return m, nil
	}
	m.askPromptOpen = false
	m.promptInput.Reset()

	req := launcher.Request{
		Agent: m.askAgent.Name,
		Args:  m.askAgent.BuildArgs(prompt),
		Dir:   p.Path,
	}
	strategy, warn := m.askLauncher.Resolve()
	if warn != "" {
		m.notify(warn)
	}
	if strategy == launcher.StrategyInline {
		agent := m.askAgent.Name
		return m, tea.ExecProcess(m.askLauncher.InlineCmd(req), func(err error) tea.Msg {
			if err != nil {
				return statusMsg{message: agent + " exited with error: " + err.Error()}
			}
			return statusMsg{message: agent + " closed"}
		})
	}
	return m, launchAskCmd(m.askLauncher, strategy, req)
}

// launchAskCmd despacha en goroutine (herdr/custom no bloquean la UI).
func launchAskCmd(l *launcher.Launcher, strategy string, req launcher.Request) tea.Cmd {
	return func() tea.Msg {
		msg, err := l.Launch(strategy, req)
		if err != nil {
			return statusMsg{message: err.Error()}
		}
		return statusMsg{message: msg}
	}
}

// ---- Limpiar consola (spec 0004 R31) ----

// fileSizeOrZero devuelve el tamaño del fichero, o 0 si no existe.
func fileSizeOrZero(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}

// clearConsole vacía los buffers en memoria de la consola del servicio
// seleccionado y salta los offsets al EOF actual: la vista queda
// limpia y el tail no reintroduce lo borrado. Los ficheros de log NO
// se modifican (el histórico reaparece al reabrir la TUI).
func (m Model) clearConsole() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		if m.onHeader() {
			m.notify("select a service to clear its console")
		}
		return m, nil
	}
	if !p.Configured {
		m.notify("No manifest — create a .vroom.toml to enable")
		return m, nil
	}
	cs := m.consoleStateFor(p.Path)
	cs.stdout, cs.stderr, cs.merged = "", "", ""
	cs.off[0] = fileSizeOrZero(m.store.StdoutLog(p.Path))
	cs.off[1] = fileSizeOrZero(m.store.StderrLog(p.Path))
	m.setConsoleContent(cs.view(m.stream))
	m.notify("console cleared")
	return m, nil
}

// ---- Helpers de selección ----

// selectedItem devuelve la fila bajo el cursor (ok=false fuera de rango
// o árbol vacío).
func (m Model) selectedItem() (treeItem, bool) {
	if m.cursor < 0 || m.cursor >= len(m.tree) {
		return treeItem{}, false
	}
	return m.tree[m.cursor], true
}

// selectedItemKind devuelve el kind del item seleccionado.
func (m Model) selectedItemKind() treeItemKind {
	it, ok := m.selectedItem()
	if !ok {
		return -1
	}
	return it.kind
}

// scrollDetails mueve el scroll del panel de detalles delta líneas.
func (m *Model) scrollDetails(delta int) {
	m.detailsTop += delta
	if m.detailsTop < 0 {
		m.detailsTop = 0
	}
}

// selected devuelve el proyecto bajo el cursor (nil si hay un header).
func (m Model) selected() *scanner.Project {
	it, ok := m.selectedItem()
	if !ok || it.kind != itemProject {
		return nil
	}
	p := it.project
	return &p
}

// onHeader reporta si el cursor está sobre un header de grupo (primario
// o secundario).
func (m Model) onHeader() bool {
	it, ok := m.selectedItem()
	return ok && (it.kind == itemPrimary || it.kind == itemSecondary)
}

// selectedStack devuelve el stack seleccionado (nil si no es un itemStack).
func (m Model) selectedStack() *orchestrate.Stack {
	it, ok := m.selectedItem()
	if !ok || it.kind != itemStack || it.stack == nil {
		return nil
	}
	return it.stack
}

// selectedNode devuelve el (primary, secondary) del header bajo el
// cursor; secondary vacío = nodo primario. Ambos "" = sin header.
func (m Model) selectedNode() (primary, secondary string) {
	it, ok := m.selectedItem()
	if !ok || (it.kind != itemPrimary && it.kind != itemSecondary) {
		return "", ""
	}
	return it.primary, it.secondary
}

// secondaryKey es la clave de plegado de un secundario (0006 R38):
// compuesta `primario/secundario` para evitar colisión de nombres entre
// primarios distintos (S38.6).
func (m Model) secondaryKey(primary, secondary string) string {
	return primary + "/" + secondary
}

// toggleRepoCollapse alterna la expansión de una fila de repo (0011). El
// estado vive en el mismo mapa persistido pero con la clave `repo:<path>`
// y semántica invertida (default colapsado).
func (m Model) toggleRepoCollapse(repoPath string) {
	m.collapsed[repoKey(repoPath)] = !m.repoExpanded(repoPath)
}

func (m Model) projectByPath(path string) *scanner.Project {
	for i := range m.projects {
		if m.projects[i].Path == path {
			return &m.projects[i]
		}
	}
	return nil
}

func statusBadge(p scanner.Project, sv *ServiceState, spinnerView, startSpinnerView string) string {
	if p.Configured && p.ManifestErr == "" {
		port := ""
		if p.Manifest != nil && p.Manifest.Port > 0 {
			port = " " + styleDim.Render(fmt.Sprintf(":%d", p.Manifest.Port))
		}
		switch sv.Status {
		case statusRunning:
			return styleRunning.Render("● running") + port
		case statusStarting:
			return startSpinnerView + styleStarting.Render(" starting")
		case statusStopping:
			return styleStopping.Render("○ stopping")
		case statusUnknown:
			return spinnerView + styleUnknown.Render(" unknown") + port
		default:
			return styleStopped.Render("· stopped")
		}
	}
	if p.ManifestErr != "" {
		return styleWarn.Render("⚠ invalid .vroom.toml")
	}
	return styleUnconfigured.Render("· unconfigured")
}

// trunc recorta s a n runes conservando el inicio.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// truncTail recorta s a n runes conservando el final (para rutas largas).
func truncTail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[len(r)-1:])
	}
	return "…" + string(r[len(r)-n+1:])
}

// padW rellena s con espacios hasta un ancho visible w (consciente de
// ANSI: usa lipgloss.Width, que ignora secuencias de escape).
func padW(s string, w int) string {
	d := w - lipglossWidth(s)
	if d <= 0 {
		return s
	}
	return s + strings.Repeat(" ", d)
}

// kbKey devuelve la tecla activa de una acción desde kb, con fallback a
// los defaults (mapa nil o incompleto = defaults).
func kbKey(kb map[string]string, action string) string {
	if k := kb[action]; k != "" {
		return k
	}
	return config.DefaultKeybindings()[action]
}

// helpSeg compone el segmento "key label" de una acción configurable.
func helpSeg(kb map[string]string, action, label string) string {
	return kbKey(kb, action) + " " + label
}

// dashboardHelp1 devuelve la línea de navegación de la ayuda: atajos
// de teclado, interacción y pestañas. Caben siempre por ser cortos.
func dashboardHelp1(width int, kb map[string]string) string {
	line := strings.Join([]string{
		"/ filter",
		"shift+click select", "! shell", "q quit",
		helpSeg(kb, "start_stop", "start/stop"),
		helpSeg(kb, "restart", "restart"),
		helpSeg(kb, "build", "build"),
		helpSeg(kb, "install", "install"),
	}, " · ")
	if width <= 0 {
		width = 80
	}
	return trunc(line, width)
}

// dashboardHelp2 devuelve la línea de acciones de la ayuda (comandos
// del servicio). En pantallas estrechas omite las menos frecuentes
// (responsive).
func dashboardHelp2(width int, kb map[string]string) string {
	full := strings.Join([]string{
		"j/k move",
		"enter collapse",
		helpSeg(kb, "tasks", "tasks"),
		helpSeg(kb, "ask", "ask"),
		helpSeg(kb, "clear", "clear"),
		helpSeg(kb, "stream", "stream"),
		helpSeg(kb, "logs", "logfile"),
		helpSeg(kb, "refresh", "refresh"),
		"1/2 tabs",
	}, " · ")
	compact := strings.Join([]string{
		helpSeg(kb, "start_stop", "start/stop"),
		helpSeg(kb, "ask", "ask"),
		helpSeg(kb, "tasks", "tasks"),
		helpSeg(kb, "clear", "clear"),
		helpSeg(kb, "logs", "logfile"),
	}, " · ")
	if width <= 0 {
		width = 80
	}
	if utf8.RuneCountInString(full) > width {
		full = compact
	}
	return trunc(full, width)
}
