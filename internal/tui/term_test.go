package tui

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"
)

// stubPty records the bytes the pump sends to the PTY plus every resize; Read returns immediate EOF (or the configured error).
type stubPty struct {
	mu      sync.Mutex
	written []byte
	resizes [][2]int
	closed  bool
}

func (p *stubPty) Read(b []byte) (int, error) { return 0, io.EOF }

func (p *stubPty) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.written = append(p.written, b...)
	return len(b), nil
}

func (p *stubPty) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *stubPty) Resize(w, h int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resizes = append(p.resizes, [2]int{w, h})
	return nil
}

func (p *stubPty) Start(_ *exec.Cmd) error { return nil }

func (p *stubPty) bytesWritten() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.written...)
}

func (p *stubPty) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// newStubSession wires a real emulator and a real pump to a stubbed PTY, so no process is ever spawned.
func newStubSession(w, h int, p *stubPty) *termSession {
	s := &termSession{pty: p, emu: vt.NewEmulator(w, h), w: w, h: h}
	go s.pump()
	return s
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func TestSendKeyEncoding(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyPressMsg
		want string
	}{
		{"printable", keyPress("a"), "a"},
		{"bang", keyPress("!"), "!"},
		{"enter", keyPress("enter"), "\r"},
		{"ctrl+c", tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}, "\x03"},
		{"ctrl+a", tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}, "\x01"},
		{"esc", keyPress("esc"), "\x1b"},
		{"arrow up", keyPress("up"), "\x1b[A"},
		{"arrow down", keyPress("down"), "\x1b[B"},
		{"pgup", keyPress("pgup"), "\x1b[5~"},
		{"backspace", keyPress("backspace"), "\x7f"},
		{"tab", keyPress("tab"), "\t"},
		{"alt+a", tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt}, "\x1ba"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &stubPty{}
			s := newStubSession(80, 10, p)
			defer s.shutdown()
			s.sendKey(tt.key)
			waitFor(t, time.Second, func() bool {
				return strings.Contains(string(p.bytesWritten()), tt.want)
			})
		})
	}
}

func TestSendKeyAfterShutdown(t *testing.T) {
	p := &stubPty{}
	s := newStubSession(80, 10, p)
	s.shutdown()
	s.shutdown()
	s.sendKey(keyPress("x"))
	if got := string(p.bytesWritten()); got != "" {
		t.Errorf("writes after shutdown: %q", got)
	}
	if !p.isClosed() {
		t.Error("the pty stub must be closed")
	}
}

func TestSessionResize(t *testing.T) {
	p := &stubPty{}
	s := newStubSession(80, 10, p)
	defer s.shutdown()
	s.resize(60, 8)
	if w, h := s.dims(); w != 60 || h != 8 {
		t.Errorf("dims = %d,%d, want 60,8", w, h)
	}
	if len(p.resizes) != 1 || p.resizes[0] != [2]int{60, 8} {
		t.Errorf("pty resizes = %v", p.resizes)
	}
}

// $SHELL is a script that sleeps 30s so the session is still alive when these assertions run.
func TestBangOpensTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires unix PTY")
	}
	sleepBin := fakeBin(t, "sleepy")
	if err := os.WriteFile(sleepBin+"/sleepy", []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", sleepBin+"/sleepy")

	m, store := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	wantDir := pathOfSelected(t, m)

	m2, cmd := press(m, "!")
	if cmd == nil {
		t.Fatal("! must set up the read loop (readPtyCmd)")
	}
	if !m2.termOpen || m2.term == nil {
		t.Fatal("! must open the modal and create the session")
	}
	if got := m2.term.cmd.Dir; got != wantDir {
		t.Errorf("session cwd = %q, want %q", got, wantDir)
	}
	if !m2.term.alive() {
		t.Error("the session must stay alive")
	}
	_ = store
	// Kill the session here or the real PTY child process outlives the test binary.
	if c := m2.term.closeCmd(); c != nil {
		c()
	}
	if m2.term.alive() {
		t.Error("closeCmd must shut down the session")
	}
}

func TestCtrlQHidesKeepsSession(t *testing.T) {
	p := &stubPty{}
	m, _ := newTestModel(t)
	m.term = newStubSession(80, 10, p)
	m.termOpen = true

	m2, _ := press(m, "ctrl+q")
	if m2.termOpen {
		t.Error("ctrl+q must hide the modal")
	}
	if m2.term == nil || !m2.term.alive() {
		t.Fatal("ctrl+q must NOT kill the session")
	}

	before := len(p.bytesWritten())
	m3, _ := press(m2, "!")
	if !m3.termOpen {
		t.Error("! must re-show the modal")
	}
	if m3.term != m2.term {
		t.Error("reopening must preserve the SAME session")
	}
	if got := string(p.bytesWritten()); len(got) != before {
		t.Errorf("reopening must not write to the PTY: %q", got)
	}
}

func TestTermModalCapturesKeys(t *testing.T) {
	p := &stubPty{}
	m, _ := newTestModel(t)
	m.term = newStubSession(80, 10, p)
	m.termOpen = true

	m2, cmd := press(m, "q")
	if cmd != nil {
		t.Error("q with terminal open must not emit quit")
	}
	if !m2.termOpen {
		t.Error("q with terminal open must not close the modal")
	}
	waitFor(t, time.Second, func() bool {
		return strings.Contains(string(p.bytesWritten()), "q")
	})

	press(m2, "!")
	waitFor(t, time.Second, func() bool {
		return strings.Contains(string(p.bytesWritten()), "!")
	})

	next, cmd2 := m2.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m3 := next.(Model)
	if cmd2 != nil {
		t.Error("ctrl+c with terminal open must not emit quit")
	}
	waitFor(t, time.Second, func() bool {
		return strings.Contains(string(p.bytesWritten()), "\x03")
	})
	_ = m3
}

func TestPtyDataFeedsEmulator(t *testing.T) {
	p := &stubPty{}
	m, _ := newTestModel(t)
	m.term = newStubSession(80, 10, p)
	m.termOpen = true

	next, cmd := m.Update(ptyDataMsg{data: []byte("hello term\r\n")})
	m2 := next.(Model)
	if cmd == nil {
		t.Error("ptyDataMsg must re-arm readPtyCmd")
	}
	out := m2.term.screen()
	if !strings.Contains(out, "hello term") {
		t.Errorf("screen = %q, want the received text", out)
	}
}

func TestPtyExitLifecycle(t *testing.T) {
	p := &stubPty{}
	m, _ := newTestModel(t)
	m.term = newStubSession(80, 10, p)
	m.termOpen = true

	next, cmd := m.Update(ptyEOFMsg{})
	m2 := next.(Model)
	if cmd != nil {
		t.Error("EOF must not arm anything: the reaper is already armed")
	}
	if m2.term == nil {
		t.Error("EOF must not clear the session (no reaper yet)")
	}

	next2, _ := m2.Update(ptyExitMsg{err: nil})
	m3 := next2.(Model)
	if m3.term != nil {
		t.Error("ptyExitMsg must clear the session (term = nil)")
	}
	if m3.termOpen {
		t.Error("ptyExitMsg must close the modal")
	}
	if !strings.Contains(m3.message, "terminal closed") {
		t.Errorf("message = %q, want close notification", m3.message)
	}
}

func TestPtyExitCodeNotify(t *testing.T) {
	m, _ := newTestModel(t)
	m.term = newStubSession(80, 10, &stubPty{})
	next, _ := m.Update(ptyExitMsg{err: &exec.ExitError{}})
	m2 := next.(Model)
	if !strings.Contains(m2.message, "terminal exited") {
		t.Errorf("message = %q, want 'terminal exited'", m2.message)
	}
}

// exitCode digs the status out of an *exec.ExitError; nil or any other error counts as 0.
func TestExitCodeHelper(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("exitCode(nil) = %d", got)
	}
}

func TestQuitCmdKillsSession(t *testing.T) {
	p := &stubPty{}
	m, _ := newTestModel(t)
	m.term = newStubSession(80, 10, p)

	cmd := m.quitCmd()
	if cmd == nil {
		t.Fatal("quitCmd must return a command")
	}
	// The Sequence must lead with closeCmd, not QuitMsg, because bubbletea runs the batch in order.
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); ok {
		t.Fatal("the first cmd of quit with session must NOT be QuitMsg")
	}
	m.term.closeCmd()()
	waitFor(t, time.Second, func() bool { return !m.term.alive() })
	if !p.isClosed() {
		t.Error("the pty stub must be closed after quit")
	}

	m2, _ := newTestModel(t)
	cmd2 := m2.quitCmd()
	if _, ok := cmd2().(tea.QuitMsg); !ok {
		t.Error("quitCmd without session must be direct QuitMsg")
	}
}

func TestTermBoxRender(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	m.termOpen = true
	box := m.termBox()
	if !strings.Contains(box, "terminal closed") {
		t.Errorf("box without session = %q, want placeholder", box)
	}

	p := &stubPty{}
	s := newStubSession(80, 8, p)
	s.dir = pathOfSelected(t, m)
	s.write([]byte("prompt$ \r\n"))
	m.term = s
	box = m.termBox()
	if !strings.Contains(box, "terminal — tienda-api") {
		t.Errorf("box without project title: %q", box)
	}
	if !strings.Contains(box, "ctrl+q hide") {
		t.Errorf("box without hint: %q", box)
	}
	if !strings.Contains(box, "prompt$") {
		t.Errorf("box without emulator content: %q", box)
	}
	s.shutdown()
}

// The one test that drives a real PTY process, so it covers the seam no stub reaches.
func TestTermSessionIntegration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires unix PTY")
	}
	s, err := startSession(80, 10, t.TempDir(), []string{"sh"})
	if err != nil {
		t.Skipf("could not open a PTY: %v", err)
	}
	defer s.shutdown()

	s.sendKey(keyPress("e"))
	s.sendKey(keyPress("c"))
	s.sendKey(keyPress("h"))
	s.sendKey(keyPress("o"))
	s.sendKey(keyPress(" "))
	s.sendKey(keyPress("m"))
	s.sendKey(keyPress("a"))
	s.sendKey(keyPress("r"))
	s.sendKey(keyPress("k"))
	s.sendKey(keyPress("4"))
	s.sendKey(keyPress("2"))
	s.sendKey(keyPress("enter"))

	deadline := time.After(5 * time.Second)
	found := false
	for !found {
		ch := make(chan tea.Msg, 1)
		go func() { ch <- readPtyCmd(s)() }()
		select {
		case msg := <-ch:
			switch msg := msg.(type) {
			case ptyDataMsg:
				s.write(msg.data)
				if strings.Contains(s.screen(), "mark42") {
					found = true
				}
			case ptyEOFMsg:
				t.Fatal("EOF before seeing the marker")
			default:
				t.Fatalf("unexpected msg: %T", msg)
			}
		case <-deadline:
			t.Fatalf("marker did not arrive; screen = %q", s.screen())
		}
	}

	// Only the reaper can see the shell exit: the PTY master never emits EOF while the slave side is held open.
	s.sendKey(keyPress("e"))
	s.sendKey(keyPress("x"))
	s.sendKey(keyPress("i"))
	s.sendKey(keyPress("t"))
	s.sendKey(keyPress("enter"))

	ch := make(chan tea.Msg, 1)
	go func() { ch <- s.waitCmd()() }()
	select {
	case msg := <-ch:
		if em, ok := msg.(ptyExitMsg); !ok {
			t.Fatalf("waitCmd produced %T", msg)
		} else if em.err != nil {
			t.Logf("wait err (informational): %v", em.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reaper did not detect shell exit")
	}
}
