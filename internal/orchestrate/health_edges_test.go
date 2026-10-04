package orchestrate

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// Los bordes que dependen de un dato externo o de una espera:
//
//   - el sondeo lento de AwaitPort (500ms) frente al fino (100ms), y los dos
//     mensajes de timeout que dependen de si el discovery sigue en vuelo;
//   - los dos rechazos de compose que sólo se ven con un fichero escrito a mano;
//   - los fallos del engine que ocurren DESPUÉS de resolver el servicio, donde el
//     nombre ya no es el problema.
//
// Y una guarda que NO se puede provocar y no se quita: el `else` de "not started"
// en Launch. wg.Wait() garantiza que cada goroutine escribió su entrada, así que
// la rama es imposible hoy. Se conserva porque protege una PROPIEDAD del patrón
// (toda goroutine registra su resultado), no una condición de ejecución: si
// alguien añadiera un `return` temprano en la goroutine, sin esa guarda la etapa
// publicaría cero resultados para ese servicio y el usuario vería un stack verde
// con una etapa vacía.
// ---------------------------------------------------------------------------

// TestAwaitPortDynamicSondeaLentoCuandoElDiscoveryNoEstaEnVuelo: puerto cerrado y
// no-pendiente sondea cada 500ms y agota el presupuesto con un mensaje genérico.
//
// El par con el otro test es lo que importa: el mensaje de timeout dice si el
// puerto estaba pendiente de un discovery EN VUELO o si ya estaba decidido y no
// abrió. Sin esa distinción el usuario lee "no abrió" y no sabe que detrás había
// un discovery que quizá iba a darle el puerto.
func TestAwaitPortDynamicSondeaLentoCuandoElDiscoveryNoEstaEnVuelo(t *testing.T) {
	port := closedTCPPort(t)
	start := time.Now()
	err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, Port: port}, 600*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("un puerto cerrado no puede darse por sano")
	}
	if errors.Is(err, ErrPortPending) {
		t.Errorf("err = %v: sin discovery en vuelo la causa no es puerto pendiente", err)
	}
	if !strings.Contains(err.Error(), "not open") {
		t.Errorf("err = %q, want un mensaje que diga que el puerto no abrió", err)
	}
	// El presupuesto se respeta: ni instantáneo (no sondeó) ni el triple.
	if elapsed < 500*time.Millisecond {
		t.Errorf("tardó %s: el sondeo de 500ms no llegó a completarse", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("tardó %s con un timeout de 600ms: AwaitPort ignora su presupuesto", elapsed)
	}
}

// TestAwaitPortDynamicSondeaFinoConDiscoveryEnVueloYAbre: el puerto abre después
// del primer sondeo y la espera lo pilla.
//
// Es lo que compra el sondeo de 100ms: con el de 500ms, un puerto que abre a los
// 150ms se habría detectado a los 500ms y la etapa habría gastado 350ms de más.
// Aquí se mide que el servicio se considera sano en cuanto abre, no en el
// siguiente tick del relojedonde casualmente cae.
func TestAwaitPortDynamicSondeaFinoConDiscoveryEnVueloYAbre(t *testing.T) {
	// Un puerto libre que se ocupa justo después del primer sondeo.
	port := closedTCPPort(t)
	go func() {
		time.Sleep(150 * time.Millisecond)
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return
		}
		// El listener vive hasta que el test acaba.
		time.Sleep(5 * time.Second)
		_ = ln.Close()
	}()

	start := time.Now()
	err := AwaitPort(PortWait{Mode: manifest.PortModeDynamic, Port: port, PortPending: true}, 3*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("un puerto que abre a los 150ms tiene que pasar el health check: %v", err)
	}
	// Detectado por sondeo fino (100ms), no por el lento: si fuera el lento, el
	// mínimo sería 500ms.
	if elapsed >= 500*time.Millisecond {
		t.Errorf("tardó %s: el sondeo fino de discovery en vuelo no está sondando cada 100ms", elapsed)
	}
}

// TestParseComposeRechazaEtapaSinNombreYTimeoutInvalido: los dos errores que sólo
// aparecen con un fichero escrito a mano.
//
// Los dos se rechazan en el PARSEO, no en el arranque, y ése es el punto: un
// fichero con una etapa sin nombre no se puede ni Dry Runear, y aceptarlo
// significaría un plan que promete arrancar algo que no existe.
func TestParseComposeRechazaEtapaSinNombreYTimeoutInvalido(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			"etapa sin nombre",
			`
primary_group = "g1"

[[stack]]
name = "app"

[[stack.stage]]
services = ["api"]
`,
			"name",
		},
		{
			"timeout que no es una duración",
			`
primary_group = "g1"

[[stack]]
name = "app"

[[stack.stage]]
name = "build"
services = ["api"]
timeout = "pronto"
`,
			"timeout",
		},
		{
			"timeout numérico sin unidad",
			`
primary_group = "g1"

[[stack]]
name = "app"

[[stack.stage]]
name = "build"
services = ["api"]
timeout = 30
`,
			"timeout",
		},
		{
			"etapa sin servicios",
			`
primary_group = "g1"

[[stack]]
name = "app"

[[stack.stage]]
name = "build"
`,
			"at least one service",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCompose(t, dir, tt.content)
			_, err := ParseComposeFile(dir)
			if err == nil {
				t.Fatal("un compose con este error tiene que rechazarse en el parseo, no en el arranque")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %q, want que mencione %q: el mensaje tiene que decir QUÉ está mal", err, tt.wantErr)
			}
		})
	}
}

// TestStopStackConUnNombreQueNoResuelvePropagaElError: la resolución se hace con
// el mismo criterio que el arranque, y ante un nombre ambiguo o inexistente
// devuelve el error en vez de elegir uno.
//
// Es lo que impide parar el proyecto equivocado: con dos proyectos chamados "api",
// parar "api" sin preguntar pararía uno de los dos en orden de escaneo, y el
// usuario se encontraría con uno parado que no sabía que existía.
func TestStopStackConUnNombreQueNoResuelvePropagaElError(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"no-existe"}}}}
	err := engine.StopStack(stack, nil)
	if err == nil {
		t.Fatal("parar un servicio que no existe tiene que dar error")
	}
	if !strings.Contains(err.Error(), "no-existe") {
		t.Errorf("err = %q, want que nombre el servicio: es lo que el usuario tiene que corregir", err)
	}

	// Y con duplicados también: mismo criterio que Launch.
	ambos := []scanner.Project{
		{Path: "/a/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "./a"}},
		{Path: "/b/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "./b"}},
	}
	dup := &Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"api"}}}}
	if err := engine.StopStack(dup, ambos); err == nil {
		t.Error("con dos proyectos llamados api hay que preguntar, no parar uno al azar")
	}
}

// TestStartServiceConStoreNoEscribibleReportaElError: si el directorio de servicio
// no se puede crear, el servicio no arranca y el motivo vuelve al usuario.
//
// La ruta es `EnsureServiceDir` → error → ServiceResult.Error. Lo que importa es
// que NO se intente arrancar: sin el directorio no hay log donde escribir, y un
// proceso lanzado sin log deja al usuario sin nada que mirar cuando se rompa.
func TestStartServiceConStoreNoEscribibleReportaElError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede escribir en un directorio sin permiso: el caso no se puede provocar")
	}

	root := t.TempDir()
	// El directorio padre sin permiso de escritura: crear el subdirectorio del
	// servicio falla.
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	store := state.NewStoreAt(locked)
	engine := NewEngine(&mockManager{}, store)

	svc := ResolvedService{Name: "api", Project: scanner.Project{
		Path:       "/dev/api",
		Name:       "api",
		Configured: true,
		Manifest: &manifest.Manifest{
			Name: "api", Command: "./api", PortMode: manifest.PortModeNone,
		},
	}}

	got := engine.startService(svc, time.Second)
	if got.Error == "" {
		t.Errorf("con el store inservible tiene que haber error, no un servicio arrancado: %+v", got)
	}
	if got.Pid != 0 {
		t.Errorf("Pid = %d con el store inservible, want 0: nada arrancó", got.Pid)
	}
	if got.Action == "started" {
		t.Error("Action = started con el store inservible: el servicio no llegó a arrancar")
	}
}

// TestLaunchConServicioQueArrancaPeroNoAbreElPuertoFallaLaEtapaYLoDejaMuerto: el
// proceso vive, el puerto no abre, eso SÍ es un fallo — Y el proceso queda parado.
//
// Con manager REAL, no con el mock. El mock Start devuelve un PID inventado sin
// arrancar nada, así que con él el test pasaría aunque el proceso se fugase. La
// fuga que este test hunt es exactamente esa: un `sleep` de verdad que sobrevive a
// un launch que él mismo endureció como fallido.
//
// Es el otro lado de port_mode none: un servicio que declara puerto tiene que
// servirlo, y si no lo hace la etapa no puede avanzar. Lo que no puede pasar es lo
// contrario —declarar buena la etapa con el servicio sin puerto—, porque las
// etapas siguientes hablarían con un servicio que no escucha.
//
// Y al revés tampoco: un launch fallido que deja procesos vivos convierte "vroom
// start falló" en "vroom start falló y ahora tengo ocho procesos zombis". El
// rollback tiene que deshacer lo que ESTA sesión arrancó, y sólo lo que arrancó
// ella.
func TestLaunchConServicioQueArrancaPeroNoAbreElPuertoFallaLaEtapaYLoDejaMuerto(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(process.NewManager(), store)

	port := closedTCPPort(t)
	p := scanner.Project{
		Path:       t.TempDir(),
		Name:       "api",
		Configured: true,
		Manifest: &manifest.Manifest{
			Name: "api", Command: "sleep 300 # marcador-vroom", Port: port, PortMode: manifest.PortModeFixed,
		},
	}
	stack := &Stack{Name: "app", Stages: []Stage{
		{Name: "e", Services: []string{"api"}, Timeout: 700 * time.Millisecond},
	}}

	result, err := engine.Launch(stack, []scanner.Project{p})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if result.OK {
		t.Errorf("un servicio que no abre su puerto tiene que fallar la etapa: %+v", result)
	}
	if !strings.Contains(result.Error, "api") {
		t.Errorf("err = %q, want que nombre el servicio culpable", result.Error)
	}
	if len(result.Stages) != 1 || len(result.Stages[0].Services) != 1 {
		t.Fatalf("la etapa tiene que reportar su servicio aunque falle: %+v", result.Stages)
	}
	if result.Stages[0].Services[0].Error == "" {
		t.Error("el servicio fallido tiene que traer su motivo: el error de la etapa no lo desglosa")
	}

	// El meta no puede decir vivo: eso es lo que hace que un `vroom status`
	// siguiente lo muestre arriba y `vroom stop` lo busque.
	meta, err := store.LoadMeta(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != 0 {
		t.Errorf("Pid = %d tras un launch fallido, want 0: el servicio se paró pero el meta lo dice vivo", meta.Pid)
	}
	if meta.State == state.StateRunning {
		t.Errorf("State = %q tras un launch fallido: el servicio no está corriendo", meta.State)
	}
}
