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
		t.Fatal("un escaneo fallido tiene que dejar un aviso: si no, el usuario ve una TUI vacía " +
			"sin ninguna pista de por qué")
	}
	if !strings.Contains(m.message, "scanning projects") {
		t.Errorf("message = %q, want que nombre el escaneo", m.message)
	}
	if len(m.projects) != 0 {
		t.Errorf("hay %d proyectos tras un escaneo fallido, want 0", len(m.projects))
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
		t.Fatal("con el log sin permiso de escritura el comando no se puede lanzar: su salida se " +
			"perdería entera y el usuario no vería por qué")
	}
	if code != 0 {
		t.Errorf("exit code = %d con un fallo de lanzamiento, want 0: no llegó a ejecutarse nada", code)
	}
}

// MEDIDO: /dev/full opens, writes and returns ENOSPC, so this branch is provoked for real instead of with an unreproducible permission.
func TestRunLoggedFallaSiElBannerNoSePuedeEscribir(t *testing.T) {
	const lleno = "/dev/full"
	if _, err := os.Stat(lleno); err != nil {
		t.Skipf("esta máquina no tiene %s, y sin él no hay forma de abrir un log que acepte el "+
			"OpenFile y rechace la escritura", lleno)
	}

	_, code, err := runLogged("build", "echo hola", t.TempDir(), lleno, "")
	if err == nil {
		t.Fatal("con un log que no acepta escrituras el comando no se puede lanzar")
	}
	if code != 0 {
		t.Errorf("exit code = %d con un fallo de escritura, want 0: no llegó a ejecutarse nada", code)
	}
}

func TestStopCmdPropagaElFalloDeQuitarElPid(t *testing.T) {
	m, store := newTestModel(t)
	p := primerProyectoConfigurado(t, m)

	// ClearPid uses os.Remove, so the pid path is made a non-empty directory: removal fails with ENOTEMPTY without relying on permissions the test user has.
	pid := store.PidFile(p.Path)
	if err := os.MkdirAll(filepath.Join(pid, "bloqueo"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := stopCmd(store, &stubManager{}, p.Path, p.Manifest.Stop)
	if cmd == nil {
		t.Fatal("stopCmd tiene que devolver un comando")
	}
	msg, ok := cmd().(stoppedMsg)
	if !ok {
		t.Fatalf("el comando devolvió %T, want stoppedMsg", cmd())
	}
	if msg.err == nil {
		t.Error("stoppedMsg.err = nil tras un ClearPid fallido: el pid guardado queda mintiendo " +
			"sobre lo que está vivo y el usuario no se entera")
	}
	if msg.path != p.Path {
		t.Errorf("path = %q, want %q: el mensaje tiene que decir a qué servicio se refiere", msg.path, p.Path)
	}
}

// MEDIDO: IsTestBinary keys off the os.Args[0] suffix, so overriding argv[0] reproduces the installed-binary entry without touching the function's contract.
func TestTuiRouteReleaserDevuelveNilFueraDeUnBinarioDeTest(t *testing.T) {
	original := os.Args[0]
	t.Cleanup(func() { os.Args[0] = original })

	os.Args[0] = "/usr/local/bin/vroom"
	if portless.IsTestBinary() {
		t.Fatal("IsTestBinary sigue diciendo que es un test con un argv de producción: " +
			"este test no probaría la rama del cliente real")
	}
	if got := tuiRouteReleaser(); got != nil {
		t.Errorf("tuiRouteReleaser() = %v con un binario de producción, want nil: nil es lo que "+
			"le dice a portless que use el cliente real", got)
	}
}

// bubbletea also delivers repeats, releases and mouse events here, and forwarding a release would type it into the shell.
func TestTermKeyIgnoraUnMensajeDeTeclaQueNoEsUnaPulsacion(t *testing.T) {
	m, _ := newTestModel(t)
	m.term = &termSession{}
	m.termOpen = true

	nuevo, cmd := m.termKey(tea.KeyReleaseMsg{Code: 'a'})
	if cmd != nil {
		t.Error("una relajación de tecla no puede devolver comando: la sesión sigue viva y su bucle " +
			"de lectura ya está en marcha")
	}
	got, ok := nuevo.(Model)
	if !ok {
		t.Fatalf("termKey devolvió %T, want Model", nuevo)
	}
	if got.termOpen != m.termOpen {
		t.Errorf("termOpen pasó de %v a %v: sólo ctrl+q cierra el modal", m.termOpen, got.termOpen)
	}
	if s := got.term; s != m.term {
		t.Error("la sesión del PTY cambió con una tecla que no es una pulsación")
	}
}
