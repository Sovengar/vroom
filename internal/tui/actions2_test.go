package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/portless"
	"vroom/internal/state"
)

func TestNewAvisaCuandoElEscaneoFalla(t *testing.T) {
	isolateConfig(t)
	// Scan on a nonexistent root errors instead of returning an empty list, which is what separates "no projects" from "could not look".
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte("[scanner]\nroot = \"/nonexistent-vroom-root-9d2f\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VROOM_CONFIG", cfg)

	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, "")
	if m.message == "" {
		t.Fatal("a failed scan must leave a warning: otherwise, the user sees an empty TUI " +
			"with no clue why")
	}
	if !strings.Contains(m.message, "scanning projects") {
		t.Errorf("message = %q, want it to name the scan", m.message)
	}
	if len(m.projects) != 0 {
		t.Errorf("there are %d projects after a failed scan, want 0", len(m.projects))
	}
}

// The error must surface unwrapped so the reader still gets the errno: it is a permissions or disk problem.
func TestRunLoggedFallaSiElLogDeSalidaNoSePuedeAbrir(t *testing.T) {
	dir := t.TempDir()
	// The dir is traversable, so MkdirAll passes and the log's OpenFile is the first real failure, like a logs dir owned by another user.
	roto := filepath.Join(dir, "logs")
	if err := os.MkdirAll(roto, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roto, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roto, 0o755) })

	_, code, err := runLogged("build", "echo hola", dir, filepath.Join(roto, "out.log"), "")
	if err == nil {
		t.Fatal("with the log lacking write permission the command cannot be launched: its output would be " +
			"lost entirely and the user would not see why")
	}
	if code != 0 {
		t.Errorf("exit code = %d with a launch failure, want 0: nothing got to execute", code)
	}
}

// MEASURED: /dev/full opens, writes and returns ENOSPC, so this branch is provoked for real instead of with an unreproducible permission.
func TestRunLoggedFallaSiElBannerNoSePuedeEscribir(t *testing.T) {
	const lleno = "/dev/full"
	if _, err := os.Stat(lleno); err != nil {
		t.Skipf("this machine does not have %s, and without it there is no way to open a log that accepts the "+
			"OpenFile and rejects writing", lleno)
	}

	_, code, err := runLogged("build", "echo hola", t.TempDir(), lleno, "")
	if err == nil {
		t.Fatal("with a log that does not accept writes the command cannot be launched")
	}
	if code != 0 {
		t.Errorf("exit code = %d with a write failure, want 0: nothing got to execute", code)
	}
}

func TestStopCmdPropagaElFalloDeQuitarElPid(t *testing.T) {
	m, store := newTestModel(t)
	p := firstConfiguredProject(t, m)

	// ClearPid uses os.Remove, so the pid path is made a non-empty directory: removal fails with ENOTEMPTY without relying on permissions the test user has.
	pid := store.PidFile(p.Path)
	if err := os.MkdirAll(filepath.Join(pid, "bloqueo"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := stopCmd(store, &stubManager{}, p.Path, p.Manifest.Stop)
	if cmd == nil {
		t.Fatal("stopCmd must return a command")
	}
	msg, ok := cmd().(stoppedMsg)
	if !ok {
		t.Fatalf("command returned %T, want stoppedMsg", cmd())
	}
	if msg.err == nil {
		t.Error("stoppedMsg.err = nil after a failed ClearPid: the saved pid is lying " +
			"about what is alive and the user does not find out")
	}
	if msg.path != p.Path {
		t.Errorf("path = %q, want %q: the message must say which service it refers to", msg.path, p.Path)
	}
}

// MEASURED: IsTestBinary keys off the os.Args[0] suffix, so overriding argv[0] reproduces the installed-binary entry without touching the function's contract.
func TestTuiRouteReleaserDevuelveNilFueraDeUnBinarioDeTest(t *testing.T) {
	original := os.Args[0]
	t.Cleanup(func() { os.Args[0] = original })

	os.Args[0] = "/usr/local/bin/vroom"
	if portless.IsTestBinary() {
		t.Fatal("IsTestBinary still says it is a test with a production argv: " +
			"this test would not test the real client branch")
	}
	if got := tuiRouteReleaser(); got != nil {
		t.Errorf("tuiRouteReleaser() = %v with a production binary, want nil: nil is what "+
			"tells portless to use the real client", got)
	}
}

// bubbletea also delivers repeats, releases and mouse events here, and forwarding a release would type it into the shell.
func TestTermKeyIgnoraUnMensajeDeTeclaQueNoEsUnaPulsacion(t *testing.T) {
	m, _ := newTestModel(t)
	m.term = &termSession{}
	m.termOpen = true

	nuevo, cmd := m.termKey(tea.KeyReleaseMsg{Code: 'a'})
	if cmd != nil {
		t.Error("a key release cannot return a command: the session is still alive and its loop " +
			"is already running")
	}
	got, ok := nuevo.(Model)
	if !ok {
		t.Fatalf("termKey returned %T, want Model", nuevo)
	}
	if got.termOpen != m.termOpen {
		t.Errorf("termOpen went from %v to %v: only ctrl+q closes the modal", m.termOpen, got.termOpen)
	}
	if s := got.term; s != m.term {
		t.Error("PTY session changed with a key that is not a press")
	}
}
