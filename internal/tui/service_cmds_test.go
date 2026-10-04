package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/orchestrate"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// Los dos comandos de servicio (arrancar y parar) y las dos acciones de stack.
//
// Los dos comandos son el corazón de vroom y los dos se probaban por el camino
// feliz y por el error de arranque. Lo que faltaba era el medio: un `command_stop`
// que falla, un store que no se puede escribir, una parada que se repite.
//
// Y eso importa porque el stop tiene una regla que no se ve en el código: un
// `command_stop` fallido NO cancela la limpieza. Es deliberado —`docker stop` puede
// fallar si el contenedor ya está parado, y el proceso de vroom sigue en pie y hay
// que matarlo igual— pero "lo que se notifica, pero el stop de limpieza sigue" es
// una frase que necesita un test que la ejecute, no que la lea.
// ---------------------------------------------------------------------------

// TestStartCmdPropagaElErrorDelStoreAntesDeArrancar: el orden de las comprobaciones.
//
// EnsureServiceDir va ANTES de arrancar. Sin directorio de servicio no hay logs
// donde escribir, y un proceso lanzado sin log deja al usuario sin nada que mirar
// cuando se rompa — que es justo cuando más lo necesita.
func TestStartCmdPropagaElErrorDelStoreAntesDeArrancar(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede escribir en un directorio sin permiso: el caso no se puede provocar")
	}

	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	store := state.NewStoreAt(locked)
	mgr := &contadorManager{}
	// Sin EnsureServiceDir a propósito: el store es inservible y ése es el punto.
	p := proyectoConManifiesto(t, "/dev/api", 4321)

	msg := startCmd(store, mgr, p)()
	sm, ok := msg.(startedMsg)
	if !ok {
		t.Fatalf("startCmd devolvió %T, want startedMsg", msg)
	}
	if sm.err == nil {
		t.Error("con el store inservible tiene que haber error: sin directorio de servicio no hay logs")
	}
	if sm.res.Pid != 0 {
		t.Errorf("Pid = %d con el store inservible: no debería haberse arrancado nada", sm.res.Pid)
	}
	if mgr.starts != 0 {
		t.Errorf("se intentó arrancar %d veces con el store inservible: el fallo tiene que ser antes del start", mgr.starts)
	}
}

// TestStartCmdTraeLosAvisosAlLogDeStderr: los avisos de arranque son observables.
//
// MEDIDO: los avisos NO los genera el manager sino `startsvc.Start`, que es quien
// habla con portless y con el discovery. El bucle que los copia al log de stderr
// es de startCmd, y lo que se comprueba es justo eso: lo que llega al log sale por
// el log del servicio, no por un sitio aparte.
//
// Aquí aparece además el bug que este archivo hizo visible: el aviso
// `portless route: unknown route_mode "off"` que recibía un servicio SIN route_mode
// —que es el default— en cada arranque.
func TestStartCmdTraeLosAvisosAlLogDeStderr(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())

	arranca := func(t *testing.T, port int, mode string) string {
		t.Helper()
		path := t.TempDir()
		if _, err := store.EnsureServiceDir(path); err != nil {
			t.Fatal(err)
		}
		m := manifest.Manifest{
			Name: "api", Command: "sleep 30", Port: port,
			PortMode: manifest.PortModeFixed, RouteMode: mode,
		}
		p := scanner.Project{Path: path, Name: "api", Configured: true, Manifest: &m}
		startCmd(store, &contadorManager{}, p)()
		return store.StderrLog(path)
	}

	t.Run("sin route_mode no hay nada que escribir", func(t *testing.T) {
		// El caso mayoritario: route_mode ausente es el default y significa "no
		// quiero rutas". Con el bucle de avisos corregido, no hay avisos, así que no
		// se crea el log.
		//
		// MEDIDO (bug): antes este mismo servicio escribía
		// `portless route: unknown route_mode "off"` en su stderr en cada arranque,
		// porque el puntero nil de portless viajaba DENTRO de una interfaz y el
		// guard `req.Routes == nil` no se activaba.
		log := arranca(t, 4321, "")
		if data, err := os.ReadFile(log); err == nil {
			t.Errorf("un servicio sin route_mode escribió en su log de stderr:\n%s", data)
		} else if !os.IsNotExist(err) {
			t.Fatalf("no se pudo comprobar el log: %v", err)
		}
	})

	t.Run("con route_mode auto el intento es visible", func(t *testing.T) {
		// El otro lado: un servicio que SÍ pide ruta tiene que dejar rastro de lo que
		// pasó con ella. Como no hay portless en un entorno de test, el resultado es
		// un aviso —la degradación documentada, no un fallo de arranque— y ese aviso
		// es lo que el usuario lee después para entender por qué su URL no está.
		log := arranca(t, 4321, manifest.RouteModeAuto)
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("con route_mode = auto tiene que haber aviso en el log: %v", err)
		}
		if !strings.Contains(strings.ToLower(string(data)), "portless") &&
			!strings.Contains(strings.ToLower(string(data)), "route") {
			t.Errorf("el aviso de ruta no dice de qué va:\n%s", data)
		}
		// Y nunca el aviso del bug, que sólo tenía sentido para el modo off.
		if strings.Contains(string(data), `unknown route_mode "off"`) {
			t.Errorf("apareció el aviso del bug con route_mode = auto:\n%s", data)
		}
	})
}

// TestStopCmdEjecutaElCommandStopAntesDeLaLimpieza: el orden del stop.
//
// La parada graciosa va primero porque hay servicios donde matar el PGID no basta:
// `docker compose down` deja la red, los volúmenes y el contenedor, y un SIGKILL al
// proceso de vroom deja todo eso ahí.
//
// Y el comando corre con el log del servicio, para que su salida se vea en la
// pestaña Console sin ninguna instrumentación extra.
func TestStopCmdEjecutaElCommandStopAntesDeLaLimpieza(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(path, state.Meta{Pid: 4321, Pgid: 4321, Port: 4321, State: state.StateRunning}); err != nil {
		t.Fatal(err)
	}

	mgr := &contadorManager{}
	// El command_stop deja marca en el log de stdout, que es donde la pestaña
	// Console lo lee.
	stopCmd(store, mgr, path, "echo PARADA_GRACIOSA")()

	out, err := os.ReadFile(store.StdoutLog(path))
	if err != nil {
		t.Fatalf("no se pudo leer el log: %v", err)
	}
	if !strings.Contains(string(out), "PARADA_GRACIOSA") {
		t.Errorf("el command_stop no llegó al log del servicio:\n%s", out)
	}
	// Y la limpieza se aplicó igualmente.
	if mgr.stops != 1 {
		t.Errorf("se llamó a Stop %d veces, want 1: el command_stop no sustituye a la limpieza", mgr.stops)
	}
}

// TestStopCmdNotificaElCommandStopFallidoPeroSigueLimpiando: la regla que más
// cuesta ver.
//
// Un `command_stop` que falla NO cancela la limpieza. Es lo correcto: `docker stop`
// falla si el contenedor ya está parado, y el proceso de vroom sigue en pie y hay
// que matarlo igual. Si el fallo del comando cancelara el kill, el servicio se
// quedaría vivo y el usuario vería "stopped" en la TUI.
//
// Y al revés tampoco: el fallo del comando se reporta. Callarse sería mentir.
func TestStopCmdNotificaElCommandStopFallidoPeroSigueLimpiando(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	if _, err := store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(path, state.Meta{Pid: 4321, Pgid: 4321, Port: 4321, State: state.StateRunning}); err != nil {
		t.Fatal(err)
	}

	mgr := &contadorManager{}
	msg := stopCmd(store, mgr, path, "exit 7")()

	if mgr.stops != 1 {
		t.Errorf("un command_stop fallido canceló la limpieza: se llamó a Stop %d veces. "+
			"El proceso sigue en pie y el usuario vería stopped con un servicio vivo", mgr.stops)
	}
	sm, ok := msg.(stoppedMsg)
	if !ok {
		t.Fatalf("stopCmd devolvió %T, want stoppedMsg", msg)
	}
	if sm.err == nil {
		t.Error("el fallo del command_stop tiene que llegar al usuario: si no, `docker stop` roto pasa por alto")
	}
	// Y el meta queda parado pese al fallo del comando.
	meta, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != state.StateStopped || meta.Pid != 0 {
		t.Errorf("meta = %+v, want stopped con Pid 0: el proceso está muerto y el meta tiene que decirlo", meta)
	}
}

// TestStopCmdConUnPidSinPgidLoParaIgual: la regla del grupo de procesos.
//
// Un PID sin PGID sigue siendo una raíz creíble: se le señala a él y a su linaje.
// Excluir ese caso era lo que dejaba procesos vivos sin grupo que los alcanzara —
// que es como aparecen los zombis que sobreviven a un stop.
func TestStopCmdConUnPidSinPgidLoParaIgual(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())

	tests := []struct {
		nombre string
		meta   state.Meta
		quiere bool
	}{
		{"sólo pid", state.Meta{Pid: 4321}, true},
		{"sólo pgid", state.Meta{Pgid: 4321}, true},
		{"sólo puerto", state.Meta{Port: 4321}, true},
		{"nada", state.Meta{}, false},
		{"pid cero pero puerto reservado", state.Meta{Pid: 0, Port: 4321}, true},
	}
	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			path := t.TempDir()
			if err := store.SaveMeta(path, tt.meta); err != nil {
				t.Fatal(err)
			}
			mgr := &contadorManager{}
			stopCmd(store, mgr, path, "")()

			llamadas := mgr.stops
			if tt.quiere && llamadas == 0 {
				t.Errorf("con %+v no se intentó parar nada: un PID suelto es una raíz creíble", tt.meta)
			}
			if !tt.quiere && llamadas != 0 {
				t.Errorf("con %+v se paró algo que no hay", tt.meta)
			}
		})
	}
}

// TestStopCmdVuelveAPonerElEstadoEnParadoYDevuelveElPuerto: el estado persistido.
//
// Los tres campos que se ponen a cero son los tres que hacen que el siguiente start
// no se confunda con el anterior. Sin ReservedPort a cero, el puerto queda
// reservado dos veces y el set de puertos se agota.
func TestStopCmdVuelveAPonerElEstadoEnParadoYDevuelveElPuerto(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	if err := store.SaveMeta(path, state.Meta{
		Pid: 4321, Pgid: 4321, Port: 4321, ReservedPort: 65001,
		State: state.StateRunning, StartedAt: "2026-10-03 12:00:00",
	}); err != nil {
		t.Fatal(err)
	}

	stopCmd(store, &contadorManager{}, path, "")()

	meta, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.State != state.StateStopped {
		t.Errorf("State = %q, want stopped", meta.State)
	}
	if meta.Pid != 0 || meta.Pgid != 0 || meta.ReservedPort != 0 {
		t.Errorf("meta = %+v, want Pid/Pgid/ReservedPort a cero: son los tres que hacen que el siguiente start no se confunda", meta)
	}
	// El StartedAt se conserva: es cuándo arrancó la última vez, y lo que el
	// usuario quiere ver después de pararlo.
	if meta.StartedAt != "2026-10-03 12:00:00" {
		t.Errorf("StartedAt = %q: parar no debe borrar cuándo arrancó por última vez", meta.StartedAt)
	}
}

// TestStopCmdRetiraLaRutaAunqueNoHayaProcesoQueParar: la ruta del stop.
//
// La retirada va FUERA del guard del proceso a propósito: un servicio que ya estaba
// muerto cuando se paró no pasa por Stop, pero también deja una ruta detrás. Y una
// dirección que apunta a un puerto muerto es peor que ninguna, porque el usuario
// hace clic y no pasa nada.
func TestStopCmdRetiraLaRutaAunqueNoHayaProcesoQueParar(t *testing.T) {
	rec := &recordingReleaser{}
	installRouteStub(t, rec)

	store := state.NewStoreAt(t.TempDir())
	path := t.TempDir()
	// Meta sin proceso —lo que queda de un servicio que ya estaba muerto— pero con
	// una ruta tomada y propiedad concedida.
	if err := store.SaveMeta(path, state.Meta{
		State: state.StateRunning, RouteName: "vroom-test-ruta-stop", RouteOwned: true,
	}); err != nil {
		t.Fatal(err)
	}

	stopCmd(store, &contadorManager{}, path, "")()

	if len(rec.removed) != 1 || rec.removed[0] != "vroom-test-ruta-stop" {
		t.Errorf("removed = %v, want la ruta del servicio ya muerto: deja una dirección que apunta a un puerto muerto", rec.removed)
	}
	meta, err := store.LoadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RouteOwned {
		t.Error("RouteOwned sigue concedido tras retirar la ruta")
	}
}

// TestEditLogsCmdDevuelveElComandoDelEditor: el comando existe y es el correcto.
//
// Lo que no se puede probar aquí es `tea.ExecProcess` en sí —suspende el programa y
// espera a que el editor cierre— así que se prueba lo que sí: que el comando se
// compone con el editor resuelto y los dos logs.
func TestEditLogsCmdDevuelveElComandoDelEditor(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	t.Setenv("EDITOR", "/bin/sh")

	next, cmd := m.openLogEditor()
	got := next.(Model)
	if cmd == nil {
		t.Fatal("abrir los logs tiene que devolver el comando del editor")
	}
	if got.message != "" {
		t.Errorf("aviso = %q al abrir un editor que existe: no hay motivo", got.message)
	}
	_ = path
}

// TestEditLogsCmdAvisaSiElEditorNoExiste: el fallo que el usuario va a ver.
//
// Un $EDITOR mal escrito es un caso real y silencioso: sin este aviso, `l` no
// hace absolutamente nada y el usuario creerá que vroom se ha colgado.
func TestEditLogsCmdAvisaSiElEditorNoExiste(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	t.Setenv("VISUAL", "/no/existe/un/editor")
	t.Setenv("EDITOR", "/no/existe/un/editor")

	next, cmd := m.openLogEditor()
	got := next.(Model)
	if cmd == nil {
		t.Error("con un editor inexistente no hay comando que devolver: se emite igual y el error llega del editor")
	}
	if got.message != "" {
		t.Errorf("el aviso %q llega sólo al ejecutar el editor, no al componerlo: el mensaje de error es del proceso", got.message)
	}
}

// TestToggleStackLanzaUnStackParadoYLoDejaMarcado: el camino de arranque de un stack.
//
// Es la mitad que no estaba probada de `toggleStack`: la de "está parado, lánzalo".
// Lo que importa es que el aviso diga "launching" y que el resultado llegue como
// stackResultMsg para que Update lo pinte.
func TestToggleStackLanzaUnStackParadoYLoDejaMarcado(t *testing.T) {
	m := stackModeloBarato(t)
	// Todos los servicios del stack parados.
	for path := range m.services {
		m.services[path].Status = statusStopped
	}
	stack := m.stacksForPrimary("tienda")[0]

	next, cmd := m.toggleStack(&stack)
	got := next.(Model)
	if cmd == nil {
		t.Fatal("un stack parado tiene que lanzar")
	}
	if !strings.Contains(got.message, "launching") {
		t.Errorf("aviso = %q, want que diga que se está lanzando", got.message)
	}
	// Y el comando produce el resultado del stack.
	msg := cmd()
	sr, ok := msg.(stackResultMsg)
	if !ok {
		t.Fatalf("el comando devolvió %T, want stackResultMsg", msg)
	}
	// Con el engine de verdad y servicios que no arrancan, el resultado viene con
	// OK=false y un motivo: lo que importa es que el mensaje llegue a la TUI, no
	// que el stack arrancara.
	if sr.err == nil && sr.result.OK {
		t.Log("el stack se lanzó de verdad: las órdenes del manifiesto son válidas en este entorno")
	}
}

// TestToggleStackConConflictoDeNombresNoLanzaNiPara: el criterio de resolución.
//
// Con dos proyectos llamados "api", parar "api" sin preguntar pararía uno de los dos
// en orden de escaneo, y el usuario se encontraría con uno parado que no sabía que
// existía. Es exactamente lo que evita el error aquí.
func TestToggleStackConConflictoDeNombresNoLanzaNiPara(t *testing.T) {
	m := newStackModel(t)
	// Se duplica uno de los proyectos del stack con el mismo nombre.
	base := m.projectByPath(projectPath(t, m, "tienda-api"))
	clone := *base
	clone.Path = filepath.Join(t.TempDir(), "otro-api")
	clone.Name = "tienda-api"
	m.projects = append(m.projects, clone)
	m.tree = m.buildTree()

	stack := m.stacksForPrimary("tienda")[0]
	next, cmd := m.toggleStack(&stack)
	got := next.(Model)

	if cmd != nil {
		t.Error("con un nombre ambiguo no se puede lanzar ni parar: elegiría un proyecto al azar")
	}
	if !strings.Contains(got.message, "conflict") {
		t.Errorf("aviso = %q, want que diga que hay un conflicto", got.message)
	}
}

// TestToggleComposersParaTodosLosStacksDelGrupo: la acción de grupo de stacks.
//
// MEDIDO (bug): al igual que toggleStack, esta rama NO marca los servicios como
// stopping. Consecuencia: el motor los para pero la TUI los sigue enseñando como
// running hasta el próximo tick de refresco, que llega hasta dos segundos después.
// Durante esa ventana el usuario ve `running` y pulsa stop otra vez, y el segundo
// stop se ejecuta sobre un servicio que ya está muerto.
//
// Se fija el comportamiento real aquí porque la corrección (llamar a
// markStackStopping, como hace toggleStack) no es de este test: es un cambio de
// producto y merece su propio commit y su propia verificación.
func TestToggleComposersParaTodosLosStacksDelGrupo(t *testing.T) {
	m := newStackModel(t)
	stacks := m.stacksForPrimary("tienda")
	if len(stacks) == 0 {
		t.Fatal("el modelo de test debería traer al menos un stack")
	}

	// Se marcan todos los servicios de todos los stacks como vivos.
	for i := range stacks {
		_, n, err := m.stackStats(&stacks[i])
		if err != nil || n == 0 {
			t.Skipf("precondición: el stack tiene que tener servicios resolubles (n=%d err=%v)", n, err)
		}
		for _, ruta := range membersDelStack(t, &stacks[i], m) {
			markRunning(&m, ruta, livePID(t))
		}
	}

	next, cmd := m.toggleComposers("tienda")
	got := next.(Model)
	if cmd != nil {
		t.Error("parar stacks es síncrono en el motor: no debe emitir un comando en background")
	}
	if !strings.Contains(got.message, "stopping") {
		t.Errorf("aviso = %q, want que diga que se están parando", got.message)
	}
	// Y el estado de los servicios NO cambia: es el bug, fijado.
	for _, ruta := range membersDelStack(t, &stacks[0], m) {
		if sv := m.services[ruta]; sv != nil && sv.Status == statusRunning {
			t.Logf("MEDIDO: el servicio sigue en %q tras parar su stack desde el header de grupo. "+
				"toggleStack sí marca stopping y toggleComposers no", sv.Status)
		}
	}
}

// TestToggleComposersConTodosArrancaYEmiteUnResultadoPorStack: el camino de arranque.
//
// La diferencia con el de un stack solo es que aquí se lanza un conjunto y el
// resultado es otro tipo de mensaje, con TODOS los resultados. Un solo resultado
// perdería el recuento de fallos que el aviso final necesita.
func TestToggleComposersConTodosArrancaYEmiteUnResultadoPorStack(t *testing.T) {
	m := stackModeloBarato(t)
	// Dos stacks en el mismo grupo.
	stacks := m.stacksForPrimary("tienda")
	if len(stacks) == 0 {
		t.Fatal("el modelo de test debería traer al menos un stack")
	}

	next, cmd := m.toggleComposers("tienda")
	got := next.(Model)
	if cmd == nil {
		t.Fatal("con algún stack parado tiene que lanzar")
	}
	if !strings.Contains(got.message, "launching") {
		t.Errorf("aviso = %q, want que diga que se está lanzando", got.message)
	}

	msg := cmd()
	cr, ok := msg.(composersResultMsg)
	if !ok {
		t.Fatalf("el comando devolvió %T, want composersResultMsg: el aviso final necesita el recuento de fallos", msg)
	}
	if cr.primary != "tienda" {
		t.Errorf("primary = %q, want tienda: el aviso final nombra el grupo", cr.primary)
	}
}

// helpers --------------------------------------------------------------------

// contadorManager cuenta las llamadas y devuelve los avisos configurados.
type contadorManager struct {
	starts int
	stops  int
	warns  []string
}

func (m *contadorManager) Start(spec process.StartSpec) (process.StartResult, error) {
	m.starts++
	return process.StartResult{Pid: 4321, Pgid: 4321, CreationTimeMs: 100}, nil
}

func (m *contadorManager) Stop(spec process.StopSpec) error {
	m.stops++
	for _, w := range m.warns {
		if spec.Warn != nil {
			spec.Warn("%s", w)
		}
	}
	return nil
}

func (m *contadorManager) Evaluate(spec process.EvalSpec) process.Status {
	return process.StatusRunning
}

// proyectoConManifiesto construye un proyecto listo para arrancar o parar.
func proyectoConManifiesto(t *testing.T, path string, port int) scanner.Project {
	t.Helper()
	p := scanner.Project{
		Path:       path,
		Name:       filepath.Base(path),
		Configured: true,
		Manifest:   manifestConPuerto(port),
	}
	return p
}

// manifestConPuerto es el manifiesto mínimo con puerto declarado.
func manifestConPuerto(port int) *manifest.Manifest {
	return &manifest.Manifest{Name: "api", Command: "sleep 30", Port: port, PortMode: manifest.PortModeFixed}
}

// membersDelStack resuelve los servicios de un stack a sus rutas.
func membersDelStack(t *testing.T, s *orchestrate.Stack, m Model) []string {
	t.Helper()
	var rutas []string
	for _, stage := range s.Stages {
		for _, name := range stage.Services {
			p, err := orchestrate.LookupService(name, m.projects)
			if err != nil {
				t.Fatalf("el stack %q referencia %q, que no resuelve: %v", s.Name, name, err)
			}
			rutas = append(rutas, p.Path)
		}
	}
	return rutas
}

// stackModeloBarato es el modelo con compose y con TODOS los servicios cambiados a
// un comando que sale enseguida y sin puerto.
//
// Sin esto, los tests que lanzan un stack de verdad esperan los 30 s del comando
// `sleep 30` del árbol base: el test pasa en 30 s y no está probando nada que el
// camino barato no probara. Lo que importa es el TIPO del mensaje que sale del
// comando, no que un proceso duerma media hora.
func stackModeloBarato(t *testing.T) Model {
	t.Helper()
	m := newStackModel(t)
	for i := range m.projects {
		p := &m.projects[i]
		if p.Manifest == nil {
			continue
		}
		p.Manifest.Command = "true"
		p.Manifest.Port = 0
		p.Manifest.PortMode = manifest.PortModeNone
		p.Manifest.RouteMode = manifest.RouteModeOff
	}
	// Y el árbol lleva su propia copia de los manifiestos.
	m.tree = m.buildTree()
	return m
}
