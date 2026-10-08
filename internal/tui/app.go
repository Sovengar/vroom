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
	"vroom/internal/manifest"
	"vroom/internal/mise"
	"vroom/internal/orchestrate"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/startsvc"
	"vroom/internal/state"
	"vroom/internal/tail"
)

const (
	pollInterval    = 2 * time.Second
	consoleTick     = 400 * time.Millisecond
	maxConsoleBytes = 192 * 1024
	treeWidth       = 30 // inner content width; the frame adds boxFrame columns on both sides
	detailsWidthMin = 40
	detailsHeight   = 12
	keybindsHeight  = 4
	boxFrame        = 2
	wheelLines      = 3
	askMinHeight    = 6
	askMaxHeightCap = 16
)

const groupConsoleHint = "group selected — pick a service to view its console"

type tabKind int

const (
	tabConsole tabKind = iota
	tabThreads
	tabMetrics
	tabGit
	tabEnv
	tabTimeline
	tabHealth
	tabCount
)

func tabTitle(k tabKind) string {
	switch k {
	case tabThreads:
		return "Threads"
	case tabMetrics:
		return "Metrics"
	case tabGit:
		return "Git"
	case tabEnv:
		return "Env"
	case tabTimeline:
		return "Timeline"
	case tabHealth:
		return "Health"
	default:
		return "Console"
	}
}

func tabLabelText(k tabKind) string {
	return fmt.Sprintf("%d %s", int(k)+1, tabTitle(k))
}

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

type uiStatus string

const (
	statusUnconfigured uiStatus = "sin configurar"
	statusStopped      uiStatus = "stopped"
	statusRunning      uiStatus = "running"
	statusUnknown      uiStatus = "unknown"
	statusStarting     uiStatus = "starting"
	statusStopping     uiStatus = "stopping"
	// Three names on purpose: pending is a bind in flight, no_port is by design, unresolved means discovery gave up undecided; all three stay stoppable (see docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md).
	statusPortPending    uiStatus = "starting, port pending"
	statusNoPort         uiStatus = "running, no port"
	statusPortUnresolved uiStatus = "running, port unresolved"
)

func (s uiStatus) alive() bool {
	switch s {
	case statusRunning, statusUnknown, statusPortPending, statusNoPort, statusPortUnresolved:
		return true
	default:
		return false
	}
}

type ServiceState struct {
	Status uiStatus
	Meta   state.Meta
}

type Model struct {
	width, height int
	root          string
	store         *state.Store
	manager       process.Manager

	projects []scanner.Project
	entries  []group.Entry
	services map[string]*ServiceState
	usedFD   bool

	cursor  int
	treeTop int

	activeTab     tabKind
	stream        streamMode
	consoleFollow bool

	tree          []treeItem
	collapsed     map[string]bool
	consoleStates map[string]*consoleState
	threads       map[string][]threadRow
	threadPrev    map[string]*threadSample
	branches      map[string]string

	metrics     map[string]*metricsView
	metricsPrev map[string]*metricsSample
	gitStatus   map[string]gitinfo.Status
	envVars     map[string][]string
	healthRes   map[string]*healthResult
	events      map[string][]timelineEvent

	message          string
	messageExpiresAt time.Time // zero value means the message never expires
	pendingRestart   map[string]bool

	jobs map[string]string

	pickerOpen   bool
	pickerKind   pickerKind
	pickerItems  []pickerItem
	pickerCursor int

	cfg           config.Config
	askLauncher   *launcher.Launcher
	askAgents     []agents.Agent
	askAgent      agents.Agent
	askPromptOpen bool
	promptInput   textarea.Model

	// Reverse key->action map precomputed from config so every keystroke resolves in O(1).
	keyActions map[string]string

	filterOpen  bool
	filterInput textinput.Model
	filterText  string

	// Closing the modal must not kill the shell: term owns the live PTY session, termOpen only visibility.
	termOpen bool
	term     *termSession

	composeFile *orchestrate.ComposeFile
	engine      *orchestrate.Engine

	bodyH        int
	bodyOuterH   int
	rightW       int
	contentH     int
	detailsShown bool
	detailsTop   int
	consoleView  viewport.Model

	spinner      spinner.Model
	startSpinner spinner.Model
}

func (m *Model) notify(s string) {
	m.message = s
	m.messageExpiresAt = time.Now().Add(5 * time.Second)
}

func (m *Model) clearMessage() {
	m.message, m.messageExpiresAt = "", time.Time{}
}

// findComposeFile walks up from the project itself and stops at root, an already-seen dir, or a parent equal to itself (filepath.Dir("/") == "/", so the walk would never end).
func findComposeFile(root string, projects []scanner.Project) (*orchestrate.ComposeFile, error) {
	seen := make(map[string]bool)
	for _, p := range projects {
		dir := p.Path
		for dir != "" {
			if seen[dir] {
				break
			}
			seen[dir] = true

			if cf, err := orchestrate.ParseComposeFile(dir); err == nil {
				return cf, nil
			}
			if dir == root {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return nil, fmt.Errorf("no %s found in any project directory under %s", orchestrate.ComposeFileName, root)
}

func New(store *state.Store, manager process.Manager, root string) Model {
	cfg := config.Load()

	scanRoot := root
	if cfg.Scanner.Root != "" {
		scanRoot = cfg.Scanner.Root
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
	ta.DynamicHeight = true
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
		metrics:        make(map[string]*metricsView),
		metricsPrev:    make(map[string]*metricsSample),
		gitStatus:      make(map[string]gitinfo.Status),
		envVars:        make(map[string][]string),
		healthRes:      make(map[string]*healthResult),
		events:         make(map[string][]timelineEvent),
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
		m.notify(cfg.Err.Error())
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
	// A failed worktree scan only warns: dropping the project would hide work the user can still see.
	for _, p := range projects {
		if p.WorktreeErr != "" {
			m.notify("worktree topology unavailable: " + p.WorktreeErr)
			break
		}
	}
	m.entries = group.Arrange(projects)
	if cf, err := findComposeFile(scanRoot, projects); err == nil {
		m.composeFile = cf
		m.engine = orchestrate.NewEngine(manager, store)
	}
	if persisted := store.LoadCollapsed(); len(persisted) > 0 {
		for k, v := range persisted {
			m.collapsed[k] = v
		}
	}
	m.tree = m.buildTree()
	m.updateLayout()
	// SoftWrap: the viewport truncates ANSI-aware, so a long line must wrap instead of losing its style.
	m.consoleView.SoftWrap = true
	return m
}

// Invariant: height = bodyOuterH + keybindsHeight and width = (treeWidth+boxFrame)+(rightW+boxFrame); details sit above the console in the right column.
func (m *Model) updateLayout() {
	bodyOuter := m.height - keybindsHeight
	if bodyOuter < 3 {
		bodyOuter = 3
	}
	m.bodyOuterH = bodyOuter
	// bodyH feeds strings.Repeat when rendering, so floor it to 1 here rather than in a branch that never runs.
	m.bodyH = max(bodyOuter-boxFrame, 1)

	// Clamp the right column so the box row never exceeds the terminal width; below ~34 cells the layout degrades.
	rightOuter := m.width - (treeWidth + boxFrame)
	if rightOuter < boxFrame {
		rightOuter = boxFrame
	}
	m.rightW = rightOuter - boxFrame

	detailsOuter := detailsHeight + boxFrame
	m.detailsShown = m.rightW >= detailsWidthMin && bodyOuter >= detailsOuter+5

	consoleOuter := bodyOuter
	if m.detailsShown {
		consoleOuter = bodyOuter - detailsOuter
	}
	m.contentH = max(consoleOuter-boxFrame-1, 0) // border + tab bar

	m.consoleView.SetWidth(m.rightW)
	m.consoleView.SetHeight(m.contentH)
}

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
	path  string
	res   process.StartResult
	meta  state.Meta
	warns []string
	err   error
}

type stoppedMsg struct {
	path string
	err  error
}

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

type stackResultMsg struct {
	result orchestrate.LaunchResult
	err    error
}

type jobMsg struct {
	path     string
	kind     string
	command  string
	exitCode int
	elapsed  time.Duration
	err      error
}

func tickCmd() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func consoleTickCmd() tea.Cmd {
	return tea.Tick(consoleTick, func(t time.Time) tea.Msg { return consoleTickMsg(t) })
}

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
				r.status = process.StatusStopped
				r.warn = fmt.Sprintf("%s: unreadable meta.json, marked stopped (%v)", p.Name, err)
			default:
				r.meta = meta
				r.status = manager.Evaluate(process.EvalSpec{
					Pid:            meta.Pid,
					CreationTimeMs: meta.CreationTimeMs,
					Port:           meta.Port,
					ProcessPattern: meta.ProcessPattern,
					PortPending:    meta.State == state.StatePortPending,
					PortUnresolved: meta.State == state.StatePortUnresolved,
					NoPort:         meta.State == state.StateNoPort,
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
		out, err := startsvc.Start(startsvc.Request{
			Manifest:   p.Manifest,
			Path:       p.Path,
			Store:      store,
			Manager:    manager,
			StdoutPath: store.StdoutLog(p.Path),
			StderrPath: store.StderrLog(p.Path),
			Routes:     portlessClient(p.Manifest),
			Branch:     gitinfo.Branch(p.Path),
		})
		if err != nil {
			return startedMsg{path: p.Path, err: err}
		}
		for _, w := range out.Warnings {
			_ = appendLine(store.StderrLog(p.Path), "── vroom ▶ start: "+w)
		}
		return startedMsg{
			path:  p.Path,
			res:   process.StartResult{Pid: out.Pid, Pgid: out.Meta.Pgid, CreationTimeMs: out.Meta.CreationTimeMs},
			meta:  out.Meta,
			warns: out.Warnings,
		}
	}
}

// command_stop runs first for services where killing the PGID is not enough (e.g. docker stop); the cleanup SIGTERM/SIGKILL always follows.
func stopCmd(store *state.Store, manager process.Manager, path, stopCommand string) tea.Cmd {
	return func() tea.Msg {
		var cmdErr error
		if stopCommand != "" {
			if _, _, err := runLogged("stop", stopCommand, path, store.StdoutLog(path), store.StderrLog(path)); err != nil {
				cmdErr = err // reported to the user, but the cleanup stop still runs
			}
		}
		meta, err := store.LoadMeta(path)
		// A PID without a PGID is still a credible root: Stop signals it and its lineage instead of skipping it.
		if err == nil && (meta.Pid > 0 || meta.Pgid > 0 || meta.Port > 0) {
			var warns []string
			_ = manager.Stop(process.StopSpec{
				Pid: meta.Pid, Pgid: meta.Pgid, Port: meta.Port,
				Timeout: process.DefaultStopTimeout,
				Warn:    func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) },
			})
			// The service is down, so its reservation returns to the pool; otherwise long-lived services run out of ports.
			process.ReleasePort(meta.ReservedPort)
			for _, w := range warns {
				_ = appendLine(store.StderrLog(path), "── vroom ▶ stop: "+w)
			}
		}
		// Deliberately outside the guard above: an already-dead service (Pid 0) also leaves a route behind, and a failure here is benign because SaveMeta below persists the revocation.
		releaseRoute(&meta)
		if err := store.ClearPid(path); err != nil {
			return stoppedMsg{path: path, err: err}
		}
		if err == nil {
			meta.State = state.StateStopped
			meta.Pid = 0
			meta.Pgid = 0
			meta.ReservedPort = 0
			_ = store.SaveMeta(path, meta)
		}
		_ = appendLine(store.StderrLog(path), "── vroom ▶ stop: service stopped ──")
		return stoppedMsg{path: path, err: cmdErr}
	}
}

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

func threadsCmd(path string, pid int) tea.Cmd {
	return func() tea.Msg {
		threads, err := process.ListThreads(pid)
		return threadsMsg{path: path, threads: threads, err: err}
	}
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(line + "\n")
	return err
}

func jobBanner(kind, text string) string {
	return fmt.Sprintf("── vroom ▶ %s: %s ──", kind, text)
}

func runLogged(kind, command, workDir, stdoutPath, stderrPath string) (time.Duration, int, error) {
	if dir := filepath.Dir(stdoutPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, 0, err
		}
	}
	// stdout is opened once and carries banner and footer: two opens made the second OpenFile error unreachable, hiding the unopenable-log failure.
	out, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = out.Close() }()
	// Banner before stderr opens on purpose: if stderr fails, the log still says which command never ran.
	if _, err := out.WriteString(jobBanner(kind, command) + "\n"); err != nil {
		return 0, 0, err
	}
	start := time.Now()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workDir
	errF, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = errF.Close() }()
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
		// Footer reuses the open descriptor and drops its error: the command already ran, so reporting a write failure would lie about whether the build ran.
		_, _ = fmt.Fprintf(out, "── vroom ✗ %s failed (exit %d, %s) ──\n", kind, exitCode, elapsed)
		return elapsed, exitCode, runErr
	}
	_, _ = fmt.Fprintf(out, "── vroom ✓ %s ok (%s) ──\n", kind, elapsed)
	return elapsed, 0, nil
}

func jobCmd(path, kind, command, workDir, stdoutPath, stderrPath string) tea.Cmd {
	return func() tea.Msg {
		elapsed, exitCode, err := runLogged(kind, command, workDir, stdoutPath, stderrPath)
		msg := jobMsg{path: path, kind: kind, command: command, exitCode: exitCode, elapsed: elapsed}
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				msg.err = err // launch failure (I/O), not a command failure
			}
		}
		return msg
	}
}

func editLogsCmd(editor, stdoutPath, stderrPath string, stderrFirst bool) tea.Cmd {
	cmd := buildEditorCmd(editor, stdoutPath, stderrPath, stderrFirst)
	// The callback is a named function, not an inline closure, because ExecProcess returns a private message type that no test can construct.
	return tea.ExecProcess(cmd, editorDoneMsg)
}

// Only editor failures matter: the editor is the user's, not vroom's.
func editorDoneMsg(err error) tea.Msg {
	if err != nil {
		return statusMsg{message: "editor exited with error: " + err.Error()}
	}
	return statusMsg{message: "editor closed"}
}

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

// $VISUAL wins over $EDITOR on purpose, the reverse of the usual convention.
func resolveEditor() string {
	if v := os.Getenv("VISUAL"); v != "" {
		return v
	}
	if v := os.Getenv("EDITOR"); v != "" {
		return v
	}
	return "nvim"
}

func (m Model) View() tea.View {
	content := m.renderDashboard()
	if m.askPromptOpen {
		content = overlay(content, m.askBox(), m.width, m.height)
	} else if m.pickerOpen {
		content = overlay(content, m.pickerBox(), m.width, m.height)
	} else if m.termOpen {
		content = overlay(content, m.termBox(), m.width, m.height)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion // wheel scrolls the console
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
		if len(m.entries) > 0 {
			_, cl := m.treeLines()
			if cl < m.treeTop {
				m.treeTop = cl
			} else if cl >= m.treeTop+m.treeVis() {
				m.treeTop = cl - m.treeVis() + 1
			}
			// The cursor branches above leave a stale treeTop when the window grows, so clamp it too or the tree renders one row over 393 blank lines.
			if tope := len(m.tree) - m.treeVis(); m.treeTop > tope {
				m.treeTop = max(0, tope)
			}
		}
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
			m.clearMessage()
		}
		cmds := []tea.Cmd{refreshCmd(m.store, m.manager, m.projects), tickCmd()}
		if p := m.selected(); p != nil && p.Configured && m.isRunning(p.Path) {
			cmds = append(cmds, threadsCmd(p.Path, m.services[p.Path].Meta.Pid))
			if m.activeTab == tabMetrics {
				cmds = append(cmds, m.metricsCmd())
			}
		}
		if m.activeTab == tabHealth {
			cmds = append(cmds, m.healthCmd())
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
				continue // transient: startedMsg/stoppedMsg resolve it
			}
			sv.Status = mapUIStatus(r.status)
			sv.Meta = r.meta
		}
		for _, r := range msg.results {
			if r.warn != "" {
				m.notify(r.warn)
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
		for _, w := range msg.warns {
			m.notify(w)
		}
		if sv != nil {
			sv.Status = mapUIStatus(stateOfMeta(msg.meta))
			sv.Meta = msg.meta
		}
		m.addEvent(msg.path, "start", "", 0, true)
		// Start truncated the log files, so the in-memory buffers must be reset or the view shows stale content.
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
		if m.pendingRestart[msg.path] {
			delete(m.pendingRestart, msg.path)
			m.addEvent(msg.path, "restart", "", 0, true)
			if p := m.projectByPath(msg.path); p != nil && sv != nil {
				sv.Status = statusStarting
				return m, startCmd(m.store, m.manager, *p)
			}
		}
		if sv != nil {
			sv.Status = statusStopped
		}
		m.addEvent(msg.path, "stop", "", 0, true)
		return m, nil

	case consoleDeltaMsg:
		m.applyConsoleDelta(msg)
		return m, nil

	case threadsMsg:
		m.applyThreads(msg)
		return m, nil

	case metricsMsg:
		m.applyMetrics(msg)
		return m, nil

	case gitMsg:
		m.applyGit(msg)
		return m, nil

	case envMsg:
		m.applyEnv(msg)
		return m, nil

	case healthMsg:
		m.healthRes[msg.path] = msg.r
		return m, nil

	case statusMsg:
		m.notify(msg.message)
		return m, nil

	case jobMsg:
		delete(m.jobs, msg.path) // releases the project lock
		ok := msg.err == nil && msg.exitCode == 0
		m.addEvent(msg.path, msg.kind, msg.command, msg.elapsed, ok)
		switch {
		case msg.err != nil:
			m.notify(msg.kind + " error: " + msg.err.Error())
		case msg.exitCode != 0:
			m.notify(fmt.Sprintf("%s failed (exit %d, %s)", msg.kind, msg.exitCode, msg.elapsed))
		default:
			m.notify(fmt.Sprintf("%s ok (%s)", msg.kind, msg.elapsed))
		}
		return m, nil

	case ptyDataMsg: // shell bytes into the emulator, which re-arms the read loop
		if s := m.term; s != nil {
			s.write(msg.data)
			return m, readPtyCmd(s)
		}
		return m, nil

	case ptyEOFMsg: // the reaper armed at open does the cleanup
		return m, nil

	case ptyExitMsg:
		if s := m.term; s != nil {
			s.shutdown() // idempotent, and unblocks the read loop if one hung
			m.termOpen = false
			if code := exitCode(msg.err); code != 0 {
				m.notify(fmt.Sprintf("terminal exited (%d)", code))
			} else {
				m.notify("terminal closed")
			}
			m.term = nil
		}
		return m, nil

	case stackResultMsg:
		if msg.err != nil {
			m.notify("stack error: " + msg.err.Error())
		} else if !msg.result.OK {
			m.notify("stack failed: " + msg.result.Error)
			m.recordStackEventByName(msg.result.Stack, false)
		} else {
			m.notify(fmt.Sprintf("stack %s launched", msg.result.Stack))
			m.recordStackEventByName(msg.result.Stack, true)
		}
		return m, nil

	case composersResultMsg:
		failed := 0
		for _, r := range msg.results {
			if !r.OK {
				failed++
			}
			m.recordStackEventByName(r.Stack, r.OK)
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

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
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
	case process.StatusPortPending:
		return statusPortPending
	case process.StatusPortUnresolved:
		return statusPortUnresolved
	case process.StatusNoPort:
		return statusNoPort
	case process.StatusStopped:
		return statusStopped
	default:
		return statusStopped
	}
}

// state and process deliberately share the status names (see adr-0012), so this cast is the only translation point.
func stateOfMeta(meta state.Meta) process.Status {
	if meta.State == "" {
		return process.StatusUnknown
	}
	return process.Status(meta.State)
}

func (m Model) handleKey(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg := msg.(tea.KeyMsg)
	key := keyMsg.String()
	if m.termOpen {
		return m.termKey(keyMsg)
	}
	if m.askPromptOpen {
		return m.askKey(keyMsg)
	}
	if m.pickerOpen {
		return m.pickerKey(key)
	}
	if m.filterOpen {
		return m.filterKey(keyMsg)
	}
	// Universal keys are not remappable, not even "/" and "!".
	switch key {
	case "q", "ctrl+c":
		return m, m.quitCmd()
	case "esc":
		if m.filterText != "" { // a filter means esc clears instead of quitting
			return m.applyFilter("")
		}
		return m, m.quitCmd()
	case "!":
		return m.openTerm()
	case "/":
		return m.openFilter()
	case "enter":
		return m.enterSelection()
	case "j", "k", "up", "down":
		return m.navigate(key)
	case "tab":
		return m.switchTab((m.activeTab + 1) % tabCount)
	case "shift+tab":
		return m.switchTab((m.activeTab + tabCount - 1) % tabCount)
	case "1", "2", "3", "4", "5", "6", "7":
		return m.switchTab(tabKind(key[0] - '1'))
	case "pgup": // scrolling pauses follow, unless details should scroll
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
	switch m.keyActions[key] {
	case "start_stop":
		return m.toggleSelected()
	case "restart":
		return m.restartSelected()
	case "build":
		if it, ok := m.selectedItem(); ok && it.kind == itemStack {
			m.notify("not available for stacks")
			return m, nil
		}
		return m.runBuild()
	case "install":
		if it, ok := m.selectedItem(); ok && it.kind == itemStack {
			m.notify("not available for stacks")
			return m, nil
		}
		return m.runInstall()
	case "tasks":
		return m.openPicker()
	case "ask":
		return m.openAsk()
	case "clear":
		return m.clearConsole()
	case "stream":
		m.stream = (m.stream + 1) % 3
		return m, m.syncConsoleView()
	case "top":
		m.consoleFollow = false
		m.consoleView.GotoTop()
		return m, nil
	case "bottom":
		m.consoleFollow = true
		m.consoleView.GotoBottom()
		return m, nil
	case "logs":
		if it, ok := m.selectedItem(); ok && it.kind == itemStack {
			m.notify("not available for stacks")
			return m, nil
		}
		return m.openLogEditor()
	case "refresh":
		return m, m.refreshBatch()
	}
	if key == "o" { // fixed alias for logs unless the config claims it
		return m.openLogEditor()
	}
	return m, nil
}

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
	m.detailsTop = 0
	_, cursorLine := m.treeLines()
	visH := m.treeVis()
	if cursorLine < m.treeTop {
		m.treeTop = cursorLine
	} else if cursorLine >= m.treeTop+visH {
		m.treeTop = cursorLine - visH + 1
	}
	return m.onSelect()
}

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
		return m, nil
	case itemRepo:
		m.toggleRepoCollapse(it.repoPath)
	case itemProject:
		if it.hasKids {
			m.toggleRepoCollapse(it.repoPath)
			break
		}
		if it.secondary != "" {
			key := m.secondaryKey(it.primary, it.secondary)
			m.collapsed[key] = !m.collapsed[key]
		} else if it.primary != "" {
			m.collapsed[it.primary] = !m.collapsed[it.primary]
		} else {
			return m, nil
		}
	}
	m.tree = m.buildTree()
	// Best-effort: a failed persist must never block the UI.
	_ = m.store.SaveCollapsed(m.collapsed)
	return m, nil
}

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
	return m, m.refreshTab()
}

func (m Model) switchTab(tab tabKind) (tea.Model, tea.Cmd) {
	m.activeTab = tab
	return m, m.refreshTab()
}

// Pointer receiver so the console refresh, which rewrites the viewport, survives the return.
func (m *Model) refreshTab() tea.Cmd {
	p := m.selected()
	switch m.activeTab {
	case tabConsole:
		if p != nil && p.Configured {
			cs := m.consoleStateFor(p.Path)
			m.setConsoleContent(cs.view(m.stream))
			return m.tailCmd()
		}
	case tabThreads:
		return m.refreshThreads()
	case tabMetrics:
		return m.metricsCmd()
	case tabGit:
		return m.gitCmd()
	case tabEnv:
		return m.envCmd()
	case tabHealth:
		return m.healthCmd()
	}
	return nil
}

func (m Model) refreshBatch() tea.Cmd {
	cmds := []tea.Cmd{refreshCmd(m.store, m.manager, m.projects), m.gitCmd()}
	if c := m.refreshTab(); c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

func (m Model) refreshThreads() tea.Cmd {
	p := m.selected()
	if p == nil || !p.Configured || !m.isRunning(p.Path) {
		return nil
	}
	return threadsCmd(p.Path, m.services[p.Path].Meta.Pid)
}

func (m Model) isRunning(path string) bool {
	sv, ok := m.services[path]
	return ok && sv.Meta.Pid > 0 && sv.Status.alive()
}

func (m Model) tailCmd() tea.Cmd {
	p := m.selected()
	if p == nil {
		return nil
	}
	cs := m.consoleStateFor(p.Path)
	return consoleTailCmd(p.Path, cs.off[0], cs.off[1], m.store.StdoutLog(p.Path), m.store.StderrLog(p.Path))
}

func (m *Model) applyConsoleDelta(msg consoleDeltaMsg) {
	cs := m.consoleStateFor(msg.path)
	// Offsets advance per stream and only when that stream was read: advancing past unread bytes would skip those lines forever.
	if msg.errS == nil {
		cs.stdout = tail.CapBuffer(cs.stdout+msg.stdout, maxConsoleBytes)
		cs.off[0] = msg.offS
	}
	if msg.errE == nil {
		cs.stderr = tail.CapBuffer(cs.stderr+msg.stderr, maxConsoleBytes)
		cs.off[1] = msg.offE
	}
	cs.merged = tail.CapBuffer(cs.merged+msg.stdout+msg.stderr, maxConsoleBytes)

	if m.activeTab != tabConsole || m.selected() == nil || m.selected().Path != msg.path {
		return
	}
	m.setConsoleContent(cs.view(m.stream))
}

func (m *Model) applyThreads(msg threadsMsg) {
	if msg.err != nil {
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

func (m *Model) consoleStateFor(path string) *consoleState {
	cs, ok := m.consoleStates[path]
	if !ok {
		cs = &consoleState{}
		m.consoleStates[path] = cs
	}
	return cs
}

func (m *Model) setConsoleContent(content string) {
	if content == "" {
		content = "No logs available"
	}
	y := m.consoleView.YOffset()
	m.consoleView.SetContent(highlightConsole(sanitizeConsole(content)))
	if m.consoleFollow {
		m.consoleView.GotoBottom()
	} else {
		m.consoleView.SetYOffset(y)
	}
}

func (m *Model) syncConsoleView() tea.Cmd {
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

// Never a double start: a running service always stops instead.
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
	if !p.Configured {
		m.notify("No manifest — create a .vroom.toml to enable")
		return m, nil
	}
	sv := m.services[p.Path]
	switch sv.Status {
	case statusRunning, statusUnknown, statusPortPending, statusNoPort:
		sv.Status = statusStopping
		m.clearMessage()
		return m, stopCmd(m.store, m.manager, p.Path, p.Manifest.Stop)
	case statusStarting, statusStopping:
		return m, nil
	default:
		sv.Status = statusStarting
		m.clearMessage()
		// Clear the in-memory console now so the old logs are not visible while starting.
		cs := m.consoleStateFor(p.Path)
		cs.stdout, cs.stderr, cs.merged = "", "", ""
		cs.off[0] = fileSizeOrZero(m.store.StdoutLog(p.Path))
		cs.off[1] = fileSizeOrZero(m.store.StderrLog(p.Path))
		m.setConsoleContent("")
		return m, startCmd(m.store, m.manager, *p)
	}
}

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
			cs := m.consoleStateFor(p.Path)
			cs.stdout, cs.stderr, cs.merged = "", "", ""
			cs.off[0] = fileSizeOrZero(m.store.StdoutLog(p.Path))
			cs.off[1] = fileSizeOrZero(m.store.StderrLog(p.Path))
			if sel != nil && sel.Path == p.Path {
				m.setConsoleContent("")
			}
			cmds = append(cmds, startCmd(m.store, m.manager, p))
		case !anyStopped && sv.Status.alive():
			sv.Status = statusStopping
			cmds = append(cmds, stopCmd(m.store, m.manager, p.Path, manifestStop(p)))
		}
	}
	return m, tea.Batch(cmds...)
}

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
	// Two passes on purpose: validating and deciding used to share one loop whose `break` hid a bad name in any stack after the first.
	todos := make([][]orchestrate.ResolvedService, len(stacks))
	allRunning := true
	for i := range stacks {
		resueltos, r, n, err := m.resolveStack(&stacks[i])
		if err != nil {
			m.notify("stack conflict: " + err.Error())
			return m, nil
		}
		todos[i] = resueltos
		if n == 0 || r < n {
			allRunning = false
		}
	}
	if allRunning {
		for i := range stacks {
			m.engine.StopResolved(todos[i])
		}
		m.notify(fmt.Sprintf("stopping all stacks in %s", primary))
		return m, nil
	}
	m.notify(fmt.Sprintf("launching stacks in %s...", primary))
	return m, func() tea.Msg {
		var results []orchestrate.LaunchResult
		for i := range stacks {
			results = append(results, *m.engine.LaunchResolved(&stacks[i], todos[i]))
		}
		return composersResultMsg{primary: primary, results: results}
	}
}

type composersResultMsg struct {
	primary string
	results []orchestrate.LaunchResult
}

func (m Model) toggleStack(s *orchestrate.Stack) (tea.Model, tea.Cmd) {
	if m.engine == nil {
		m.notify("orchestration engine not available")
		return m, nil
	}
	resueltos, running, total, err := m.resolveStack(s)
	if err != nil {
		m.notify("stack conflict: " + err.Error())
		return m, nil
	}
	if running == total && total > 0 {
		// Stop every resolved service, never an arbitrary first name match.
		m.engine.StopResolved(resueltos)
		m.markStackStopping(s)
		m.notify(fmt.Sprintf("stopping stack %s", s.Name))
		return m, nil
	}
	m.notify(fmt.Sprintf("launching stack %s...", s.Name))
	return m, func() tea.Msg {
		return stackResultMsg{result: *m.engine.LaunchResolved(s, resueltos)}
	}
}

func (m Model) markStackStopping(s *orchestrate.Stack) {
	seen := make(map[string]bool)
	for _, stage := range s.Stages {
		for _, name := range stage.Services {
			if seen[name] {
				continue
			}
			seen[name] = true
			p, err := orchestrate.LookupService(name, m.projects)
			if err != nil {
				continue
			}
			if sv := m.services[p.Path]; sv != nil && sv.Status.alive() {
				sv.Status = statusStopping
			}
		}
	}
}

// Nested worktree rows and repo container rows are excluded: they render under their own repo row.
func (m Model) nodeMembers(primary, secondary string) []scanner.Project {
	var out []scanner.Project
	for _, e := range m.entries {
		if e.Project.IsNestedRow() {
			continue
		}
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
		if m.onHeader() {
			m.notify("select a service to restart")
		}
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

func manifestStop(p scanner.Project) string {
	if p.Manifest == nil {
		return ""
	}
	return p.Manifest.Stop
}

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
		case pickerAgents:
			m.pickerOpen = false
			return m.startAskPrompt(agents.Agent{Name: it.Name, Cmd: it.agentCmd})
		default:
			m.pickerOpen = false
			return m.launchJob("task", "mise run "+it.Name)
		}
	}
	return m, nil
}

// Keeps the applied text so `/` also means "edit the filter".
func (m Model) openFilter() (tea.Model, tea.Cmd) {
	m.filterOpen = true
	m.filterInput.SetValue(m.filterText)
	return m, m.filterInput.Focus()
}

func (m Model) filterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.filterOpen = false
		m.filterInput.Reset()
		return m.applyFilter("")
	case "enter":
		m.filterOpen = false // the filter is already live
		return m, nil
	}
	ni, cmd := m.filterInput.Update(msg)
	m.filterInput = ni
	if v := ni.Value(); v != m.filterText {
		next, _ := m.applyFilter(v)
		m = next.(Model)
	}
	return m, cmd
}

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

// An empty [ask] prompt template means no prefill.
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

// Must be called after any Prompt/width change and before Focus.
func (m *Model) sizeAskPrompt() {
	m.promptInput.SetWidth(askInnerW(m.width))
	maxH := m.height - 8 // title + blank + hint + borders + margin
	if maxH > askMaxHeightCap {
		maxH = askMaxHeightCap
	}
	if maxH < askMinHeight {
		maxH = askMinHeight
	}
	m.promptInput.MaxHeight = maxH
}

// Unknown placeholders are left untouched so a typo is visible, not silently dropped.
func expandAskPrompt(tmpl, name, dir, logs string) string {
	r := strings.NewReplacer(
		"{name}", name,
		"{dir}", dir,
		"{logs}", logs,
	)
	return r.Replace(tmpl)
}

func (m Model) askKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		// Unconditional: making the only escape depend on an empty text input left no way out of a typed request.
		return m, tea.Quit
	case "q":
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
		return m, tea.ExecProcess(m.askLauncher.InlineCmd(req), inlineAgentDoneMsg(m.askAgent.Name))
	}
	return m, launchAskCmd(m.askLauncher, strategy, req)
}

// Named function, like editorDoneMsg, because ExecProcess returns a private message type no test can construct.
func inlineAgentDoneMsg(agent string) func(error) tea.Msg {
	return func(err error) tea.Msg {
		if err != nil {
			return statusMsg{message: agent + " exited with error: " + err.Error()}
		}
		return statusMsg{message: agent + " closed"}
	}
}

// herdr/custom strategies must not block the UI.
func launchAskCmd(l *launcher.Launcher, strategy string, req launcher.Request) tea.Cmd {
	return func() tea.Msg {
		msg, err := l.Launch(strategy, req)
		if err != nil {
			return statusMsg{message: err.Error()}
		}
		return statusMsg{message: msg}
	}
}

func fileSizeOrZero(path string) int64 {
	if fi, err := os.Stat(path); err == nil {
		return fi.Size()
	}
	return 0
}

// Only the in-memory buffers and offsets move; the log files on disk are left untouched.
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

func (m Model) selectedItem() (treeItem, bool) {
	if m.cursor < 0 || m.cursor >= len(m.tree) {
		return treeItem{}, false
	}
	return m.tree[m.cursor], true
}

func (m Model) selectedItemKind() treeItemKind {
	it, ok := m.selectedItem()
	if !ok {
		return -1
	}
	return it.kind
}

func (m *Model) scrollDetails(delta int) {
	m.detailsTop += delta
	if m.detailsTop < 0 {
		m.detailsTop = 0
	}
}

func (m Model) selected() *scanner.Project {
	it, ok := m.selectedItem()
	if !ok || it.kind != itemProject {
		return nil
	}
	p := it.project
	return &p
}

func (m Model) onHeader() bool {
	it, ok := m.selectedItem()
	return ok && (it.kind == itemPrimary || it.kind == itemSecondary)
}

func (m Model) selectedStack() *orchestrate.Stack {
	it, ok := m.selectedItem()
	if !ok || it.kind != itemStack || it.stack == nil {
		return nil
	}
	return it.stack
}

func (m Model) selectedNode() (primary, secondary string) {
	it, ok := m.selectedItem()
	if !ok || (it.kind != itemPrimary && it.kind != itemSecondary) {
		return "", ""
	}
	return it.primary, it.secondary
}

// Composite key so two different primaries cannot collide on the same secondary name.
func (m Model) secondaryKey(primary, secondary string) string {
	return primary + "/" + secondary
}

// Repo rows live in the same persisted map under `repo:<path>`, with inverted semantics (collapsed by default).
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

// Never the declared port when the state is unresolved: it may belong to another worktree's twin, and an unconfirmed port is not a legitimate probe target (see docs/adr/adr-0012-port-ownership-contract-and-dynamic-ports.md).
func displayPort(p scanner.Project, sv *ServiceState) int {
	if sv != nil {
		if sv.Meta.State == state.StatePortUnresolved {
			return 0
		}
		if sv.Meta.Port > 0 {
			return sv.Meta.Port
		}
	}
	if p.Manifest != nil && p.Manifest.Port > 0 {
		return p.Manifest.Port
	}
	return 0
}

// portOrigin says where the shown number came from: (Dynamic) only for a port vroom discovered, so a dynamic manifest
// whose declared fallback is on screen still reads (Fixed) — the label describes the number, not the mode's intent.
func portOrigin(p scanner.Project, sv *ServiceState) string {
	if p.Manifest != nil && p.Manifest.EffectivePortMode() == manifest.PortModeDynamic &&
		sv != nil && sv.Meta.Port > 0 {
		return "(Dynamic)"
	}
	return "(Fixed)"
}

func statusBadge(p scanner.Project, sv *ServiceState, spinnerView, startSpinnerView string) string {
	if p.Configured && p.ManifestErr == "" {
		port := ""
		if n := displayPort(p, sv); n > 0 {
			port = " " + styleDim.Render(fmt.Sprintf(":%d %s", n, portOrigin(p, sv)))
		}
		switch sv.Status {
		case statusRunning:
			return styleRunning.Render("● running") + port
		case statusStarting:
			return startSpinnerView + styleStarting.Render(" starting")
		case statusStopping:
			return styleStopping.Render("○ stopping")
		case statusPortPending:
			return startSpinnerView + styleStarting.Render(" starting, port pending")
		case statusPortUnresolved:
			return styleRunning.Render("● running") + " " + styleWarn.Render(" port unresolved")
		case statusNoPort:
			return styleRunning.Render("● running") + " " + styleDim.Render("(no port)")
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

// n <= 0 must not index negative: with ~40 width-arithmetic callers a panic in a text helper blanks the whole TUI.
func trunc(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return string(r[:1])
	}
	return string(r[:n-1]) + "…"
}

// n == 0 must yield nothing: the last rune alone reads as garbage, not as "does not fit".
func truncTail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return string(r[len(r)-1:])
	}
	return "…" + string(r[len(r)-n+1:])
}

// ANSI-aware width, so escape sequences never count as visible cells.
func padW(s string, w int) string {
	d := w - lipglossWidth(s)
	if d <= 0 {
		return s
	}
	return s + strings.Repeat(" ", d)
}

// A nil or incomplete map means the defaults.
func kbKey(kb map[string]string, action string) string {
	if k := kb[action]; k != "" {
		return k
	}
	return config.DefaultKeybindings()[action]
}

func helpSeg(kb map[string]string, action, label string) string {
	return kbKey(kb, action) + " " + label
}

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
		"1-7 tabs",
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
