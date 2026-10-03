package orchestrate

import (
	"errors"
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
// Los bordes del engine que los tests de flujo no tocan: el canal de LaunchAsync,
// las ramas de error de stopService/stopProcess, los avisos que se escriben en el
// log de un servicio, y los dos rechazos de compose que dependen de datos.
//
// LaunchAsync está al 0% y es la ruta que usa la TUI: si el resultado no llegara
// al canal, o llegara dos veces, la TUI se quedaría esperando para siempre
// esperando un mensaje que no existe.
//
// Y los avisos de stop/start se escriben en el log de un servicio, que es donde el
// usuario puede leerlos después: si no se escribieran, el motivo de un reinicio
// desaparecería.
// ---------------------------------------------------------------------------

// TestLaunchAsyncEntregaElResultadoYCierraElCanal: la ruta real de la TUI.
//
// Las dos mitades importan por separado. Que llegue el resultado es lo obvio. Que
// el canal se CIERRE es lo que permite a la TUI distinguir "terminó" de "todavía
// no": un canal abierto con el resultado ya enviado deja al consumidor en un
// `select` esperando el segundo valor para siempre.
func TestLaunchAsyncEntregaElResultadoYCierraElCanal(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "async", PrimaryGroup: "g1", Stages: []Stage{
		{Name: "s1", Services: []string{"api"}, Timeout: 10 * time.Second},
	}}
	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{
			Name: "api", Command: "./api", PortMode: manifest.PortModeNone,
		}},
	}

	ch := engine.LaunchAsync(stack, projects)

	select {
	case result, ok := <-ch:
		if !ok {
			t.Fatal("el canal llegó cerrado sin entregar resultado")
		}
		if !result.OK || result.Stack != "async" {
			t.Errorf("resultado = %+v, want OK y stack async", result)
		}
		if len(result.Stages) != 1 || len(result.Stages[0].Services) != 1 {
			t.Fatalf("el resultado no trae la etapa con su servicio: %+v", result)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("LaunchAsync no entregó nada en 30s: la TUI se quedaría colgada")
	}

	// Y el canal se cierra: el siguiente receive tiene que dar ok=false.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("el canal entregó un segundo resultado")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("el canal no se cerró tras entregar el resultado")
	}
}

// TestLaunchAsyncConErrorDeValidacionLoEntregaComoResultado: un stack que no
// resuelve no se pierde en un `return nil, err` dentro de la goroutine.
//
// Es el error que distingue a este función de un Launch directo: aquí no puede
// haber un `error` de retorno, así que el fallo tiene que viajar DENTRO del
// resultado por el canal. Si no, la goroutine cerraría el canal sin enviar nada
// y la TUI vería un stack que se terminaron sin saber por qué.
func TestLaunchAsyncConErrorDeValidacionLoEntregaComoResultado(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "roto", PrimaryGroup: "g1", Stages: []Stage{
		{Name: "s1", Services: []string{"no-existe"}, Timeout: time.Second},
	}}

	select {
	case result := <-engine.LaunchAsync(stack, nil):
		if result.OK {
			t.Error("un stack con un servicio inexistente no puede salir OK")
		}
		if result.Stack != "roto" {
			t.Errorf("Stack = %q, want roto: el error tiene que decir sobre qué stack fue", result.Stack)
		}
		if result.Error == "" {
			t.Error("sin Error el usuario ve un stack rojo sin explicación")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("LaunchAsync no entregó nada")
	}
}

// TestStopServiceSinManifiestoNoHaceNada: un proyecto sin manifiesto no tiene
// servicio que parar.
//
// Y es distinto de "parar un servicio parado": aquí ni siquiera se toca el store.
// Un `nil` de manifiesto haría que cualquier acceso a p.Manifest reventara, y
// StopStack resuelve nombres contra la lista de proyectos escaneados, que incluye
// los no configurados.
func TestStopServiceSinManifiestoNoHaceNada(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(root)
	engine := NewEngine(&mockManager{}, store)

	// Un servicio escrito a mano con un PID: si stopService lo ignorara mal, dejaría
	// un PID vivo ahí.
	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(path, state.Meta{Pid: 4242, State: state.StateRunning}); err != nil {
		t.Fatal(err)
	}

	engine.stopService(scanner.Project{Path: path, Name: "sin-manifiesto", Manifest: nil})

	meta, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Pid != 4242 {
		t.Errorf("Pid = %d tras parar un proyecto sin manifiesto, want 4242: no hay servicio que parar", meta.Pid)
	}
}

// TestStopServiceConMetaAusenteNoFalla: si no hay meta, no hay nada que parar y
// tampoco es un error.
//
// Es el caso de un servicio que nunca arrancó, y de un proyecto recién escaneado.
// La parada tiene que ser silenciosa ahí: si hiciera ruido, `vroom stop` de un
// stack con un servicio que no llegó a arrancar fallaría.
func TestStopServiceConMetaAusenteNoFalla(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	// Un manifiesto mínimo y una ruta que nunca se guardó en el store.
	p := scanner.Project{
		Path:       t.TempDir(),
		Name:       "nunca-arrancado",
		Configured: true,
		Manifest:   &manifest.Manifest{Name: "nunca-arrancado", Command: "./x", PortMode: manifest.PortModeNone},
	}
	engine.stopService(p) // no debe hacer nada ni entrar en pánico

	// Y StopStack completo sobre ese servicio tampoco.
	stack := &Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"nunca-arrancado"}}}}
	if err := engine.StopStack(stack, []scanner.Project{p}); err != nil {
		t.Errorf("StopStack de un servicio sin meta dio error %v", err)
	}
}

// TestStopServiceRetiraLaRutaYElPuertoDeUnServicioYaMuerto: un meta con todo a
// cero pero con una ruta tomada igual hay que liberarla.
//
// Es el caso que motivated releaseRouteOnStop y la razón de existir de la rama
// "else": si el servicio ya estaba muerto y hadrelease al pool, la ruta se queda
// tomada. Y una ruta tomada por un servicio parado bloquea al siguiente que
// quiera ese nombre — es el HIGH que dejó de ser HIGH cuando se corrigió.
func TestStopServiceRetiraLaRutaYElPuertoDeUnServicioYaMuerto(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(root)
	engine := NewEngine(&mockManager{}, store)

	path := t.TempDir()
	const routeName = "vroom-test-ruta-muerta"
	// Todo a cero (servicio muerto) pero con la ruta tomada y propiedad concededida.
	meta := state.Meta{
		State:      state.StateRunning,
		RouteName:  routeName,
		RouteOwned: true,
	}
	if err := store.SaveMeta(path, meta); err != nil {
		t.Fatal(err)
	}

	engine.stopService(scanner.Project{
		Path:     path,
		Name:     "muerto",
		Manifest: &manifest.Manifest{Name: "muerto", Command: "./x", PortMode: manifest.PortModeNone},
	})

	got, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.RouteOwned {
		t.Error("RouteOwned sigue en true tras parar un servicio muerto: la ruta queda tomada para siempre")
	}
	if got.State != state.StateStopped {
		t.Errorf("State = %q, want %q", got.State, state.StateStopped)
	}
	if got.Pid != 0 || got.Pgid != 0 {
		t.Errorf("Pid/Pgid = %d/%d tras parar, want 0/0", got.Pid, got.Pgid)
	}
}

// TestStopServiceNoRetiraUnaRutaQueNoEraSuya: RouteOwned false significa que el
// handle ya lo tenía otro.
//
// Retirarla sería pisar la ruta de otro servicio — el bug que la propiedad existe
// para evitar. El handle sobrevive a la revocación para que la reconciliación
// tenga dónde mirar, así que usarlo como autoridad de borrado borraría rutas
// ajenas.
func TestStopServiceNoRetiraUnaRutaQueNoEraSuya(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	path := t.TempDir()
	// RouteOwned false con un nombre de ruta presente: la ruta es de otro.
	if err := store.SaveMeta(path, state.Meta{
		State:      state.StateRunning,
		RouteName:  "vroom-test-ruta-ajena",
		RouteOwned: false,
	}); err != nil {
		t.Fatal(err)
	}

	engine.stopService(scanner.Project{
		Path:     path,
		Name:     "ajeno",
		Manifest: &manifest.Manifest{Name: "ajeno", Command: "./x", PortMode: manifest.PortModeNone},
	})

	got, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	// El nombre sobrevive (el handle se queda para la reconciliación) pero no se
	// marca como ours.
	if got.RouteOwned {
		t.Error("RouteOwned = true tras parar un servicio cuya ruta no era suya")
	}
	if got.RouteName != "vroom-test-ruta-ajena" {
		t.Errorf("RouteName = %q: la ruta de otro no puede desaparecer del meta", got.RouteName)
	}
}

// TestStopProcessEscribeLosAvisosEnElLogDeStderrDelServicio: el motivo de un
// reinicio tiene que quedar escrito donde el usuario puede leerlo.
//
// El manager reporta los avisos por un callback; stopProcess los acumula y los
// escribe DESPUÉS de parar, con prefijo. El orden importa: si se escribieran
// antes, el log tendría la línea y luego el "stop", que se lee como que el aviso
// era del arranque.
func TestStopProcessEscribeLosAvisosEnElLogDeStderrDelServicio(t *testing.T) {
	root := t.TempDir()
	store := state.NewStoreAt(root)
	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}

	mgr := &mockManager{stopFunc: func(spec process.StopSpec) error {
		if spec.Warn != nil {
			spec.Warn("descendiente %d sobrevivió a SIGKILL", 999)
		}
		return nil
	}}
	engine := NewEngine(mgr, store)

	meta := state.Meta{Pid: 4242, Pgid: 4242}
	engine.stopProcess(path, &meta)

	data, err := os.ReadFile(store.StderrLog(path))
	if err != nil {
		t.Fatalf("no se pudo leer el log de stderr: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "stop:") {
		t.Errorf("el aviso no lleva el prefijo de stop: %q", got)
	}
	if !strings.Contains(got, "sobrevivió a SIGKILL") {
		t.Errorf("el aviso del manager no llegó al log: %q", got)
	}
	// Y el log de stdout queda intacto: los avisos de vroom no se mezclan con la
	// salida del servicio, que es lo que el usuarioPegar en otro lado.
	if _, err := os.Stat(store.StdoutLog(path)); err == nil {
		if data, err := os.ReadFile(store.StdoutLog(path)); err == nil && len(data) > 0 {
			t.Errorf("stop escribió en el log de stdout del servicio: %q", data)
		}
	}
}

// TestStopProcessSinAvisosNoCreaElLogDeStderr: parar sin avisos no deja un log
// vacío por ahí.
//
// El log de stderr es lo que el usuario mira cuando algo va mal, y un fichero
// vacío sugiere que se escribió algo que se perdió. Sólo se crea si hay algo que
// decir.
func TestStopProcessSinAvisosNoCreaElLogDeStderr(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)
	path := t.TempDir()

	engine.stopProcess(path, &state.Meta{Pid: 4242, Pgid: 4242})

	if _, err := os.Stat(store.StderrLog(path)); err == nil {
		t.Error("parar sin avisos creó un log de stderr vacío")
	}
}

// TestAppendLineAnexaYSobreviveAUnLogQueNoExiste: appendLine es lo que escribe
// los avisos, y tiene que crear el fichero.
//
// El servicio arranca con stdout/stderr apuntando a logs que aún no existen: sin
// O_CREATE, el primer aviso se perdería y no volvería a intentarlo hasta el
// siguiente arranque.
func TestAppendLineAnexaYSobreviveAUnLogQueNoExiste(t *testing.T) {
	dir := t.TempDir()
	// Un log dentro de un directorio que NO existe: el error tiene que volver, no
	// tragarse. Es el caso que distingue "crear el fichero" de "crear el árbol".
	path := filepath.Join(dir, "sub", "log")

	err := appendLine(path, "primera")
	if err == nil {
		t.Fatal("appendLine a un directorio inexistente tiene que dar error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("err = %v, want NotExist", err)
	}

	// Y con el directorio de verdad: anexa, no sobrescribe.
	real := filepath.Join(dir, "log")
	if err := appendLine(real, "primera"); err != nil {
		t.Fatal(err)
	}
	if err := appendLine(real, "segunda"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	want := "primera\nsegunda\n"
	if string(data) != want {
		t.Errorf("log = %q, want %q: tiene que anexar", data, want)
	}
}

// TestProcessAliveCubreTodosLosEstados: qué estados implican un proceso en pie.
//
// Este valor decide si se reinicia un servicio, así que un estado mal clasificado
// significa un reinicio de algo sano o un servicio que se da por parado y nunca
// se para. Y no usa uiStatus.alive() del TUI a propósito: son paquetes distintos
// y la duplicación es lo que haría que divergieran.
func TestProcessAliveCubreTodosLosEstados(t *testing.T) {
	tests := []struct {
		status process.Status
		want   bool
		why    string
	}{
		{process.StatusRunning, true, "running: hay proceso y puerto"},
		{process.StatusPortPending, true, "port_pending: el discovery sigue en vuelo, no se toca"},
		{process.StatusNoPort, true, "no_port: el servicio no declara puerto, eso es válido"},
		{process.StatusPortUnresolved, true, "port_unresolved: el proceso vive aunque no se sepa el puerto"},
		{process.StatusStopped, false, "stopped: hay que pararlo"},
		{process.StatusUnknown, false, "unknown: no hay prueba de vida, hay que arrancarlo"},
		{process.Status(""), false, "estado vacío es ausencia de prueba"},
		{process.Status("inventado"), false, "un estado que no existe es ausencia de prueba"},
	}
	for _, tt := range tests {
		if got := processAlive(tt.status); got != tt.want {
			t.Errorf("processAlive(%q) = %v, want %v (%s)", tt.status, got, tt.want, tt.why)
		}
	}
}

// TestDryRunConServicioQueNoResuelveDaError: el plan no se publica si el plan no
// es ejecutable.
//
// Es lo que evita el peor caso de un dry run: un plan que dice "voy a arrancar
// api y web" cuando web no existe en ningún proyecto. El usuario lo lee, lo
// aprueba, y el arranque falla en la etapa.
func TestDryRunConServicioQueNoResuelveDaError(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "plano", Stages: []Stage{{Name: "s1", Services: []string{"api"}}}}
	projects := []scanner.Project{
		{Path: "/dev/web", Name: "web", Configured: true, Manifest: &manifest.Manifest{Name: "web", Command: "./web"}},
	}

	result, err := engine.DryRun(stack, projects)
	if err == nil {
		t.Fatalf("DryRun con un servicio inexistente devolvió %+v sin error", result)
	}
	if result != nil {
		t.Errorf("result = %+v con error: un plan que no se puede ejecutar no se publica a medias", result)
	}
}

// TestDryRunConAmbosValoresInvalidos: el stack sin servicios y con un nombre
// repetido no produce un plan.
//
// El caso del nombre repetido importa porque es el que el usuario escribe sin
// querer (copiar una etapa) y produce un plan con el mismo servicio dos veces, que
// al arrancar muere en la resolución por ambiguo.
func TestDryRunConStageSinServiciosDaError(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "./api"}},
	}

	// MEDIDO: una etapa SIN servicios no da error. El caparazón de "una etapa
	// tiene al menos un servicio" vive en ParseComposeFile, no en DryRun: un
	// compose escrito a mano nunca produce una etapa vacía. Duplicar la regla
	// aquí sería dar a DryRun una obligación que nadie tiene, y el test que
	// comprueba que el caparazón está donde está es TestParseStageNoServices.
	stack := &Stack{Name: "plano", Stages: []Stage{{Name: "s1"}}}
	result, err := engine.DryRun(stack, projects)
	if err != nil {
		t.Errorf("DryRun de una etapa sin servicios dio error: %v", err)
	} else if len(result.Stages) != 1 || len(result.Stages[0].Services) != 0 {
		t.Errorf("resultado = %+v: la etapa vacía se publica tal cual", result)
	}

	// Y un servicio repetido entre etapas NO es error en dry run: solo en el
	// arranque, cuando la resolución por nombre ve los duplicados.
	dup := &Stack{Name: "plano", Stages: []Stage{
		{Name: "s1", Services: []string{"api"}},
		{Name: "s2", Services: []string{"api"}},
	}}
	// Sin duplicado en projects el nombre resuelve bien: el duplicado de etapas no
	// es error por sí mismo.
	if _, err := engine.DryRun(dup, projects); err != nil {
		t.Errorf("el mismo servicio en dos etapas no es error en dry run: %v", err)
	}
}

// TestStackStatusConServicioDuplicadoLoCuentaUnaSolaVez: el conteo total es el
// número de servicios DISTINTOS del stack.
//
// Es lo que hace que la TUI pueda decir "2 de 3". Si contara las apariciones, un
// servicio que se repite en dos etapas aparecería dos veces en el total y el
// contador nunca llegaría a cuadrar con lo que el usuario ve debajo.
func TestStackStatusConServicioDuplicadoLoCuentaUnaSolaVez(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	stack := &Stack{Name: "dup", Stages: []Stage{
		{Name: "s1", Services: []string{"api", "web"}},
		{Name: "s2", Services: []string{"api"}},
	}}
	projects := []scanner.Project{
		{Path: "/dev/api", Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "./api"}},
		{Path: "/dev/web", Name: "web", Configured: true, Manifest: &manifest.Manifest{Name: "web", Command: "./web"}},
	}

	running, total, err := engine.StackStatus(stack, projects)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2: api sale en dos etapas pero es un servicio", total)
	}
	_ = running
}

// TestStackStatusConMetaDePidCeroNoCuentaComoRunning: un meta escrito pero sin
// proceso no es un servicio vivo.
//
// El `meta.Pid > 0` es lo que separa "arrancado en algún momento" de "corriendo
// ahora". Sin él, un servicio parado seguiría apareciendo como arriba en la TUI y
// `vroom stop` no lo pararía porque stopService ya lo considera muerto.
func TestStackStatusConMetaDePidCeroNoCuentaComoRunning(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	path := t.TempDir()
	if err := store.SaveMeta(path, state.Meta{State: state.StateStopped}); err != nil {
		t.Fatal(err)
	}
	projects := []scanner.Project{{Path: path, Name: "api", Configured: true, Manifest: &manifest.Manifest{Name: "api", Command: "./api"}}}

	running, total, err := engine.StackStatus(&Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"api"}}}}, projects)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	if running != 0 {
		t.Errorf("running = %d con un meta sin PID, want 0", running)
	}
}

// TestStackStatusConMetaIlegibleNoFalla: un meta que no se puede leer no es un
// servicio corriendo ni un error del status.
//
// Es el estado de un servicio que alguien está editando a mano o de una escritura
// interrumpida. Fallar el status entero dejaría al usuario sin ver el resto del
// stack, que es lo que quiere saber.
func TestStackStatusConMetaIlegibleNoFalla(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{}, store)

	// Un directorio donde debería estar el meta.
	path := t.TempDir()
	if err := os.MkdirAll(filepath.Join(path, ".."), 0o755); err != nil {
		t.Fatal(err)
	}
	projects := []scanner.Project{{Path: path, Name: "raro", Configured: true, Manifest: &manifest.Manifest{Name: "raro", Command: "./x"}}}

	running, total, err := engine.StackStatus(&Stack{Name: "s", Stages: []Stage{{Name: "e", Services: []string{"raro"}}}}, projects)
	if err != nil {
		t.Errorf("un meta que no se puede leer no puede fallar el status: %v", err)
	}
	if total != 1 || running != 0 {
		t.Errorf("running/total = %d/%d, want 0/1", running, total)
	}
}
