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

// stubPty graba los writes (lo que el pump envía al PTY) y los
// resizes; Read devuelve EOF inmediato (o el error configurado).
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

// newStubSession arma una termSession con PTY stub: emulador real +
// pump real, sin procesos.
func newStubSession(w, h int, p *stubPty) *termSession {
	s := &termSession{pty: p, emu: vt.NewEmulator(w, h), w: w, h: h}
	go s.pump()
	return s
}

// waitFor poll-ea cond hasta el deadline; falla el test si no llega.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condición no alcanzada antes del deadline")
}

// keyPress construye el KeyPressMsg de una tecla imprimible o especial.
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

// ---- Codificación de teclas ----

// Las teclas llegan al PTY con su secuencia ANSI correcta: el keymap
// del emulador (vt.SendKey) codifica ctrl/alt/flechas/especiales.
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

// shutdown tras cerrar: sendKey no escribe ni panickea (idempotencia).
func TestSendKeyAfterShutdown(t *testing.T) {
	p := &stubPty{}
	s := newStubSession(80, 10, p)
	s.shutdown()
	s.shutdown() // idempotente
	s.sendKey(keyPress("x"))
	if got := string(p.bytesWritten()); got != "" {
		t.Errorf("writes tras shutdown: %q", got)
	}
	if !p.isClosed() {
		t.Error("el pty stub debe quedar cerrado")
	}
}

// resize redimensiona emulador y PTY; con dims nuevas tras un cambio
// de ventana.
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

// ---- Ciclo de vida en el Model ----

// "!" abre el modal y crea la sesión con cwd = proyecto seleccionado;
// el shell es $SHELL (aquí un script lento) y el loop de lectura queda
// armado.
func TestBangOpensTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requiere PTY unix")
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
		t.Fatal("! debe armar el loop de lectura (readPtyCmd)")
	}
	if !m2.termOpen || m2.term == nil {
		t.Fatal("! debe abrir el modal y crear la sesión")
	}
	if got := m2.term.cmd.Dir; got != wantDir {
		t.Errorf("cwd de la sesión = %q, want %q", got, wantDir)
	}
	if !m2.term.alive() {
		t.Error("la sesión debe quedar viva")
	}
	_ = store
	// Cleanup: matar la sesión para no dejar procesos colgados.
	if c := m2.term.closeCmd(); c != nil {
		c()
	}
	if m2.term.alive() {
		t.Error("closeCmd debe apagar la sesión")
	}
}

// ctrl+q oculta el modal SIN matar la sesión; "!" la re-muestra sin
// crear otra.
func TestCtrlQHidesKeepsSession(t *testing.T) {
	p := &stubPty{}
	m, _ := newTestModel(t)
	m.term = newStubSession(80, 10, p)
	m.termOpen = true

	m2, _ := press(m, "ctrl+q")
	if m2.termOpen {
		t.Error("ctrl+q debe ocultar el modal")
	}
	if m2.term == nil || !m2.term.alive() {
		t.Fatal("ctrl+q NO debe matar la sesión")
	}

	// Reabrir: misma sesión (no se crea otra) y el stub no recibió
	// un segundo Start.
	before := len(p.bytesWritten())
	m3, _ := press(m2, "!")
	if !m3.termOpen {
		t.Error("! debe re-mostrar el modal")
	}
	if m3.term != m2.term {
		t.Error("reabrir debe conservar la MISMA sesión")
	}
	if got := string(p.bytesWritten()); len(got) != before {
		t.Errorf("reabrir no debe escribir al PTY: %q", got)
	}
}

// Con el modal abierto las teclas van al shell: "q" NO sale de la TUI
// y "!" escribe el bang.
func TestTermModalCapturesKeys(t *testing.T) {
	p := &stubPty{}
	m, _ := newTestModel(t)
	m.term = newStubSession(80, 10, p)
	m.termOpen = true

	m2, cmd := press(m, "q")
	if cmd != nil {
		t.Error("q con terminal abierta no debe emitir quit")
	}
	if !m2.termOpen {
		t.Error("q con terminal abierta no debe cerrar el modal")
	}
	waitFor(t, time.Second, func() bool {
		return strings.Contains(string(p.bytesWritten()), "q")
	})

	press(m2, "!")
	waitFor(t, time.Second, func() bool {
		return strings.Contains(string(p.bytesWritten()), "!")
	})

	// ctrl+c va al shell (SIGINT), no sale de vroom.
	next, cmd2 := m2.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	m3 := next.(Model)
	if cmd2 != nil {
		t.Error("ctrl+c con terminal abierta no debe emitir quit")
	}
	waitFor(t, time.Second, func() bool {
		return strings.Contains(string(p.bytesWritten()), "\x03")
	})
	_ = m3
}

// ptyDataMsg alimenta el emulador y re-arma el loop de lectura.
func TestPtyDataFeedsEmulator(t *testing.T) {
	p := &stubPty{}
	m, _ := newTestModel(t)
	m.term = newStubSession(80, 10, p)
	m.termOpen = true

	next, cmd := m.Update(ptyDataMsg{data: []byte("hello term\r\n")})
	m2 := next.(Model)
	if cmd == nil {
		t.Error("ptyDataMsg debe re-armar readPtyCmd")
	}
	out := m2.term.screen()
	if !strings.Contains(out, "hello term") {
		t.Errorf("screen = %q, want el texto recibido", out)
	}
}

// EOF del PTY no arma nada (el reaper se armó al abrir la sesión) y
// ptyExitMsg limpia la sesión con aviso y modal cerrado.
func TestPtyExitLifecycle(t *testing.T) {
	p := &stubPty{}
	m, _ := newTestModel(t)
	m.term = newStubSession(80, 10, p)
	m.termOpen = true

	next, cmd := m.Update(ptyEOFMsg{})
	m2 := next.(Model)
	if cmd != nil {
		t.Error("EOF no debe armar nada: el reaper ya está armado")
	}
	if m2.term == nil {
		t.Error("EOF no debe limpiar la sesión (aún no hay reaper)")
	}

	next2, _ := m2.Update(ptyExitMsg{err: nil})
	m3 := next2.(Model)
	if m3.term != nil {
		t.Error("ptyExitMsg debe limpiar la sesión (term = nil)")
	}
	if m3.termOpen {
		t.Error("ptyExitMsg debe cerrar el modal")
	}
	if !strings.Contains(m3.message, "terminal closed") {
		t.Errorf("message = %q, want aviso de cierre", m3.message)
	}
}

// Exit code != 0 se notifica.
func TestPtyExitCodeNotify(t *testing.T) {
	m, _ := newTestModel(t)
	m.term = newStubSession(80, 10, &stubPty{})
	next, _ := m.Update(ptyExitMsg{err: &exec.ExitError{}})
	m2 := next.(Model)
	if !strings.Contains(m2.message, "terminal exited") {
		t.Errorf("message = %q, want 'terminal exited'", m2.message)
	}
}

// exitCode extrae el código de un ExitError; otros errores → 0.
func TestExitCodeHelper(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Errorf("exitCode(nil) = %d", got)
	}
}

// Con sesión viva, q encadena el shutdown antes de salir.
func TestQuitCmdKillsSession(t *testing.T) {
	p := &stubPty{}
	m, _ := newTestModel(t)
	m.term = newStubSession(80, 10, p)

	cmd := m.quitCmd()
	if cmd == nil {
		t.Fatal("quitCmd debe devolver un comando")
	}
	// Con sesión el primer cmd del Sequence NO es QuitMsg (es el
	// closeCmd; el runtime de bubbletea ejecuta la secuencia).
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); ok {
		t.Fatal("el primer cmd del quit con sesión NO debe ser QuitMsg")
	}
	// El close del sequence es closeCmd: al invocarlo se apaga todo.
	m.term.closeCmd()()
	waitFor(t, time.Second, func() bool { return !m.term.alive() })
	if !p.isClosed() {
		t.Error("el pty stub debe quedar cerrado tras el quit")
	}

	// Sin sesión: quitCmd es tea.Quit a secas.
	m2, _ := newTestModel(t)
	cmd2 := m2.quitCmd()
	if _, ok := cmd2().(tea.QuitMsg); !ok {
		t.Error("quitCmd sin sesión debe ser QuitMsg directo")
	}
}

// El box muestra título con el cwd de la sesión y el hint; sin sesión,
// placeholder "terminal closed".
func TestTermBoxRender(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	m.termOpen = true // sin sesión aún
	box := m.termBox()
	if !strings.Contains(box, "terminal closed") {
		t.Errorf("box sin sesión = %q, want placeholder", box)
	}

	p := &stubPty{}
	s := newStubSession(80, 8, p)
	s.dir = pathOfSelected(t, m)
	s.write([]byte("prompt$ \r\n"))
	m.term = s
	box = m.termBox()
	if !strings.Contains(box, "terminal — tienda-api") {
		t.Errorf("box sin título de proyecto: %q", box)
	}
	if !strings.Contains(box, "ctrl+q hide") {
		t.Errorf("box sin hint: %q", box)
	}
	if !strings.Contains(box, "prompt$") {
		t.Errorf("box sin contenido del emulador: %q", box)
	}
	s.shutdown()
}

// ---- Integración con PTY real (unix) ----

// Sesión real: sh corriendo en un PTY; el echo del shell llega al
// emulador y el exit cierra el ciclo completo.
func TestTermSessionIntegration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requiere PTY unix")
	}
	s, err := startSession(80, 10, t.TempDir(), []string{"sh"})
	if err != nil {
		t.Skipf("no se pudo abrir un PTY: %v", err)
	}
	defer s.shutdown()

	// Escribir un echo marcador y esperar a que el emulador lo pinte.
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

	// Bombea el output del PTY al emulador hasta ver el marcador.
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
				t.Fatal("EOF antes de ver el marcador")
			default:
				t.Fatalf("msg inesperado: %T", msg)
			}
		case <-deadline:
			t.Fatalf("marcador no llegó; screen = %q", s.screen())
		}
	}

	// exit + enter: el reaper (waitCmd) detecta la salida del shell
	// (el master NO emite EOF mientras el pty sostiene el slave).
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
			t.Fatalf("waitCmd produjo %T", msg)
		} else if em.err != nil {
			t.Logf("wait err (informativo): %v", em.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("el reaper no detectó la salida del shell")
	}
}
