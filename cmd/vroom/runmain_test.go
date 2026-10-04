package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/process"
)

// main() has no test of its own because it os.Exits, so everything it decides was moved into runMain, which answers the only question main asks: subcommand or TUI?

// The injected TUI double must never be called here: the real one would hang the test waiting for input, and a hang reports nothing.
func TestRunMainConUnSubcomandoNoLlegaALaTUI(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))
	llamada := false
	falso := func() error { llamada = true; return nil }

	if code := runMain([]string{"help"}, falso, func(int) { t.Fatal("help no debe pedir salir") }); code != 0 {
		t.Errorf("runMain(help) = %d, want 0", code)
	}
	if llamada {
		t.Error("con un subcomando se arrancó la TUI: `cli.Run` ya resolvió el trabajo")
	}
}

// The TUI is injected because the real one needs a terminal: what is under test is the startup decision, not the terminal program.
func TestRunMainDevuelveCeroCuandoLaTUIArrancaYSale(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	if code := runMain(nil, func() error { return nil }, func(int) { t.Fatal("una TUI que arranca bien no pide salir") }); code != 0 {
		t.Errorf("runMain sin subcomando y con la TUI bien = %d, want 0: un `vroom` sin argumentos "+
			"que el usuario cierra con q tiene que salir como éxito", code)
	}
}

// An unknown flag is not a failed subcommand but "start the TUI", so it must never reach the CLI exit path: exiting there would leave `vroom --whatever` doing nothing.
func TestRunMainConAYSinSubcomandoEsElRepartoQueDecideElModo(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))
	root := t.TempDir()
	t.Chdir(root)

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

// runMain has two exit paths that must not be confused: a failed subcommand is applied by cli.Run through the exit callback, the TUI by runMain's own return value, and mixing them lets a failed subcommand exit 0.
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

// MEDIDO: os.Getwd only returns ENOENT when the working directory is deleted, which is the real case of a project dir renamed from another terminal; t.Chdir restores the process so the rest of the suite does not notice.
func TestRunTUIFallaSinDirectorioDeTrabajo(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))

	// The dir is removed after t.Chdir picked it because os.Getwd uses the kernel descriptor, which stays valid until the directory actually disappears.
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

	// Isolated because inside a deleted CWD NewStore can fail first on HOME and mask the Getwd error.
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

// Accepts everything: these tests cover startup, not process management, so no test of main can kill a real process.
type procesoFalso struct{}

func (*procesoFalso) Start(process.StartSpec) (process.StartResult, error) {
	return process.StartResult{}, nil
}
func (*procesoFalso) Stop(process.StopSpec) error { return nil }
func (*procesoFalso) Evaluate(process.EvalSpec) process.Status {
	return process.StatusStopped
}

var _ process.Manager = (*procesoFalso)(nil)
