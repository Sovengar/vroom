package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	xpty "github.com/charmbracelet/x/xpty"
)

// Dimensiones del modal de terminal: el grid del emulador
// llena el interior del box, igual que el textarea del ask.
const (
	termMinW = 20
	termMinH = 6
	termMaxH = 40
)

// ptyIface es la porción de xpty.Pty que usa termSession; interfaz
// propia para poder stubarla en tests.
type ptyIface interface {
	io.ReadWriteCloser
	Resize(width, height int) error
	Start(cmd *exec.Cmd) error
}

// termSession es una sesión de shell persistente: un PTY
// corriendo el shell del usuario más el emulador VT que mantiene su
// pantalla. Ocultar el modal NO la mata; solo shutdown() lo hace.
//
// El emulador no es thread-safe: todas sus mutaciones (Write del
// output del pty, SendKey, Resize, Render) ocurren en el hilo de
// Update; la goroutine pump solo lee del input pipe y escribe al PTY.
type termSession struct {
	mu     sync.Mutex
	pty    ptyIface
	emu    *vt.Emulator
	cmd    *exec.Cmd
	dir    string // cwd fijo de la sesión (título del modal)
	pgid   int    // process group del shell (Setsid: pgid = pid)
	w, h   int    // dims actuales del grid
	closed bool   // shutdown ya ejecutado
}

// resolveShell devuelve el shell del usuario: $SHELL o sh.
func resolveShell() string {
	if v := os.Getenv("SHELL"); v != "" {
		return v
	}
	return "sh"
}

// termEnv hereda el entorno y garantiza TERM para el shell del PTY.
func termEnv() []string {
	env := os.Environ()
	if os.Getenv("TERM") == "" {
		env = append(env, "TERM=xterm-256color")
	}
	return env
}

// newTermSession lanza el shell del usuario (interactivo) en un PTY
// nuevo con cwd dir.
func newTermSession(w, h int, dir string) (*termSession, error) {
	return startSession(w, h, dir, []string{resolveShell()})
}

// startSession es el constructor inyectable (argv explícito) para tests.
func startSession(w, h int, dir string, argv []string) (*termSession, error) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	p, err := xpty.NewPty(w, h)
	if err != nil {
		return nil, err
	}
	emu := vt.NewEmulator(w, h)
	emu.SetScrollbackSize(1000)
	cmd := exec.Command(argv[0], argv[1:]...) // nolint:gosec // argv deliberado del shell del usuario
	cmd.Dir = dir
	cmd.Env = termEnv()
	setSessionLeader(cmd)
	s := &termSession{pty: p, emu: emu, cmd: cmd, dir: dir, w: w, h: h}
	if err := p.Start(cmd); err != nil {
		_ = p.Close()
		_ = emu.Close()
		return nil, err
	}
	if cmd.Process != nil {
		s.pgid = cmd.Process.Pid // Setsid: el shell es líder de su grupo
	}
	go s.pump()
	return s, nil
}

// pump copia el input pipe del emulador (secuencias de teclas y resize)
// hacia el PTY. Corre hasta que el emulador se cierra (EOF).
//
// OJO: el mutex SOLO protege el flag closed/lifecycle, nunca se retiene
// a lo largo de llamadas que puedan bloquear (SendKey escribe al pipe
// y solo se desbloquea cuando pump lee; si sendKey retuviera el lock,
// pump y sendKey se deadlockearían).
func (s *termSession) pump() {
	buf := make([]byte, 4096)
	for {
		n, err := s.emu.Read(buf)
		if n > 0 && !s.isClosed() {
			_, _ = s.pty.Write(buf[:n]) // close concurrente → error benigno
		}
		if err != nil {
			return
		}
	}
}

// isClosed lee el flag de lifecycle bajo mutex.
func (s *termSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// sendKey codifica la tecla con el keymap del emulador (vt.SendKey:
// ctrl, alt, flechas, F-keys...) y el pump la entrega al PTY. El
// emulador solo se muta desde el hilo de Update; sin lock aquí.
func (s *termSession) sendKey(k tea.KeyPressMsg) {
	if s.isClosed() {
		return
	}
	s.emu.SendKey(vt.KeyPressEvent(k)) // tea.KeyPressMsg → uv.KeyPressEvent (structs idénticos)
}

// write vuelca bytes del PTY en el emulador (thread del Update).
func (s *termSession) write(data []byte) {
	if s.isClosed() {
		return
	}
	_, _ = s.emu.Write(data)
}

// resize redimensiona el emulador y el PTY (SIGWINCH al shell).
func (s *termSession) resize(w, h int) {
	if w < 1 || h < 1 || s.isClosed() {
		return
	}
	s.w, s.h = w, h // solo el hilo de Update toca w/h
	s.emu.Resize(w, h)
	_ = s.pty.Resize(w, h) // close concurrente → error benigno
}

// dims devuelve las dimensiones actuales del grid.
func (s *termSession) dims() (w, h int) {
	return s.w, s.h
}

// alive reporta si la sesión no ha sido apagada.
func (s *termSession) alive() bool {
	return !s.isClosed()
}

// label es el título corto de la sesión: basename del cwd fijo al crear.
func (s *termSession) label() string {
	return filepath.Base(s.dir)
}

// screen renderiza el grid del emulador con el cursor dibujado como
// bloque invertido (el Render del buffer no lo incluye).
func (s *termSession) screen() string {
	if s.isClosed() {
		return ""
	}
	out := s.emu.Render()
	pos := s.emu.CursorPosition()
	lines := strings.Split(out, "\n")
	if pos.Y >= 0 && pos.Y < len(lines) {
		line := lines[pos.Y]
		w := lipglossWidth(line)
		x := pos.X
		if x > w {
			x = w
		}
		var rest string
		if w > x {
			rest = ansi.TruncateLeft(line, x+1, "")
		}
		lines[pos.Y] = ansi.Truncate(line, x, "") + styleTermCursor.Render(" ") + rest
	}
	return strings.Join(lines, "\n")
}

// shutdown cierra el PTY (SIGHUP a la sesión) y remata el grupo con
// SIGKILL como backstop; idempotente.
func (s *termSession) shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	p, e, pgid := s.pty, s.emu, s.pgid
	s.mu.Unlock()
	if p != nil {
		_ = p.Close()
	}
	if e != nil {
		// Cerrar el input pipe desbloquea el Read del pump (EOF) sin
		// pasar por emu.Close(): su flag interno `closed` se escribe
		// sin lock y el pump lo consulta dentro de Read → data race
		// bajo -race. io.Pipe sí es concurrency-safe.
		if in, ok := e.InputPipe().(io.Closer); ok {
			_ = in.Close()
		}
	}
	killSessionGroup(pgid)
}

// wait espera al proceso del shell (xpty.WaitProcess: cmd.Wait en Unix,
// reaper en Windows) para reaper sin zombie.
func (s *termSession) wait() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil // sesión stub/sin proceso: nada que repear
	}
	return xpty.WaitProcess(context.Background(), s.cmd)
}

// ---- Comandos y mensajes del PTY ----

// ptyDataMsg transporta bytes leídos del PTY (output del shell).
type ptyDataMsg struct{ data []byte }

// ptyEOFMsg señala que el master del PTY no tiene más datos: el shell
// (o todos sus hijos con el slave abierto) terminó.
type ptyEOFMsg struct{}

// ptyExitMsg llega tras reaper el proceso: err es el resultado de wait.
type ptyExitMsg struct{ err error }

// readPtyCmd lee del PTY una vez (loop: cada ptyDataMsg re-arma) y
// señala EOF cuando el read no devuelve datos.
func readPtyCmd(s *termSession) tea.Cmd {
	return func() tea.Msg {
		buf := make([]byte, 8192)
		n, _ := s.pty.Read(buf)
		if n > 0 {
			return ptyDataMsg{data: append([]byte(nil), buf[:n]...)}
		}
		return ptyEOFMsg{}
	}
}

// waitCmd espera la salida del proceso fuera del hilo de Update.
func (s *termSession) waitCmd() tea.Cmd {
	return func() tea.Msg { return ptyExitMsg{err: s.wait()} }
}

// closeCmd apaga la sesión (para encadenar antes de tea.Quit).
func (s *termSession) closeCmd() tea.Cmd {
	return func() tea.Msg {
		s.shutdown()
		return nil
	}
}

// quitCmd sale de la TUI matando antes la sesión de terminal viva, si
// la hay: sin sesión es tea.Quit a secas.
func (m Model) quitCmd() tea.Cmd {
	if s := m.term; s != nil && s.alive() {
		return tea.Sequence(s.closeCmd(), tea.Quit)
	}
	return tea.Quit
}

// exitCode extrae el exit code de un error de wait (0 si ok).
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 0
}

// ---- Modal de terminal (Model) ----

// termW es el ancho del grid: el ancho interior del box tipo ask.
func (m Model) termW() int {
	w := askInnerW(m.width)
	if w < termMinW {
		w = termMinW
	}
	return w
}

// termH es el alto del grid: la pantalla menos título/hint/bordes.
func (m Model) termH() int {
	h := m.height - 8
	if h > termMaxH {
		h = termMaxH
	}
	if h < termMinH {
		h = termMinH
	}
	return h
}

// termCwdLabel es el cwd que usará la sesión: proyecto seleccionado o
// root del workspace.
func (m Model) termCwdLabel() string {
	if p := m.selected(); p != nil {
		return p.Name
	}
	return filepath.Base(m.root)
}

// termCwd es el directorio de trabajo de la sesión nueva.
func (m Model) termCwd() string {
	if p := m.selected(); p != nil {
		return p.Path
	}
	return m.root
}

// openTerm abre el modal de terminal (tecla !): si ya hay una
// sesión viva la re-muestra (y re-dimensiona si cambió el layout); si
// no, crea la sesión con cwd = proyecto seleccionado o root.
func (m Model) openTerm() (tea.Model, tea.Cmd) {
	w, h := m.termW(), m.termH()
	if s := m.term; s != nil && s.alive() {
		m.termOpen = true
		if cw, ch := s.dims(); cw != w || ch != h {
			s.resize(w, h)
		}
		return m, nil
	}
	s, err := newTermSession(w, h, m.termCwd())
	if err != nil {
		m.notify("terminal: " + err.Error())
		return m, nil
	}
	m.term = s
	m.termOpen = true
	m.clearMessage()
	// Loop de lectura + reaper del proceso desde el arranque: el master
	// del PTY NO emite EOF al morir el shell mientras
	// el propio pty sostiene el slave, así que la salida se detecta
	// por wait, no por EOF.
	return m, tea.Batch(readPtyCmd(s), s.waitCmd())
}

// termKey captura las teclas mientras el modal está abierto: todo va
// al PTY vía el keymap del emulador; solo ctrl+q es de
// vroom (oculta el modal, la sesión sigue viva).
func (m Model) termKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if kp.String() == "ctrl+q" {
		m.termOpen = false
		return m, nil
	}
	if s := m.term; s != nil {
		s.sendKey(kp)
	}
	return m, nil
}

// termBox renderiza el modal: título con el cwd de la sesión, el grid
// del emulador y el hint de ctrl+q.
func (m Model) termBox() string {
	innerW := m.termW()
	var label string
	if s := m.term; s != nil {
		label = s.label()
	} else {
		label = m.termCwdLabel()
	}
	lines := []string{
		styleTitle.Render(trunc("terminal — "+label, innerW)),
		"",
	}
	if s := m.term; s != nil && s.alive() {
		for _, l := range strings.Split(s.screen(), "\n") {
			lines = append(lines, padW(l, innerW))
		}
	} else {
		lines = append(lines, styleDim.Render("terminal closed"))
	}
	lines = append(lines, "", styleDim.Render("ctrl+q hide · session keeps running"))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}
