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

// opts son las opciones del programa. Existe una variable y no una lista de
// parámetros para que un test pueda inyectar entrada y salida y arrancar la TUI
// sin un terminal de verdad detrás.
//
// Cero valor = terminal real, que es lo que usa main.
var opts []tea.ProgramOption

// main es el único sitio del repo que llama a os.Exit, y por eso no tiene test:
// un os.Exit en medio de un test mata el proceso entero y no hay forma de observar
// el código de salida desde dentro.
//
// Lo que sí se cubre es todo lo que hay alrededor, vía runTUI: el reparto entre
// modo CLI y modo TUI, la creación del store y el arranque del programa. El
// `return` después de `cli.Run` se comprueba desde fuera, ejecutando el binario con
// un subcomando y mirando su código de salida.
func main() {
	// CLI mode: subcomandos para consumo por IA.
	//
	// El código de salida lo aplica main, no el paquete cli: así el paquete
	// informa del fallo en vez de matar el proceso, y main conserva el control
	// del único os.Exit que existe en el arranque. `Run` devuelve false cuando
	// no hubo subcomando, y entonces sigue hacia la TUI.
	if cli.Run(os.Args[1:]) {
		return
	}

	// TUI mode: por defecto sin argumentos.
	if err := runTUI(process.NewManager()); err != nil {
		fmt.Fprintln(os.Stderr, "vroom:", err)
		os.Exit(1)
	}
}

// runTUI es el arranque sin el os.Exit: separa "qué pasó" de "cómo muere el
// proceso", igual que hace cli.Run con los subcomandos.
//
// La razón de separarlo es que main() no se puede probar: os.Exit mata el proceso
// de test y tea.Program necesita un terminal. Con esta forma, los dos fallos que
// sí pueden producirse —el store y el directorio de trabajo— se devuelven como error y
// se comprueban, y el camino de éxito se arranca con la entrada y la salida
// redirigidas.
//
// El error no lleva contexto porque main ya imprime el prefijo "vroom:" y los tres
// son fallos de entorno con un mensaje que se explica solo.
//
// El segundo error, el de os.Getwd, no tiene test: se necesita que el directorio de
// trabajo haya desaparecido o dejado de ser legible, y en un test eso significaría
// unlinkear el CWD bajo los pies del propio proceso de test.
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
