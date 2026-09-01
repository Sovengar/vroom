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

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"vroom/internal/group"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

const (
	pollInterval = 2 * time.Second // spec R13
	tailMaxBytes = 64 * 1024
)

type viewKind int

const (
	viewList viewKind = iota
	viewService
	viewLogs
)

// uiStatus es el estado mostrado en la UI, incluyendo los transitorios
// starting/stopping (spec R14).
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

// Model es el modelo principal de la TUI.
type Model struct {
	width, height int
	root          string
	store         *state.Store
	manager       process.Manager

	projects []scanner.Project
	entries  []group.Entry
	services map[string]*ServiceState // clave: ruta absoluta del proyecto

	cursor      int
	view        viewKind
	logViewport viewport.Model
	logStderr   bool // false = stdout, true = stderr (spec R16)

	message          string
	messageExpiresAt time.Time // cero = sin expiración
	pendingRestart   map[string]bool
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

// New construye el modelo: escanea el CWD, agrupa y fija estados iniciales.
func New(store *state.Store, manager process.Manager, root string) Model {
	projects, err := scanner.Scan(root)
	m := Model{
		root:           root,
		store:          store,
		manager:        manager,
		services:       make(map[string]*ServiceState, len(projects)),
		pendingRestart: make(map[string]bool),
		width:          80,
		height:         24,
	}
	if err != nil {
		m.notify("error scanning projects: " + err.Error())
	}
	m.projects = projects
	for _, p := range projects {
		if p.Configured {
			m.services[p.Path] = &ServiceState{Status: statusStopped}
		} else {
			m.services[p.Path] = &ServiceState{Status: statusUnconfigured}
		}
	}
	m.entries = group.Arrange(projects)
	return m
}

// ---- Mensajes ----

type tickMsg time.Time

type refreshResult struct {
	status process.Status
	meta   state.Meta
	warn   string
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

type logTailMsg struct {
	path     string
	isStderr bool
	content  string
}

type statusMsg struct{ message string }

// ---- Comandos ----

func tickCmd() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// refreshCmd re-verifica liveness de todos los servicios configurados
// leyendo meta.json del disco (soporta re-adjunta, spec S7.2/R9).
func refreshCmd(store *state.Store, manager process.Manager, projects []scanner.Project) tea.Cmd {
	return func() tea.Msg {
		results := make(map[string]refreshResult, len(projects))
		for _, p := range projects {
			if !p.Configured {
				continue
			}
			r := refreshResult{}
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
			Group:          p.Manifest.Group,
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

func stopCmd(store *state.Store, manager process.Manager, path string) tea.Cmd {
	return func() tea.Msg {
		meta, err := store.LoadMeta(path)
		if err == nil && meta.Pgid > 0 {
			// S8.1/S8.2: SIGTERM al PGID, timeout 5s, SIGKILL al PGID.
			_ = manager.Stop(process.StopSpec{Pgid: meta.Pgid, Timeout: process.DefaultStopTimeout})
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
		return stoppedMsg{path: path}
	}
}

func logTailCmd(store *state.Store, path string, stderr bool) tea.Cmd {
	return func() tea.Msg {
		file := store.StdoutLog(path)
		if stderr {
			file = store.StderrLog(path)
		}
		content, err := tailFile(file, tailMaxBytes)
		if err != nil {
			content = "(could not read log: " + err.Error() + ")"
		} else if content == "" {
			content = "No logs available" // S16.2
		}
		return logTailMsg{path: path, isStderr: stderr, content: content}
	}
}

// editLogsCmd suspende la TUI y abre ambos ficheros de log en el editor
// del usuario (spec R17): $VISUAL/$EDITOR, default nvim. Para
// vim/nvim añade -O (split vertical) con el foco en el stream activo.
// Al cerrar el editor la TUI se restaura con el callback.
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
// stderrFirst decide qué fichero se abre con el foco.
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

// tailFile devuelve los últimos maxBytes del fichero.
func tailFile(path string, maxBytes int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	var offset int64
	if info.Size() > maxBytes {
		offset = info.Size() - maxBytes
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return "", err
	}
	buf := make([]byte, info.Size()-offset)
	if _, err := f.Read(buf); err != nil && len(buf) == 0 {
		return "", err
	}
	return string(buf), nil
}

// ---- Init / Update / View ----

func (m Model) View() tea.View {
	var content string
	switch m.view {
	case viewLogs:
		content = m.renderLogs()
	case viewService:
		content = m.renderService()
	default:
		content = m.renderList()
	}
	v := tea.NewView(content)
	v.AltScreen = true // dashboard a pantalla completa (v2: alt screen desde View)
	return v
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(refreshCmd(m.store, m.manager, m.projects), tickCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeViewport()
		return m, nil

	case tickMsg:
		if !m.messageExpiresAt.IsZero() && time.Now().After(m.messageExpiresAt) {
			m.clearMessage() // el polling no borra mensajes antes de expirar
		}
		cmds := []tea.Cmd{refreshCmd(m.store, m.manager, m.projects), tickCmd()}
		if m.view == viewLogs {
			if p := m.selected(); p != nil && p.Configured {
				cmds = append(cmds, logTailCmd(m.store, p.Path, m.logStderr))
			}
		}
		return m, tea.Batch(cmds...)

	case refreshedMsg:
		for path, r := range msg.results {
			sv, ok := m.services[path]
			if !ok {
				continue
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

	case logTailMsg:
		if m.view == viewLogs {
			if p := m.selected(); p != nil && p.Path == msg.path && msg.isStderr == m.logStderr {
				m.logViewport.SetContent(msg.content)
				m.logViewport.GotoBottom()
			}
		}
		return m, nil

	case statusMsg:
		m.notify(msg.message)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
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
	key := msg.(tea.KeyMsg).String()
	switch m.view {
	case viewLogs:
		switch key {
		case "tab": // S16.1: alternar stdout/stderr
			m.logStderr = !m.logStderr
			if p := m.selected(); p != nil && p.Configured {
				return m, logTailCmd(m.store, p.Path, m.logStderr)
			}
			return m, nil
		case "esc":
			m.view = viewService
			return m, nil
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r": // S-T6
			return m, m.refreshBatch()
		case "o":
			return m.openLogEditor()
		default:
			return m.updateViewportKeys(msg)
		}
	case viewService:
		switch key {
		case "esc":
			m.view = viewList
			return m, nil
		case "q", "ctrl+c":
			return m, tea.Quit
		case "s":
			return m.toggleSelected()
		case "R":
			return m.restartSelected()
		case "l":
			return m.openLogs()
		case "o":
			return m.openLogEditor()
		case "r":
			return m, m.refreshBatch()
		}
	default: // viewList
		switch key {
		case "q", "esc", "ctrl+c": // R12: q/Esc para salir
			return m, tea.Quit
		case "j", "down": // S12.1: navegación cíclica
			if len(m.entries) > 0 {
				m.cursor = (m.cursor + 1) % len(m.entries)
			}
			return m, nil
		case "k", "up":
			if len(m.entries) > 0 {
				m.cursor = (m.cursor - 1 + len(m.entries)) % len(m.entries)
			}
			return m, nil
		case "enter":
			if len(m.entries) > 0 {
				m.view = viewService
			}
			return m, nil
		case "s":
			return m.toggleSelected()
		case "R":
			return m.restartSelected()
		case "l":
			return m.openLogs()
		case "o":
			return m.openLogEditor()
		case "r": // S-T6: refresh forzado inmediato
			return m, m.refreshBatch()
		}
	}
	return m, nil
}

func (m Model) updateViewportKeys(msg tea.Msg) (tea.Model, tea.Cmd) {
	vp, cmd := m.logViewport.Update(msg)
	m.logViewport = vp
	return m, cmd
}

// refreshBatch re-verifica liveness (y re-tailea logs si la vista está abierta).
func (m Model) refreshBatch() tea.Cmd {
	cmds := []tea.Cmd{refreshCmd(m.store, m.manager, m.projects)}
	if m.view == viewLogs {
		if p := m.selected(); p != nil && p.Configured {
			cmds = append(cmds, logTailCmd(m.store, p.Path, m.logStderr))
		}
	}
	return tea.Batch(cmds...)
}

// ---- Acciones de servicio (spec R14/R15) ----

// toggleSelected es la acción contextual de `s`: start si está parado,
// stop si está corriendo (o unknown). Nunca hace doble start: si el
// estado es running el toggle SIEMPRE para (S15.1).
func (m Model) toggleSelected() (tea.Model, tea.Cmd) {
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
		return m, stopCmd(m.store, m.manager, p.Path)
	case statusStarting, statusStopping:
		return m, nil // en tránsito: ignorar
	default: // stopped
		sv.Status = statusStarting // S14.1
		m.clearMessage()
		return m, startCmd(m.store, m.manager, *p)
	}
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
	if sv.Status != statusRunning {
		m.notify("only a running service can be restarted")
		return m, nil
	}
	m.pendingRestart[p.Path] = true
	sv.Status = statusStopping
	return m, stopCmd(m.store, m.manager, p.Path)
}

func (m Model) openLogs() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
		return m, nil
	}
	if !p.Configured {
		m.notify("no manifest — no service logs")
		return m, nil
	}
	m.view = viewLogs
	m.resizeViewport()
	return m, logTailCmd(m.store, p.Path, m.logStderr)
}

// openLogEditor abre ambos logs del servicio seleccionado en el editor.
func (m Model) openLogEditor() (tea.Model, tea.Cmd) {
	p := m.selected()
	if p == nil {
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
		m.logStderr, // foco en el stream activo de la vista de logs
	)
}

func (m *Model) resizeViewport() {
	// header + línea en blanco + help + margen para la línea de mensajes
	h := m.height - 5
	if h < 3 {
		h = 3
	}
	m.logViewport.SetWidth(m.width)
	m.logViewport.SetHeight(h)
}

// ---- Helpers de selección ----

func (m Model) selected() *scanner.Project {
	if m.cursor < 0 || m.cursor >= len(m.entries) {
		return nil
	}
	p := m.entries[m.cursor].Project
	return &p
}

func (m Model) projectByPath(path string) *scanner.Project {
	for i := range m.projects {
		if m.projects[i].Path == path {
			return &m.projects[i]
		}
	}
	return nil
}

func statusBadge(p scanner.Project, sv *ServiceState) string {
	if p.Configured && p.ManifestErr == "" {
		port := ""
		if p.Manifest != nil && p.Manifest.Port > 0 {
			port = " " + styleDim.Render(fmt.Sprintf(":%d", p.Manifest.Port))
		}
		switch sv.Status {
		case statusRunning:
			return styleRunning.Render("● running") + port
		case statusStarting:
			return styleStarting.Render("○ starting")
		case statusStopping:
			return styleStopping.Render("○ stopping")
		case statusUnknown:
			return styleUnknown.Render("? unknown") + port
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

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// helpLines es la ayuda completa por vista (se recorta según ancho).
var helpLines = map[viewKind]string{
	viewLogs:    "tab stdout/stderr · pgup/pgdn scroll · r refresh · o open logs · esc back · q quit",
	viewService: "s start/stop · R restart · l log view · o open logs · r refresh · esc back · q quit",
	viewList:    "j/k move · enter details · s start/stop · R restart · l log view · o open logs · r refresh · q quit",
}

// helpCompact es la versión reducida para terminales estrechas.
var helpCompact = map[viewKind]string{
	viewLogs:    "tab toggle · r refresh · o open · esc back · q quit",
	viewService: "s start/stop · l logs · o open · esc back · q quit",
	viewList:    "j/k move · s start/stop · l logs · o open · r refresh · q quit",
}

// helpLine devuelve la ayuda que cabe en width: completa, compacta o
// truncada (responsive).
func helpLine(view viewKind, width int) string {
	if width <= 0 {
		width = 80
	}
	h := helpLines[view]
	if utf8.RuneCountInString(h) > width {
		h = helpCompact[view]
	}
	return trunc(h, width)
}
