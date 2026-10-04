package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// `main()` de verdad, en un proceso de verdad.
//
// `main()` llama a `os.Exit`, y un `os.Exit` dentro del proceso de test lo mata sin
// que se pueda mirar nada. La única forma honesta de ejecutarlo es en otro proceso:
// el propio binario de test se re-ejecuta, y esta vez `main()` corre para verdad.
//
// La cobertura no se pierde por el camino: `go test -cover` compila el binario de
// test instrumentado y le pone `GOCOVERDIR`, que el hijo hereda del entorno, así
// que sus contadores acaban en el mismo perfil que los del padre. Eso es lo que
// permite afirmar que `main()` está cubierta en vez de suponerlo.
// ---------------------------------------------------------------------------

// vroomComoMarcaDeHijo es lo que el proceso hijo lee para saber que debe
// ejecutar `main()` en vez de la suite.
const vroomComoMarcaDeHijo = "VROOM_TEST_EJECUTAR_MAIN"

// TestMain es el punto de entrada del binario de test, y por eso es el sitio donde
// se decide qué hace este proceso: suite normal, o `main()` de vroom.
//
// Sin esto habría que llamar a `main()` desde un test, y el `os.Exit` de su rama de
// error se comería al resto de la suite.
func TestMain(m *testing.M) {
	if os.Getenv(vroomComoMarcaDeHijo) == "1" {
		// El hijo no ejecuta tests: ejecuta el programa. Sus argumentos vienen en
		// argv porque es lo que `main()` lee (`os.Args[1:]`).
		main()
		// `main()` sólo vuelve si todo fue bien; llegar aquí significa código 0.
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// ejecutarMainComoHijo lanza este mismo binario de test como proceso hijo y
// devuelve su código de salida y su stderr.
//
// Se pasa el entorno entero y sólo se le añade la marca: `GOCOVERDIR` tiene que
// llegar al hijo tal cual, o su cobertura se perdería.
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
	cmd.Stdout = os.Stderr // la salida del hijo no es lo que se comprueba aquí

	if err := cmd.Run(); err != nil {
		var salida *exec.ExitError
		if !errors.As(err, &salida) {
			t.Fatalf("no se pudo ejecutar el hijo %s: %v", self, err)
		}
		return salida.ExitCode(), salidaErr.String()
	}
	return 0, salidaErr.String()
}

// TestMainDevuelveCeroConUnSubcomandoQueVaBien: la rama que no mata el proceso.
//
// El caso normal: un subcomando que funciona. `main()` llega al final sin error y
// el proceso sale con 0.
//
// Y se comprueba desde FUERA a propósito —con el código de salida real del proceso
// real— porque es lo único que distingue este camino del otro: los dos ejecutan las
// mismas líneas hasta que `runMain` responde, y lo que cambia es si el proceso muere
// con 1.
func TestMainDevuelveCeroConUnSubcomandoQueVaBien(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))

	code, _ := ejecutarMainComoHijo(t, "help")
	if code != 0 {
		t.Errorf("código de salida = %d con un subcomando que va bien, want 0", code)
	}
}

// TestMainSaleConUnoCuandoLaTUINoPuedeArrancar: la rama que mata el proceso.
//
// Sin subcomando, `runMain` cae en la TUI. Sin TTY detrás, bubbletea no puede abrir
// el terminal y el arranque falla: ese es el caso que `main()` tiene que convertir
// en "vroom: <motivo>" por stderr y código de salida 1.
//
// Lo que se comprueba son las dos mitades del contrato de un proceso que falla: el
// CÓDIGO, porque un script decide con él, y el MENSAJE, porque es lo único que ve un
// usuario que lanzó vroom en un contexto sin terminal.
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
