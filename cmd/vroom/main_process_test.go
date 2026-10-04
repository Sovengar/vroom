package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The child-process harness exists because main() calls os.Exit, which would kill the test binary; the child's coverage still lands in the parent profile because go test -cover exports GOCOVERDIR through the inherited env.
const vroomComoMarcaDeHijo = "VROOM_TEST_EJECUTAR_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(vroomComoMarcaDeHijo) == "1" {
		// Args travel in argv because that is what main() reads.
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The whole env is forwarded so GOCOVERDIR reaches the child untouched; a minimal env would silently drop its coverage.
func ejecutarMainComoHijo(t *testing.T, args ...string) (code int, stderr string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, args...)
	cmd.Env = append(os.Environ(), vroomComoMarcaDeHijo+"=1")
	var salidaErr strings.Builder
	cmd.Stderr = &salidaErr
	cmd.Stdout = os.Stderr // keeps child output visible for debugging without touching the asserted stream

	if err := cmd.Run(); err != nil {
		var salida *exec.ExitError
		if !errors.As(err, &salida) {
			t.Fatalf("no se pudo ejecutar el hijo %s: %v", self, err)
		}
		return salida.ExitCode(), salidaErr.String()
	}
	return 0, salidaErr.String()
}

// The exit code is read from a real process because both paths run the same lines until runMain answers; only the process death differs.
func TestMainDevuelveCeroConUnSubcomandoQueVaBien(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))

	code, _ := ejecutarMainComoHijo(t, "help")
	if code != 0 {
		t.Errorf("código de salida = %d con un subcomando que va bien, want 0", code)
	}
}

// No subcommand drops runMain into the TUI, and with no TTY behind it bubbletea cannot open the terminal: the failure main() must report as "vroom: <reason>" plus exit 1.
func TestMainSaleConUnoCuandoLaTUINoPuedeArrancar(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))

	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	code, stderr := ejecutarMainComoHijo(t)
	if code != 1 {
		t.Errorf("código de salida = %d sin TTY y sin subcomando, want 1: un `vroom` que no puede "+
			"arrancar su TUI tiene que parecer un fallo, no un éxito silencioso", code)
	}
	if !strings.Contains(stderr, "vroom:") {
		t.Errorf("stderr = %q, want que empiece por \"vroom:\": sin el prefijo y el motivo, el "+
			"usuario ve un fallo sin saber de quién es", stderr)
	}
	if !strings.Contains(stderr, "TTY") && !strings.Contains(stderr, "tty") {
		t.Errorf("stderr = %q, want que nombre el motivo del fallo —el terminal—: \"vroom: error\" "+
			"a secas no dice qué hacer", stderr)
	}
}
