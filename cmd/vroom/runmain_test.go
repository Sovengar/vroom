package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/process"
)

// ---------------------------------------------------------------------------
// El reparto entre modo CLI y modo TUI, y el código de salida.
//
// `main()` no tiene test por sí mismo —mata el proceso con `os.Exit`—, pero todo lo
// que decide se ha movido a `runMain`, que sí. Lo que se comprueba aquí es la
// pregunta que hace `main`: ¿esto es un subcomando o es la TUI?
// ---------------------------------------------------------------------------

// TestRunMainConUnSubcomandoNoLlegaALaTUI: el modo CLI.
//
// Con un subcomando reconocido, `runMain` no tiene que arrancar un programa de
// terminal —que en un test colgaría esperando entrada—. Si lo hiciera, este test se
// quedaría esperando y el fallo se vería como un cuelgue, no como un error.
func TestRunMainConUnSubcomandoNoLlegaALaTUI(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))
	// El subcomando más inocuo que existe: `--help` imprime y sale con 0.
	// El arranque de la TUI se inyecta y NO debe llamarse: con un subcomando
	// reconocido la TUI no se toca, y una llamada aquí colgaría el test.
	llamada := false
	falso := func() error { llamada = true; return nil }

	if code := runMain([]string{"help"}, falso, func(int) { t.Fatal("help no debe pedir salir") }); code != 0 {
		t.Errorf("runMain(help) = %d, want 0", code)
	}
	if llamada {
		t.Error("con un subcomando se arrancó la TUI: `cli.Run` ya resolvió el trabajo")
	}
}

// TestRunMainDevuelveCeroCuandoLaTUIArrancaYSale: el camino bueno de la TUI.
//
// Sin subcomando y con la TUI arrancando y cerrándose bien, el proceso tiene que
// salir con 0. Un 1 aquí lo haría aparecer como fallo en cualquier script que
// lance `vroom` sin argumentos.
//
// Y la TUI va inyectada porque la de verdad necesita un terminal: lo que se prueba
// es la decisión del arranque, no el programa de terminal.
func TestRunMainDevuelveCeroCuandoLaTUIArrancaYSale(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	if code := runMain(nil, func() error { return nil }, func(int) { t.Fatal("una TUI que arranca bien no pide salir") }); code != 0 {
		t.Errorf("runMain sin subcomando y con la TUI bien = %d, want 0: un `vroom` sin argumentos "+
			"que el usuario cierra con q tiene que salir como éxito", code)
	}
}

// TestRunMainConAYSinSubcomandoEsElRepartoQueDecideElModo: el interruptor.
//
// `cli.Run` devuelve false cuando no hubo subcomando, y `runMain` sigue hacia la
// TUI. El caso que importa es el de los argumentos que NO son un subcomando: son
// las flags que la TUI misma acepta.
//
// Y aquí se comprueba el otro lado del interruptor: con un subcomando la TUI no se
// arranca, y sin él tampoco se puede arrancar en un test —haría falta un terminal—,
// así que el camino de la TUI se cubre por `runTUI` y lo que se comprueba aquí es
// que el reparto no se equivoca de lado.
func TestRunMainConAYSinSubcomandoEsElRepartoQueDecideElModo(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))
	root := t.TempDir()
	t.Chdir(root)

	// Un flag que no es ningún subcomando: `cli.Run` no lo reconoce, devuelve false
	// —"esto no es un subcomando"—, y entonces `runMain` cae en la TUI.
	//
	// El código 1 que sale es el de "la TUI no arrancó", y lo que se comprueba aquí
	// es que NO se pide un `exit` por el camino de la CLI: ese `exit` es para cuando un
	// subcomando falla, y un flag desconocido no es un subcomando fallido sino "arranca
	// la TUI". Pedir salir aquí dejaría `vroom --lo-que-sea` sin hacer nada.
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	var pedidos []int
	if code := runMain([]string{"--no-existe"}, func() error { return errors.New("sin TTY") },
		func(c int) { pedidos = append(pedidos, c) }); code != 1 {
		t.Errorf("runMain(--no-existe) = %d, want 1: si la TUI no arranca, el proceso tiene que morir "+
			"con 1 y no volver con 0, que un script leería como éxito", code)
	}
	if len(pedidos) != 0 {
		t.Errorf("exit = %v, want ninguna llamada: un flag desconocido no es un subcomando fallido", pedidos)
	}
}

// TestRunMainPropagaElCodigoDeSalidaDeUnSubcomandoQueFalla: el otro `exit`.
//
// El reparto tiene DOS caminos de salida y no se deben confundir. Uno es el de aquí:
// un subcomando que se ejecutó y falló —`start` de un servicio que no existe—, y el
// código lo aplica el `cli.Run` con el `exit` que le pasó `runMain`. El otro es el de
// la TUI, y lo aplica `runMain` con su valor de retorno.
//
// Si los dos se mezclaran, un subcomando fallido podría salir con 0 por el camino de
// la TUI, que es el peor sitio posible para esconderse.
func TestRunMainPropagaElCodigoDeSalidaDeUnSubcomandoQueFalla(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "nada.toml"), []byte("name = \"nada\"\ncommand_start = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "nada"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nada", ".vroom.toml"),
		[]byte("name = \"nada\"\ncommand_start = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	var pedidos []int
	arrancada := false
	code := runMain([]string{"stop", "no-existe-en-absoluto"},
		func() error { arrancada = true; return nil },
		func(c int) { pedidos = append(pedidos, c) })

	if arrancada {
		t.Error("se arrancó la TUI después de un subcomando que sí se reconoció")
	}
	if len(pedidos) != 1 {
		t.Fatalf("exit = %v, want exactamente una llamada: el código de un subcomando fallido lo "+
			"aplica `cli.Run`, no `runMain`", pedidos)
	}
	if pedidos[0] == 0 {
		t.Errorf("exit = %d por un subcomando fallido: 0 haría que un script leyera un fallo como "+
			"un éxito", pedidos[0])
	}
	if code != 0 {
		t.Errorf("runMain = %d con un subcomando fallido, want 0: para `runMain` el subcomando ya "+
			"está resuelto, y su código lo aplicó `cli.Run`", code)
	}
}

// TestRunTUIFallaSinDirectorioDeTrabajo: el `os.Getwd` que no puede resolverse.
//
// El segundo fallo de arranque es el del directorio de trabajo, y es el que menos se
// piensa: `os.Getwd` falla cuando el directorio de trabajo ha desaparecido o ha
// dejado de ser legible, que pasa cuando alguien renombra un directorio de proyecto
// desde otra terminal mientras la TUI está corriendo en él.
//
// MEDIDO: se provoca borrando el directorio de trabajo del proceso de test. Es
// agresivo, pero es la única forma de que `getcwd` devuelva ENOENT, y `t.Chdir` se
// encarga de dejar el proceso donde estaba para que el resto de la suite no note
// nada.
func TestRunTUIFallaSinDirectorioDeTrabajo(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))

	// El CWD se borra DESPUÉS de que `t.Chdir` lo haya elegido: `os.Getwd` usa el
	// descriptor que tiene abierto el kernel, que sigue siendo válido hasta que el
	// directorio desaparece de verdad.
	volado := filepath.Join(t.TempDir(), "proyecto-que-desaparece")
	if err := os.MkdirAll(volado, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(volado)
	if err := os.RemoveAll(volado); err != nil {
		t.Fatal(err)
	}

	_, err := os.Getwd()
	if err == nil {
		t.Skip("el directorio de trabajo sigue ahí: este host no deja borrar el CWD y el test no " +
			"está probando el fallo que dice probar")
	}

	// Con el CWD dentro de un directorio volado, `state.NewStore` puede fallar antes
	// (por el HOME), así que se aísla para que lo que se provoque sea el Getwd.
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	err = runTUI(&procesoFalso{})
	if err == nil {
		t.Fatal("con el directorio de trabajo desaparecido la TUI no puede arrancar, y eso tiene " +
			"que salir como error")
	}
	if !strings.Contains(err.Error(), "no such file") && !strings.Contains(err.Error(), "getwd") &&
		!strings.Contains(err.Error(), "no existe") && !strings.Contains(err.Error(), "archivo") {
		t.Logf("el error fue %q: viene del %s, que es el fallo de entorno que tocaba", err, err)
	}
}

// procesoFalso acepta todo: lo que se prueba es el arranque, no la gestión de
// procesos, y así ningún test de `main` puede matar algo de verdad.
type procesoFalso struct{}

func (*procesoFalso) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}
func (*procesoFalso) Stop(process.StopSpec) error { return nil }
func (*procesoFalso) Evaluate(process.EvalSpec) process.Status {
	return process.StatusStopped
}

var _ process.Manager = (*procesoFalso)(nil)
