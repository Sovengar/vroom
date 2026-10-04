// main owns the repo's only os.Exit; cli.Run reports failures and returns so every command stays testable.
package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/cli"
	"vroom/internal/process"
	"vroom/internal/state"
	"vroom/internal/tui"
)

// A package var, not a parameter, so a test can inject input and output and run the TUI with no real terminal behind it.
var opts []tea.ProgramOption

// The repo's only os.Exit, so it has no test of its own: the code is verified through runMain, plus a child process that re-execs this binary and observes the real exit status.
func main() {
	os.Exit(runMain(os.Args[1:], func() error { return runTUI(process.NewManager()) }, os.Exit))
}

// tui is injected because tea.Program needs a real terminal, and injection is what lets the whole CLI-vs-TUI split be tested.
func runMain(args []string, tui func() error, exit func(int)) int {
	// cli.Run only reports: it returns false when there was no subcommand, and the caller, not the package, applies the exit code.
	if cli.Run(args, exit) {
		return 0
	}

	if err := tui(); err != nil {
		fmt.Fprintln(os.Stderr, "vroom:", err)
		return 1
	}
	return 0
}

// Errors carry no context because main already prints the "vroom:" prefix and all three are self-explanatory environment failures.
func runTUI(manager process.Manager) error {
	store, err := state.NewStore()
	if err != nil {
		return err
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}

	model := tui.New(store, manager, root)
	_, err = tea.NewProgram(model, opts...).Run()
	return err
}
