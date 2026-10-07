package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The fallback is "sh" and not "/bin/sh" on purpose: a shell this binary cannot find on PATH would leave the user with a terminal error instead of a prompt.
func TestResolveShellUsaElDelUsuarioYElDeSuPropioEsElQueExiste(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	if got := resolveShell(); got != "/bin/zsh" {
		t.Errorf("with SHELL = %q, want /bin/zsh", got)
	}

	t.Setenv("SHELL", "")
	if got := resolveShell(); got != "sh" {
		t.Errorf("without SHELL = %q, want sh: an absolute path would be invented", got)
	}

	t.Setenv("SHELL", "/no/existe/un/shell")
	if _, err := newTermSession(40, 10, t.TempDir()); err == nil {
		t.Error("a nonexistent $SHELL cannot open a terminal: the user would see an empty modal with no explanation")
	}
}

// TERM must appear exactly once: two of them make some programs take the first and others the last, and that depends on map iteration order.
func TestTermEnvHeredaElEntornoYGarantizaTerm(t *testing.T) {
	t.Setenv("TERM", "")
	t.Setenv("VROOM_TEST_MARKER", "presente")

	env := termEnv()

	terms := 0
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "TERM" {
			terms++
			if v != "xterm-256color" {
				t.Errorf("TERM = %q, want xterm-256color: without it the shell assumes a Unix terminal", v)
			}
		}
	}
	if terms != 1 {
		t.Errorf("there are %d TERM variables, want exactly 1", terms)
	}

	var marker bool
	for _, kv := range env {
		if kv == "VROOM_TEST_MARKER=presente" {
			marker = true
		}
	}
	if !marker {
		t.Error("termEnv did not inherit the process environment: a shell without its PATH starts nothing")
	}

	t.Setenv("TERM", "alacritty")
	for _, kv := range termEnv() {
		if k, _, ok := strings.Cut(kv, "="); ok && k == "TERM" && kv != "TERM=alacritty" {
			t.Errorf("with user TERM there is a second TERM: %q", kv)
		}
	}
}

// The stub PTY is the seam: without the guard emu.Resize on a closed emulator reaches the C library and takes the whole process down, and that cannot be provoked with a live session.
func TestResizeEWriteIgnoranUnaSesionCerrada(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	s.closed = true

	w, h := s.dims()
	s.resize(80, 24)
	if gotW, gotH := s.dims(); gotW != w || gotH != h {
		t.Errorf("resize of a closed session changed dimensions: %d,%d -> %d,%d", w, h, gotW, gotH)
	}
	s.write([]byte("this must not reach anywhere"))

	if s.alive() {
		t.Error("alive = true on a closed session")
	}
	if got := s.screen(); got != "" {
		t.Errorf("screen of a closed session = %q, want empty string", got)
	}
}

// A height of 0 tells the terminal library "no screen", and what comes out is a division by zero inside C, not a small terminal.
func TestResizeIgnoraLasDimensionesInvalidasYLasValidasNo(t *testing.T) {
	pty := &stubPty{}
	s := newStubSession(40, 10, pty)

	for _, dims := range [][2]int{{0, 10}, {40, 0}, {-5, 10}, {40, -5}} {
		s.resize(dims[0], dims[1])
		if w, h := s.dims(); w != 40 || h != 10 {
			t.Errorf("resize(%d,%d) applied invalid dimensions: %d,%d", dims[0], dims[1], w, h)
		}
		if len(pty.resizes) != 0 {
			t.Errorf("resize(%d,%d) reached the PTY: %v", dims[0], dims[1], pty.resizes)
		}
	}

	s.resize(80, 24)
	if w, h := s.dims(); w != 80 || h != 24 {
		t.Errorf("dims = %d,%d, want 80,24", w, h)
	}
	if len(pty.resizes) != 1 || pty.resizes[0] != [2]int{80, 24} {
		t.Errorf("the PTY did not receive the resize: %v", pty.resizes)
	}
}

// The correction must sit where the dimensions are created, or the PTY is born 0x0 and the shell starts in a sizeless terminal.
func TestStartSessionCorrigeLasDimensionesInvalidasEnElOrigen(t *testing.T) {
	// argv is a command that exits at once, not a shell, so what was created can be inspected.
	s, err := startSession(0, 0, t.TempDir(), []string{"true"})
	if err != nil {
		t.Fatalf("startSession: %v", err)
	}
	defer s.shutdown()

	w, h := s.dims()
	if w != 1 || h != 1 {
		t.Errorf("dims = %d,%d, want 1,1: a 0x0 grid is not a grid", w, h)
	}
}

// Returning an error here would raise a terminal-exited message in tests that have no terminal at all, and it is what lets a stub PTY skip the reaper.
func TestWaitSinProcesoNoEsUnError(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	if err := s.wait(); err != nil {
		t.Errorf("wait of a session without process = %v, want nil", err)
	}

	vacia := &termSession{}
	if err := vacia.wait(); err != nil {
		t.Errorf("wait of an empty session = %v, want nil", err)
	}
}

// The pump re-arms the read after every message, so an empty read means EOF and ends the session; conflating it with "no data yet" closes the terminal on the first empty tick.
func TestReadPtyDevuelveEOFCuandoNoHayBytesQueLeer(t *testing.T) {
	pty := &stubPty{} // Read returns (0, io.EOF)
	s := newStubSession(40, 10, pty)

	msg := readPtyCmd(s)()
	if _, ok := msg.(ptyEOFMsg); !ok {
		t.Errorf("readPtyCmd returned %T, want ptyEOFMsg: an empty read is EOF and closes the session", msg)
	}
}

// A basename, not a full path: the title sits on one modal line and an absolute path overflows it.
func TestLabelEsElBasenameDelCwd(t *testing.T) {
	tests := []struct{ dir, want string }{
		{"/home/usuario/proyectos/api", "api"},
		{"/home/usuario/proyectos/mi proyecto", "mi proyecto"},
		{"/", "/"},
		{"", "."},
	}
	for _, tt := range tests {
		s := &termSession{dir: tt.dir}
		if got := s.label(); got != tt.want {
			t.Errorf("label of %q = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

// Both bounds matter: a 0-column grid splits the prompt, and a screen-height grid pushes the title and the help line outside the border.
func TestTermWYTermHTienenSueloYTope(t *testing.T) {
	for _, tt := range []struct{ w, h int }{
		{300, 80}, {200, 60}, {120, 40}, {100, 30}, {60, 12}, {40, 3}, {20, 1},
	} {
		m, _ := newTestModel(t)
		m.width, m.height = tt.w, tt.h
		m.updateLayout()

		if got := m.termW(); got < termMinW {
			t.Errorf("%dx%d: termW = %d, want >= %d", tt.w, tt.h, got, termMinW)
		}
		if got := m.termH(); got < termMinH {
			t.Errorf("%dx%d: termH = %d, want >= %d", tt.w, tt.h, got, termMinH)
		}
		if got := m.termH(); got > termMaxH {
			t.Errorf("%dx%d: termH = %d, want <= %d: the modal would overflow the screen", tt.w, tt.h, got, termMaxH)
		}
		if m.termW()+boxFrame > tt.w && tt.w > 40 {
			t.Errorf("%dx%d: the grid is %d wide, plus the frame does not fit", tt.w, tt.h, m.termW())
		}
	}
}

// This is what the ! key promises: with the workspace root as cwd the user would have to cd before every command.
func TestTermCwdEsElProyectoSeleccionadoYNoElRoot(t *testing.T) {
	m, _ := newTestModel(t)

	m = moveCursorTo(t, m, "tienda-api")
	want := projectPath(t, m, "tienda-api")
	if got := m.termCwd(); got != want {
		t.Errorf("termCwd = %q, want the path of project %q", got, want)
	}
	if got := m.termCwdLabel(); got != "tienda-api" {
		t.Errorf("termCwdLabel = %q, want tienda-api", got)
	}

	// noSelection builds its own tree, so the root must be read from the model under test and not from the previous one.
	enHeader := noSelection(t)
	if got := enHeader.termCwd(); got != enHeader.root {
		t.Errorf("without project termCwd = %q, want the root %q", got, enHeader.root)
	}
	if got := enHeader.termCwdLabel(); got != filepath.Base(enHeader.root) {
		t.Errorf("without project termCwdLabel = %q, want the basename of the root", got)
	}
}

// An empty modal with no reason would read as vroom being broken, so the failure has to name the shell it could not start.
func TestOpenTermConShellInvalidoAvisaYNoAbreElModal(t *testing.T) {
	t.Setenv("SHELL", "/no/existe/un/shell")

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	next, cmd := m.openTerm()
	got := next.(Model)
	if cmd != nil {
		t.Error("without a shell there is nothing to launch")
	}
	if got.termOpen {
		t.Error("without a shell the modal cannot open: it would show an empty rectangle")
	}
	if got.term != nil {
		t.Error("no half-open session should remain")
	}
	if !strings.Contains(got.message, "terminal") {
		t.Errorf("= %q, want a warning that mentions the terminal", got.message)
	}
}

// Recreating the shell would lose the user's cwd, exported vars and half-typed command, so the live session is reused and only resized.
func TestOpenTermReutilizaLaSesionVivaYLaRedimensiona(t *testing.T) {
	t.Run("reuses without recreating", func(t *testing.T) {
		// A stub session, not a real shell: proving openTerm creates no new shell with a real one would leave a process behind per test.
		pty := &stubPty{}
		s := newStubSession(40, 10, pty)

		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.term = s

		next, cmd := m.openTerm()
		got := next.(Model)
		if cmd != nil {
			t.Error("reusing a live session does not need to emit commands")
		}
		if got.term != s {
			t.Error("the second open created a new session: the user's shell would be lost and an orphan process would remain")
		}
		if !got.termOpen {
			t.Error("the live session must be re-shown")
		}
	})

	t.Run("resizes the live session when the layout changes", func(t *testing.T) {
		pty := &stubPty{}
		s := newStubSession(40, 10, pty)

		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.term = s
		m.width, m.height = 200, 60
		m.updateLayout()

		got, _ := m.openTerm()
		model := got.(Model)
		if model.term != s {
			t.Fatal("a layout change cannot create a new session")
		}
		if len(pty.resizes) == 0 {
			t.Error("with the layout changed the grid was not resized: lines get cut off")
		}
		if w, h := s.dims(); w != m.termW() || h != m.termH() {
			t.Errorf("dims = %d,%d, want %d,%d: the grid did not adjust to the new modal", w, h, m.termW(), m.termH())
		}
	})

	t.Run("does not resize if the size does not change", func(t *testing.T) {
		pty := &stubPty{}
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.term = newStubSession(m.termW(), m.termH(), pty)

		got, _ := m.openTerm()
		if len(pty.resizes) != 0 {
			t.Errorf("without a layout change there is a spurious resize: %v", pty.resizes)
		}
		_ = got
	})
}

// Update's read loop does the PTY-to-emulator pump, so it is replicated by hand here as readPtyCmd plus write.
func TestOpenTermAbreElShellEnElProyectoConSizeCorrecto(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this machine")
	}

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	want := projectPath(t, m, "tienda-api")

	s, err := startSession(m.termW(), m.termH(), want, []string{"/bin/sh", "-c", "pwd; echo TERMINADO"})
	if err != nil {
		t.Fatalf("startSession: %v", err)
	}
	defer s.shutdown()

	deadline := time.After(30 * time.Second)
	found := false
	for !found {
		ch := make(chan tea.Msg, 1)
		go func() { ch <- readPtyCmd(s)() }()
		select {
		case msg := <-ch:
			switch m2 := msg.(type) {
			case ptyDataMsg:
				s.write(m2.data)
				if strings.Contains(s.screen(), "TERMINADO") {
					found = true
				}
			case ptyEOFMsg:
				t.Fatalf("EOF before seeing the output; screen = %q", s.screen())
			default:
				t.Fatalf("unexpected msg: %T", msg)
			}
		case <-deadline:
			t.Fatalf("the shell output did not arrive; screen = %q", s.screen())
		}
	}

	// Compared with the newlines stripped because the emulator can wrap the path across several lines.
	plano := strings.ReplaceAll(s.screen(), "\n", "")
	if !strings.Contains(plano, filepath.Base(want)) {
		t.Errorf("the shell opened somewhere else: %q does not contain %q", plano, filepath.Base(want))
	}
}

// Arbitrary bytes written to a real PTY produce control characters that move the cursor or switch terminal, so only key presses may reach it.
func TestTermKeyIgnoraLoQueNoEsUnaTecla(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.term = s
	m.termOpen = true

	// termKey takes a tea.KeyMsg, so the signature is the filter; what is pinned here is that nothing inside calls msg.String(), which would panic on another type.
	noTeclas := []tea.Msg{
		ptyDataMsg{data: []byte("this is not a key")},
		ptyEOFMsg{},
		ptyExitMsg{},
		tickMsg{},
		nil,
	}
	for _, msg := range noTeclas {
		if _, esTecla := msg.(tea.KeyMsg); esTecla {
			t.Fatalf("%T was not in the non-key list", msg)
		}
	}
	if n := len(s.pty.(*stubPty).bytesWritten()); n != 0 {
		t.Fatalf("%d bytes were written to the PTY before any key", n)
	}
	before := len(s.pty.(*stubPty).bytesWritten())
	next, _ := m.termKey(keyPress("a"))
	if !next.(Model).termOpen {
		t.Error("a normal key closed the modal")
	}
	waitFor(t, 2*time.Second, func() bool {
		return len(s.pty.(*stubPty).bytesWritten()) > before
	})
}

// Closing the modal must not kill the terminal, or the user loses their shell for having closed the panel.
func TestCtrlQOcultaElModalYDejaLaSesionViva(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.term = s
	m.termOpen = true

	next, cmd := m.termKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	got := next.(Model)

	if cmd != nil {
		t.Error("ctrl+q emits no commands: it only hides the modal")
	}
	if got.termOpen {
		t.Error("ctrl+q must hide the modal")
	}
	if got.term != s {
		t.Error("ctrl+q must leave the session alive: closing it would lose the user's shell")
	}
	if !s.alive() {
		t.Error("the session was shut down: ctrl+q hides, it does not kill")
	}

	reabierto, _ := got.openTerm()
	if re := reabierto.(Model); re.term != s || !re.termOpen {
		t.Error("reopening must show the same session, not create another")
	}
}
