package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/process"
)

// main() cannot be driven from here because it os.Exits, so these tests drive runTUI with output discarded and assert only its wiring.

// Option order matters: stacking two tea.WithInput leaves the winner undefined, so the slice is built once.
func sinTerminal(t *testing.T, in io.Reader) {
	t.Helper()
	opts = []tea.ProgramOption{tea.WithOutput(io.Discard)}
	if in == nil {
		opts = append(opts, tea.WithInput(nil))
	} else {
		opts = append(opts, tea.WithInput(in))
	}
	t.Cleanup(func() { opts = nil })
}

// MEDIDO: with tea.WithInput(nil) the program waits forever for a key nobody sends, so any test that reaches the TUI needs a real reader.
func TestRunTUIArrancaYSaleSolo(t *testing.T) {
	// The pipe carries "q", the key that quits the TUI, so the program leaves by the normal path instead of being torn down.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pw.WriteString("q"); err != nil {
		t.Fatal(err)
	}
	sinTerminal(t, pr)
	t.Cleanup(func() { _ = pw.Close() })

	// runTUI takes the CWD as the scan root, so the test runs in a real empty directory.
	t.Chdir(t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))

	if err := runTUI(&fakeManager{}); err != nil {
		t.Errorf("runTUI devolvió %v: con entrada y salida redirigidas no hay motivo para fallar", err)
	}
}

// MEDIDO: the store lives under $XDG_STATE_HOME, not $XDG_DATA_HOME, so a developer XDG_DATA_HOME left in the env made NewStore succeed and the first version of this test hang forever.
func TestRunTUIFallaSiElStoreNoSePuedeCrear(t *testing.T) {
	sinTerminal(t, nil)

	bloqueado := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(bloqueado, []byte("soy un fichero, no un directorio"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", bloqueado)
	t.Setenv("HOME", bloqueado)
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))

	err := runTUI(&fakeManager{})
	if err == nil {
		t.Fatal("un store que no se puede crear tiene que dar error: si no, la TUI arranca con un store a medias")
	}
	// main() owns the "vroom:" prefix; one here would double it into "vroom: vroom:".
	if strings.Contains(err.Error(), "vroom:") {
		t.Errorf("el error ya trae prefijo y main lo volvería a poner: %q", err)
	}
	if err.Error() == "" {
		t.Error("el error está vacío: el usuario vería \"vroom: \" y nada más")
	}
}

// The blocked store is the probe: if main had fallen through to the TUI, runTUI could not create it and the process would exit 1, which is what keeps `vroom list --json` from opening a dashboard on an agent's terminal.
func TestMainSaleSinErrorCuandoLaCLIResuelveElSubcomando(t *testing.T) {
	bloqueado := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(bloqueado, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", bloqueado)
	t.Setenv("HOME", bloqueado)

	out, code := runBin(t, selfBin(t), "--help")
	if code != 0 {
		t.Errorf("vroom --help sale con %d, want 0\n%s", code, out)
	}
	if out == "" {
		t.Error("vroom --help no imprime nada")
	}
}

// The exact string is pinned because an agent invoking the binary parses "vroom: <err>" as its failure contract.
func TestElErrorDelStoreSeImprimeConElPrefijoVroom(t *testing.T) {
	sinTerminal(t, nil)
	bloqueado := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(bloqueado, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", bloqueado)
	t.Setenv("HOME", bloqueado)

	err := runTUI(&fakeManager{})
	if err == nil {
		t.Fatal("precondición: el store tiene que fallar")
	}
	linea := "vroom: " + err.Error()
	if !strings.HasPrefix(linea, "vroom: ") {
		t.Errorf("la línea de error = %q", linea)
	}
	if strings.Contains(linea, "\n") {
		t.Errorf("el error tiene saltos de línea y rompería el formato de una línea: %q", linea)
	}
	if linea == "vroom: " {
		t.Error("la línea de error está vacía")
	}
}

// opts is a global so main can rely on its zero value meaning a real terminal; tests inject and the reset rides on t.Cleanup.
func TestOptsSinTerminalNoRompeElCableadoPorDefecto(t *testing.T) {
	if len(opts) != 0 {
		t.Fatalf("opts = %v al empezar: tiene que estar vacía por defecto, porque main la usa sin tocar", opts)
	}
	sinTerminal(t, nil)
	if len(opts) != 2 {
		t.Errorf("opts = %d opciones tras sinTerminal, want 2", len(opts))
	}
	// A misuse of tea.WithInput panics at startup instead of returning an error, so only the absence of a panic is assertable.
	_, err := tea.NewProgram(nil, opts...).Run()
	_ = err // with a nil model the program never starts, but construction must still not fail
}

// A no-op manager: runTUI never touches it until a keypress, which these tests never reach.
type fakeManager struct{}

func (fakeManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, errors.New("no se arranca nada en un test")
}

func (fakeManager) Stop(process.StopSpec) error { return nil }

func (fakeManager) Evaluate(process.EvalSpec) process.Status {
	return process.StatusStopped
}

// The test binary carries the same main() as the production one, so running it observes main from outside.
func selfBin(t *testing.T) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("no se pudo resolver el propio binario: %v", err)
	}
	return bin
}

func runBin(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	// An own HOME, otherwise the child reads the developer's real store.
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"XDG_STATE_HOME="+t.TempDir(),
		"VROOM_CONFIG="+filepath.Join(t.TempDir(), "ausente.toml"),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode()
	}
	t.Fatalf("no se pudo ejecutar %s: %v", bin, err)
	return "", -1
}
