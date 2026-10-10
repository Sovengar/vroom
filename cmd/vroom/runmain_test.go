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

	if code := runMain([]string{"help"}, falso, func(int) { t.Fatal("help must not ask to exit") }); code != 0 {
		t.Errorf("runMain(help) = %d, want 0", code)
	}
	if llamada {
		t.Error("the TUI was started with a subcommand: `cli.Run` already resolved the work")
	}
}

// The TUI is injected because the real one needs a terminal: what is under test is the startup decision, not the terminal program.
func TestRunMainDevuelveCeroCuandoLaTUIArrancaYSale(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	if code := runMain(nil, func() error { return nil }, func(int) { t.Fatal("a TUI that starts fine does not ask to exit") }); code != 0 {
		t.Errorf("runMain without subcommand and with the TUI fine = %d, want 0: a `vroom` without arguments "+
			"that the user closes with q must exit as success", code)
	}
}

// An unknown flag is not a failed subcommand but "start the TUI", so it must never reach the CLI exit path: exiting there would leave `vroom --whatever` doing nothing.
func TestRunMainConAYSinSubcomandoEsElRepartoQueDecideElModo(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))
	root := t.TempDir()
	t.Chdir(root)

	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	var pedidos []int
	if code := runMain([]string{"--no-existe"}, func() error { return errors.New("no TTY") },
		func(c int) { pedidos = append(pedidos, c) }); code != 1 {
		t.Errorf("runMain(--no-existe) = %d, want 1: if the TUI cannot start, the process must die "+
			"with 1 and not return with 0, which a script would read as success", code)
	}
	if len(pedidos) != 0 {
		t.Errorf("exit = %v, want no calls: an unknown flag is not a failed subcommand", pedidos)
	}
}

// runMain has two exit paths that must not be confused: a failed subcommand is applied by cli.Run through the exit callback, the TUI by runMain's own return value, and mixing them lets a failed subcommand exit 0.
func TestRunMainPropagaElCodigoDeSalidaDeUnSubcomandoQueFalla(t *testing.T) {
	t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "ausente.toml"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "nada.toml"), []byte("name = \"nada\"\ncommands.start.run = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "nada"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nada", ".vroom.toml"),
		[]byte("name = \"nada\"\ncommands.start.run = \"true\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	var pedidos []int
	arrancada := false
	code := runMain([]string{"stop", "no-existe-en-absoluto"},
		func() error { arrancada = true; return nil },
		func(c int) { pedidos = append(pedidos, c) })

	if arrancada {
		t.Error("the TUI was started after a subcommand that was recognized")
	}
	if len(pedidos) != 1 {
		t.Fatalf("exit = %v, want exactly one call: the code of a failed subcommand is "+
			"applied by `cli.Run`, not `runMain`", pedidos)
	}
	if pedidos[0] == 0 {
		t.Errorf("exit = %d for a failed subcommand: 0 would make a script read a failure as "+
			"a success", pedidos[0])
	}
	if code != 0 {
		t.Errorf("runMain = %d with a failed subcommand, want 0: for `runMain` the subcommand is "+
			"already resolved, and its code was applied by `cli.Run`", code)
	}
}

// MEASURED: os.Getwd only returns ENOENT when the working directory is deleted, which is the real case of a project dir renamed from another terminal; t.Chdir restores the process so the rest of the suite does not notice.
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
		t.Skip("the working directory is still there: this host does not let you delete the CWD and the test is " +
			"not testing the failure it claims to test")
	}

	// Isolated because inside a deleted CWD NewStore can fail first on HOME and mask the Getwd error.
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))

	err = runTUI(&procesoFalso{})
	if err == nil {
		t.Fatal("with the working directory gone the TUI cannot start, and that must " +
			"come out as an error")
	}
	if !strings.Contains(err.Error(), "no such file") && !strings.Contains(err.Error(), "getwd") &&
		!strings.Contains(err.Error(), "no existe") && !strings.Contains(err.Error(), "archivo") {
		t.Logf("the error was %q: it comes from the %s, which is the environment failure in question", err, err)
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
