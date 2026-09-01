// vroom — TUI para gestionar servicios de múltiples proyectos.
//
// Escanea el CWD (2 niveles), detecta proyectos por marcadores de
// lenguaje y permite iniciar/detener/ver logs de servicios daemonizados
// que sobreviven al cierre de la terminal.
package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/process"
	"vroom/internal/state"
	"vroom/internal/tui"
)

func main() {
	store, err := state.NewStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "vroom:", err)
		os.Exit(1)
	}

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "vroom:", err)
		os.Exit(1)
	}

	model := tui.New(store, process.NewManager(), root)
	if _, err := tea.NewProgram(model).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "vroom:", err)
		os.Exit(1)
	}
}
