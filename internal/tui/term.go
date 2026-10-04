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

const (
	termMinW = 20
	termMinH = 6
	termMaxH = 40
)

// Own interface instead of xpty.Pty so tests can stub the PTY.
type ptyIface interface {
	io.ReadWriteCloser
	Resize(width, height int) error
	Start(cmd *exec.Cmd) error
}

// The VT emulator is not thread-safe: every mutation (pty output write, SendKey, Resize, Render) must stay on the Update goroutine.
type termSession struct {
	mu     sync.Mutex
	pty    ptyIface
	emu    *vt.Emulator
	cmd    *exec.Cmd
	dir    string
	pgid   int
	w, h   int
	closed bool
}

func resolveShell() string {
	if v := os.Getenv("SHELL"); v != "" {
		return v
	}
	return "sh"
}

// Measured bug: with TERM set-but-empty, os.Environ already carries TERM=, so appending ours yields two TERM entries and exec does not define which one wins.
func termEnv() []string {
	if os.Getenv("TERM") != "" {
		return os.Environ()
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if k, _, ok := strings.Cut(kv, "="); ok && k == "TERM" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "TERM=xterm-256color")
}

func newTermSession(w, h int, dir string) (*termSession, error) {
	return startSession(w, h, dir, []string{resolveShell()})
}

func startSession(w, h int, dir string, argv []string) (*termSession, error) {
	return startSessionWith(w, h, dir, argv, openRealPty)
}

// A parameter and not a global var: real PTY creation fails (fd exhaustion, devpts limit) and that is the only way to test the error path without breaking parallel tests.
func openRealPty(w, h int) (ptyIface, error) {
	return xpty.NewPty(w, h)
}

func startSessionWith(w, h int, dir string, argv []string, openPty func(int, int) (ptyIface, error)) (*termSession, error) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	p, err := openPty(w, h)
	if err != nil {
		return nil, err
	}
	emu := vt.NewEmulator(w, h)
	emu.SetScrollbackSize(1000)
	cmd := exec.Command(argv[0], argv[1:]...) // nolint:gosec // argv is deliberately the user's own shell
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
		s.pgid = cmd.Process.Pid // Setsid makes the shell a session leader, so its pgid equals its pid
	}
	go s.pump()
	return s, nil
}

// The mutex only guards the closed flag and is never held across a blocking call, or SendKey and this pump would deadlock on the input pipe.
func (s *termSession) pump() {
	buf := make([]byte, 4096)
	for {
		n, err := s.emu.Read(buf)
		if n > 0 && !s.isClosed() {
			_, _ = s.pty.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (s *termSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *termSession) sendKey(k tea.KeyPressMsg) {
	if s.isClosed() {
		return
	}
	s.emu.SendKey(vt.KeyPressEvent(k)) // tea.KeyPressMsg -> uv.KeyPressEvent (identical structs)
}

func (s *termSession) write(data []byte) {
	if s.isClosed() {
		return
	}
	_, _ = s.emu.Write(data)
}

func (s *termSession) resize(w, h int) {
	if w < 1 || h < 1 || s.isClosed() {
		return
	}
	s.w, s.h = w, h // only the Update goroutine touches w/h
	s.emu.Resize(w, h)
	_ = s.pty.Resize(w, h)
}

func (s *termSession) dims() (w, h int) {
	return s.w, s.h
}

func (s *termSession) alive() bool {
	return !s.isClosed()
}

func (s *termSession) label() string {
	return filepath.Base(s.dir)
}

// Render pads every line to the grid width so x never exceeds w; the min guards a disagreement, because ansi.Truncate would then cut past the line end and lose it.
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
		x := min(pos.X, w)
		var rest string
		if w > x {
			rest = ansi.TruncateLeft(line, x+1, "")
		}
		lines[pos.Y] = ansi.Truncate(line, x, "") + styleTermCursor.Render(" ") + rest
	}
	return strings.Join(lines, "\n")
}

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
		// Closing the input pipe unblocks the pump's Read without emu.Close, whose internal closed flag is written unlocked and read inside Read, a data race under -race.
		if in, ok := e.InputPipe().(io.Closer); ok {
			_ = in.Close()
		}
	}
	killSessionGroup(pgid)
}

// xpty.WaitProcess reaps the shell (cmd.Wait on Unix, a reaper on Windows) so a dead session leaves no zombie.
func (s *termSession) wait() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return xpty.WaitProcess(context.Background(), s.cmd)
}

type ptyDataMsg struct{ data []byte }

type ptyEOFMsg struct{}

type ptyExitMsg struct{ err error }

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

func (s *termSession) waitCmd() tea.Cmd {
	return func() tea.Msg { return ptyExitMsg{err: s.wait()} }
}

// Must be chained before tea.Quit, so the PTY is still closable while the program can act.
func (s *termSession) closeCmd() tea.Cmd {
	return func() tea.Msg {
		s.shutdown()
		return nil
	}
}

func (m Model) quitCmd() tea.Cmd {
	if s := m.term; s != nil && s.alive() {
		return tea.Sequence(s.closeCmd(), tea.Quit)
	}
	return tea.Quit
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 0
}

func (m Model) termW() int {
	// The floor is this modal's own decision, not a consequence of askInnerW's, so the grid stays usable if ask's floor ever drops.
	return max(askInnerW(m.width), termMinW)
}

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

func (m Model) termCwdLabel() string {
	if p := m.selected(); p != nil {
		return p.Name
	}
	return filepath.Base(m.root)
}

func (m Model) termCwd() string {
	if p := m.selected(); p != nil {
		return p.Path
	}
	return m.root
}

// An already-live session is only re-shown and resized, never restarted: hiding the modal must not kill the user's shell.
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
	// The PTY master never emits EOF while the pty itself holds the slave open, so shell exit is detected with wait, not EOF.
	return m, tea.Batch(readPtyCmd(s), s.waitCmd())
}

// ctrl+q is the only vroom key: it hides the modal and leaves the session running.
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
