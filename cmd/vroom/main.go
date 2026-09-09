// vroom — TUI para gestionar servicios de múltiples proyectos.
//
// Escanea el CWD (2 niveles), detecta proyectos por marcadores de
// lenguaje y permite iniciar/detener/ver logs de servicios daemonizados
// que sobreviven al cierre de la terminal.
//
// Modo CLI (para consumo por IA):
//
//	vroom list          → JSON con todos los proyectos y su estado
//	vroom start <name>  → arranca un servicio daemonizado
//	vroom stop <name>   → detiene un servicio
//	vroom build <name>  → ejecuta command_build (one-shot)
//	vroom install <name> → ejecuta command_install (one-shot)
//	vroom logs <name>   → muestra logs del servicio
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

func main() {
	// CLI mode: subcomandos para consumo por IA
	if cli.Run(os.Args[1:]) {
		return
	}

	// TUI mode: por defecto sin argumentos
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
