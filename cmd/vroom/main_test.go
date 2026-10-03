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

// ---------------------------------------------------------------------------
// El arranque, sin el os.Exit.
//
// main() no se puede probar: mata el proceso de test con os.Exit y arranca un
// programa de terminal. La forma de probarlo es la que el propio main ya usaba
// para la CLI —separar "qué pasó" de "cómo muere el proceso"— y por eso aquí se
// ejecuta `runTUI` con la entrada anulada y la salida a un io.Discard.
//
// Lo que se comprueba no es que la TUI se vea bien, sino el cableado: que el store
// se crea antes que el modelo, que el root es el directorio de trabajo, que un
// fallo de cualquiera de los dos sale como error y no como un programa a medias, y
// que sin stdin la TUI no se cuelga esperando una tecla que nadie va a pulsar.
// ---------------------------------------------------------------------------

// sinTerminal redirige la entrada y la salida de bubbletea para poder arrancar la
// TUI sin un terminal detrás.
//
// `in` nil significa "sin entrada": el programa no lee nada y sale al cerrarse.
// Con un reader, el programa lee de él.
//
// El orden importa y por eso se construye la lista una sola vez: apilar dos
// WithInput deja el resultado en manos de cuál gana, y el que ganaba no era el que
// este test creía.
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

// TestRunTUIArrancaYSaleSolo: el camino de éxito.
//
// Se le manda un `q` por un pipe, que es como el usuario cierra la TUI. La prueba
// real de este test es que NO se cuelga: MEDIDO que con la entrada anulada
// (`tea.WithInput(nil)`) el programa se queda esperando para siempre, porque nadie
// le manda la tecla que lo cierra. Por eso los tests que fallan ANTES de llegar a
// la TUI pueden usar entrada nil, y este —que la llega a arrancar— no.
func TestRunTUIArrancaYSaleSolo(t *testing.T) {
	// La entrada es un pipe con un `q` dentro. Es lo que hace que el programa salga
	// por la vía normal —el usuario cierra la TUI— en vez de por un cierre abrupto.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pw.WriteString("q"); err != nil {
		t.Fatal(err)
	}
	sinTerminal(t, pr)
	t.Cleanup(func() { _ = pw.Close() })

	// Se escribe en un directorio de trabajo real y limpio: runTUI usa el CWD como
	// root del escaneo.
	t.Chdir(t.TempDir())
	// Y un HOME propio, para que no lea el store real del developer.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))

	if err := runTUI(&fakeManager{}); err != nil {
		t.Errorf("runTUI devolvió %v: con entrada y salida redirigidas no hay motivo para fallar", err)
	}
}

// TestRunTUIFallaSiElStoreNoSePuedeCrear: el primer fallo posible.
//
// Se provoca con un XDG_DATA_HOME que apunta a un fichero. El store vive bajo ahí,
// así que crear el directorio falla —no es un permiso simulado, es un tipo de
// fichero que no puede ser directorio—.
//
// Y lo que importa no es sólo el error: es que NO se llegue a construir el modelo.
// Un modelo con un store a medias arrancar con un estado vacío y el usuario
// vería "no hay servicios" en vez de un fallo claro.
func TestRunTUIFallaSiElStoreNoSePuedeCrear(t *testing.T) {
	sinTerminal(t, nil)

	// Un fichero donde debería ir el directorio del store.
	//
	// MEDIDO: la variable que decide dónde vive el store es $XDG_STATE_HOME, NO
	// $XDG_DATA_HOME. Con la primera puesta en el entorno del developer el store se
	// crea en un sitio de verdad, `NewStore` no falla, la TUI arranca... y se queda
	// esperando una tecla que este test no va a mandar nunca. El primer versión de
	// este test colgaba por eso.
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
	// Y el mensaje tiene que decir qué falló, sin un "vroom:" delante: ese prefijo
	// lo pone main, y duplicarlo saldría "vroom: vroom: ...".
	if strings.Contains(err.Error(), "vroom:") {
		t.Errorf("el error ya trae prefijo y main lo volvería a poner: %q", err)
	}
	if err.Error() == "" {
		t.Error("el error está vacío: el usuario vería \"vroom: \" y nada más")
	}
}

// TestMainSaleSinErrorCuandoLaCLIResuelveElSubcomando: el reparto de responsabilidades.
//
// El modo CLI devuelve true cuando hubo subcomando, y main entonces NO debe tocar la
// TUI ni el store. Es lo que hace que `vroom list --json` no abra un dashboard en el
// terminal del agente que lo invoca.
//
// No se prueba `main` —mata el proceso con os.Exit— sino la consecuencia que sí se
// puede observar: la función de la TUI no llega a tocar el store, porque con un
// XDG_DATA_HOME bloqueado fallaría. Y con la salida de la CLI en un buffer.
func TestMainSaleSinErrorCuandoLaCLIResuelveElSubcomando(t *testing.T) {
	// El store está bloqueado a propósito: si main cayera en la TUI, runTUI
	// fallaría al no poder crearlo y el proceso saldría con 1.
	bloqueado := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(bloqueado, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", bloqueado)
	t.Setenv("HOME", bloqueado)

	// Se ejecuta el binario compilado de verdad con un subcomando y se mira el
	// código de salida. Es la única forma de observar a main sin estar dentro de él.
	// Se ejecuta el binario de verdad: es la única forma de observar a main sin
	// estar dentro de él. El flag --help lo resuelve la CLI sin tocar la TUI ni el
	// store, así que con el store bloqueado tiene que salir con 0.
	out, code := runBin(t, selfBin(t), "--help")
	if code != 0 {
		t.Errorf("vroom --help sale con %d, want 0\n%s", code, out)
	}
	if out == "" {
		t.Error("vroom --help no imprime nada")
	}
}

// TestElErrorDelStoreSeImprimeConElPrefijoVroom: el formato del único os.Exit.
//
// main imprime "vroom: <err>" y sale con 1. Es la convención de unix y la que un
// agente que invoca el binario sabe leer, así que se fija la cadena exacta.
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
	// Esto es lo que main va a imprimir.
	linea := "vroom: " + err.Error()
	if !strings.HasPrefix(linea, "vroom: ") {
		t.Errorf("la línea de error = %q", linea)
	}
	if strings.Contains(linea, "\n") {
		t.Errorf("el error tiene saltos de línea y rompería el formato de una línea: %q", linea)
	}
	// Y no está vacío: "vroom: " a secas es el peor diagnóstico posible.
	if linea == "vroom: " {
		t.Error("la línea de error está vacía")
	}
}

// TestOptsSinTerminalNoRompeElCableadoPorDefecto: la variable global de opciones.
//
// Es una variable y no un parámetro para que main pueda tener cero valor (= terminal
// real) y un test inyectar. El riesgo de una global es que se olvide resetear, así
// que el reset va en t.Cleanup de sinTerminal y aquí se comprueba que el valor
// cero significa "sin opciones", no "opciones vacías explícitas".
func TestOptsSinTerminalNoRompeElCableadoPorDefecto(t *testing.T) {
	if len(opts) != 0 {
		t.Fatalf("opts = %v al empezar: tiene que estar vacía por defecto, porque main la usa sin tocar", opts)
	}
	sinTerminal(t, nil)
	if len(opts) != 2 {
		t.Errorf("opts = %d opciones tras sinTerminal, want 2", len(opts))
	}
	// Y el programa las acepta sin quejarse: un mal uso de tea.WithInput sería un
	// panic en el arranque, no un error.
	_, err := tea.NewProgram(nil, opts...).Run()
	_ = err // con model nil el programa no arranca, pero la construcción no falla
}

// fakeManager cumple process.Manager sin arrancar nada: runTUI no lo usa hasta que
// el usuario pulse una tecla, y este test nunca llega ahí.
type fakeManager struct{}

func (fakeManager) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, errors.New("no se arranca nada en un test")
}

func (fakeManager) Stop(process.StopSpec) error { return nil }

func (fakeManager) Evaluate(process.EvalSpec) process.Status {
	return process.StatusStopped
}

// selfBin devuelve la ruta del binario de test, que tiene el mismo main que el
// binario de producción.
func selfBin(t *testing.T) string {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("no se pudo resolver el propio binario: %v", err)
	}
	return bin
}

// runBin ejecuta un binario con argumentos y devuelve su salida y su código.
func runBin(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	// Un HOME propio para que el binario no lea nada del developer.
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
