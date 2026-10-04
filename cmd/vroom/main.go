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

// main es el único sitio del repo que llama a os.Exit, y por eso no tiene test
// propio: un `os.Exit` en medio de un test mata el proceso entero y no hay forma de
// observar el código de salida desde dentro.
//
// Lo que sí se comprueba es todo lo que hay alrededor, vía `runMain` —que devuelve
// el código en vez de aplicarlo— y vía un proceso hijo que re-ejecuta este mismo
// binario de test y llama a `main()` de verdad, mirando su código de salida desde
// fuera. Eso es lo único que demuestra que `main` pasa el del proceso real a
// `os.Exit`, y no un número inventado por el test.
func main() {
	os.Exit(runMain(os.Args[1:], func() error { return runTUI(process.NewManager()) }, os.Exit))
}

// runMain es el arranque entero: reparte entre modo CLI y modo TUI y devuelve el
// código con el que debe morir el proceso.
//
// El código lo devuelve `runMain` y no se aplica aquí por dos razones. La primera es
// que así todo el arranque es comprobable: `main()` se limita a lo único que no se
// puede probar —llamar a `os.Exit`— y el reparto entre CLI y TUI, la creación del
// store, el directorio de trabajo y los dos códigos de salida se ejecutan enteros
// desde un test. La segunda es que el único `os.Exit` del repo queda en una línea,
// que es donde se puede ver que es el único.
//
// El arranque de la TUI va por parámetro (`tui`) porque necesita un terminal de
// verdad detrás: sin él, la rama de éxito sólo se puede ejecutar en un proceso hijo
// con una TTY, que es un tipo de prueba que no se puede escribir. Con el parámetro,
// el reparto entre "hubo subcomando" y "no lo hubo" se comprueba entero, y
// `runTUI` se prueba por separado con la entrada y la salida redirigidas.
func runMain(args []string, tui func() error, exit func(int)) int {
	// CLI mode: subcomandos para consumo por IA.
	//
	// El código de salida lo aplica quien llama, no este paquete: así el paquete
	// cli informa del fallo en vez de matar el proceso. `Run` devuelve false cuando
	// no hubo subcomando, y entonces sigue hacia la TUI.
	if cli.Run(args, exit) {
		return 0
	}

	// TUI mode: por defecto sin argumentos.
	if err := tui(); err != nil {
		fmt.Fprintln(os.Stderr, "vroom:", err)
		return 1
	}
	return 0
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
