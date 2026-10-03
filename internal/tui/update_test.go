package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"charm.land/bubbles/v2/spinner"

	"vroom/internal/orchestrate"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// El bucle de Update, mensaje por mensaje.
//
// Update es un switch sobre tipos de msg, y cada case es una regla de la TUI que
// sólo se verifica si el msg LLEGA. Bubbletea entrega lo que los Cmd devuelven,
// así que un case mal escrito —un aviso que no sale, un estado que no se
// actualiza— no falla: la TUI simplemente muestra otra cosa.
//
// Por eso estos tests construyen el msg a mano y comprueban el efecto sobre el
// modelo, que es lo que la vista va a renderizar. No usan teatest porque no hace
// falta un Bubbletea corriendo: Update es una función pura de (modelo, msg) a
// (modelo, cmd), y eso es exactamente lo que se puede comprobar directamente.
// ---------------------------------------------------------------------------

// updateMsg aplica un msg al modelo y devuelve el modelo resultante. Es el
// CICLO de Bubbletea sin Bubbletea.
func updateMsg(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update devolvió %T, want Model", next)
	}
	return got
}

// TestUpdateRefreshedIgnoraLoQueNoEstabaEnElArbol: el polling puede devolver un
// servicio que ya no está en el modelo (borrado del disco entremedias), y eso no
// puede recrear la fila.
//
// Si lo recreara, el modelo crecería en cada pollsolto con filas de servicios que
// ya no existen, y el árbol de la TUI mostraría proyectos fantasma. Es un caso
// real porque el escaneo y el poll no están sincronizados.
func TestUpdateRefreshedIgnoraLoQueNoEstabaEnElArbol(t *testing.T) {
	m, _ := newTestModel(t)
	before := len(m.services)

	got := updateMsg(t, m, refreshedMsg{results: map[string]refreshResult{
		"/ruta/que/no/existe": {status: process.StatusRunning},
	}})

	if len(got.services) != before {
		t.Errorf("el poll creó %d filas nuevas: un servicio borrado del disco no puede volver", len(got.services)-before)
	}
	if got.services["/ruta/que/no/existe"] != nil {
		t.Error("se inventó un servicio que el poll mención y el modelo no conoce")
	}
}

// TestUpdateRefreshedIgnoraLosTransitoriosYPropagaLaRama: los estados starting y
// stopping los resuelven startedMsg/stoppedMsg, no el poll.
//
// Si el poll los sobrescribiera, un servicio que está arrancando aparecería como
// parado durante todo el arranque — y el usuario vería el servicio "apagado"
// mientras se levanta—, y uno que se está parando aparecería como vivo. La regla
// es que el poll no pisa lo transitorio.
func TestUpdateRefreshedIgnoraLosTransitoriosYPropagaLaRama(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")
	m.services[path].Status = statusStarting

	got := updateMsg(t, m, refreshedMsg{results: map[string]refreshResult{
		path: {status: process.StatusStopped, branch: "feature/nueva"},
	}})

	if got.services[path].Status != statusStarting {
		t.Errorf("el poll pisó un estado transitorio: %q -> %q", statusStarting, got.services[path].Status)
	}
	// La rama sí se propaga siempre: un `git branch -m` tiene que notarse aunque
	// el servicio esté arrancando.
	if got.branches[path] != "feature/nueva" {
		t.Errorf("branches[%s] = %q, want feature/nueva: la rama se propaga aunque el estado sea transitorio", path, got.branches[path])
	}
}

// TestUpdateRefreshedMuestraSoloElPrimerAviso: el poll trae un aviso por servicio
// ilegible, y la barra de estado sólo tiene sitio para uno.
//
// Se muestra el primero y se corta, a propósito: cinco avisos a la vez empujarían
// el que más importa (el de más arriba) fuera de la barra. El orden del mapa es
// aleatorio, así que lo que se afirma es el COMPORTAMIENTO —uno solo— y no cuál.
func TestUpdateRefreshedMuestraSoloElPrimerAviso(t *testing.T) {
	m, _ := newTestModel(t)
	paths := []string{
		projectPath(t, m, "tienda-api"),
		projectPath(t, m, "tienda-web"),
		projectPath(t, m, "suelto"),
	}
	results := map[string]refreshResult{}
	for i, p := range paths {
		results[p] = refreshResult{status: process.StatusStopped, warn: "aviso-" + string(rune('a'+i))}
	}

	got := updateMsg(t, m, refreshedMsg{results: results})

	if got.message == "" {
		t.Fatal("un aviso del poll no llegó a la barra de estado")
	}
	if !strings.HasPrefix(got.message, "aviso-") {
		t.Errorf("el mensaje no es ninguno de los avisos: %q", got.message)
	}
	if got.messageExpiresAt.IsZero() {
		t.Error("un aviso sin caducidad se queda pegado para siempre y tapa el siguiente")
	}
}

// TestUpdateStartedConErrorDejaElServicioParadoYLoDice: un arranque fallido deja
// la fila en parado y un aviso que nombra el error.
//
// Las dos mitades: el estado, para no seguir mostrando un servicio vivo que no
// existe, y el aviso, para que el error no se pierda. Un estado sin aviso deja al
// usuario con un servicio que no arranca y sin explicación.
func TestUpdateStartedConErrorDejaElServicioParadoYLoDice(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")
	m.services[path].Status = statusStarting

	got := updateMsg(t, m, startedMsg{path: path, err: errors.New("no such file or directory")})

	if got.services[path].Status != statusStopped {
		t.Errorf("tras un arranque fallido el servicio quedó en %q, want stopped", got.services[path].Status)
	}
	if !strings.Contains(got.message, "error starting") || !strings.Contains(got.message, "no such file") {
		t.Errorf("el aviso no dice qué falló: %q", got.message)
	}
}

// TestUpdateStartedConExitoFijaElEstadoYLimpiaLaConsola: un arranque OK fija el
// estado desde el Meta, registra el evento y TRUNCA la consola en memoria.
//
// La limpieza de la consola es lo que evita que se vea contenido viejo: los logs
// se truncan en el arranque, y si el buffer en memoria no se limpia, la primera
// línea que se ve después de arrancar es la del servicio anterior. Es un bug de
// lectura, no de escritura, y sólo se nota si se mira el buffer.
func TestUpdateStartedConExitoFijaElEstadoYLimpiaLaConsola(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")
	m.services[path].Status = statusStarting

	// Contenido viejo en la consola, y un log en disco de la ejecución previa.
	cs := m.consoleStateFor(path)
	cs.stdout = "SALIDA DEL SERVICIO ANTERIOR\n"
	cs.stderr = "ERROR DEL SERVICIO ANTERIOR\n"
	cs.off[0], cs.off[1] = 999, 999

	got := updateMsg(t, m, startedMsg{
		path: path,
		res:  process.StartResult{Pid: 4242, Pgid: 4242},
		meta: state.Meta{Name: "tienda-api", Pid: 4242, Pgid: 4242, State: state.StateRunning},
	})

	if got.services[path].Status != statusRunning {
		t.Errorf("Status = %q tras un arranque OK, want running", got.services[path].Status)
	}
	if got.services[path].Meta.Pid != 4242 {
		t.Errorf("Meta.Pid = %d, want 4242", got.services[path].Meta.Pid)
	}

	// Los offsets vuelven a lo que hay REALMENTE en el log, que tras el truncado
	// del arranque es 0.
	gotCS := got.consoleStateFor(path)
	if gotCS.stdout != "" || gotCS.stderr != "" || gotCS.merged != "" {
		t.Errorf("la consola en memoria no se limpió: %q / %q", gotCS.stdout, gotCS.stderr)
	}
	if gotCS.off[0] != 0 || gotCS.off[1] != 0 {
		t.Errorf("los offsets quedaron en %d/%d: con el log truncado hay que releer desde 0", gotCS.off[0], gotCS.off[1])
	}

	// Y se registró el evento, que es lo que aparece en la lista de actividad.
	if len(got.events[path]) == 0 {
		t.Error("un arranque correcto no dejó evento: el usuario no ve que arrancó nada")
	}
}

// TestUpdateStartedConAvisosNoBloqueanElServicio: los warnings del arranque son
// visibles pero NO bloqueantes, y sale el ÚLTIMO.
//
// MEDIDO: notify sobrescribe, así que de varios avisos sólo queda el último. Se
// fija el comportamiento real y no el ideal a propósito: cambiarlo a "concatenar"
// metería un texto de tres líneas en una barra de una línea, y el primero —que
// suele ser la causa— desaparecería igual. El resto de avisos no se pierde: van al
// log de stderr del servicio, que es donde se leen los avisos de un arranque largo.
//
// Lo que NO se negocia es el estado: con la ruta degradada el servicio OPERA, y
// dejarlo en un estado de error haría que el usuario creyera que no arrancó.
func TestUpdateStartedConAvisosNoBloqueanElServicio(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")

	got := updateMsg(t, m, startedMsg{
		path:  path,
		meta:  state.Meta{Name: "tienda-api", Pid: 4242, State: state.StateRunning},
		warns: []string{"portless not found", "route degraded"},
	})

	if got.message != "route degraded" {
		t.Errorf("message = %q: notify sobrescribe, así que el último aviso es el que queda", got.message)
	}
	if got.messageExpiresAt.IsZero() {
		t.Error("un aviso sin caducidad tapa los siguientes para siempre")
	}
	if got.services[path].Status != statusRunning {
		t.Errorf("un warning dejó el servicio en %q: el servicio OPERA con la ruta degradada", got.services[path].Status)
	}
}

// TestUpdateStoppedConErrorNoRelanzaNiBorraElPendiente: un stop fallido deja el
// servicio en parado, avisa, y NO borra el pendingRestart.
//
// El pendingRestart es lo que encadena stop → start. Si un stop fallido lo
// borrara, el reinicio se perdería en silencio: el usuario pidió reiniciar y la
// TUI aceptaría y no haría nada. Es el peor resultado, porque parece que el
// reinicio ocurrió.
func TestUpdateStoppedConErrorNoRelanzaNiBorreElPendiente(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")
	m.pendingRestart[path] = true
	m.services[path].Status = statusStopping

	got := updateMsg(t, m, stoppedMsg{path: path, err: errors.New("kill failed")})

	if got.services[path].Status != statusStopped {
		t.Errorf("Status = %q tras un stop fallido, want stopped", got.services[path].Status)
	}
	if !strings.Contains(got.message, "error stopping") {
		t.Errorf("el aviso no dice que falló el stop: %q", got.message)
	}
	if _, still := got.pendingRestart[path]; !still {
		t.Error("un stop fallido borró el pendingRestart: el reinicio se perdería en silencio")
	}
}

// TestUpdateStoppedDisparaElReinicioCuandoEstabaPendiente: el encadenado
// stop → start, que es lo que hace "reiniciar".
//
// Y lo que se comprueba es el estado INTERMEDIO que existe de verdad: el
// servicio pasa por starting, que es lo que muestra el spinner de arranque. Un
// estado intermedio mal puesto hace que el usuario vea "corriendo" mientras se
// está arrancando, y para eso existe el spinner.
func TestUpdateStoppedDisparaElReinicioCuandoEstabaPendiente(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")
	m.pendingRestart[path] = true
	m.services[path].Status = statusStopping

	out, cmd := m.Update(stoppedMsg{path: path})
	got := out.(Model)

	if cmd == nil {
		t.Fatal("un stop limpio con reinicio pendiente debería encadenar el arranque")
	}
	if _, still := got.pendingRestart[path]; still {
		t.Error("el pendingRestart no se borró: el siguiente stop volvería a reiniciar")
	}
	if got.services[path].Status != statusStarting {
		t.Errorf("Status = %q entre el stop y el arranque, want starting: es lo que muestra el spinner", got.services[path].Status)
	}
	if len(got.events[path]) == 0 {
		t.Error("el reinicio no dejó evento de 'restart'")
	}
}

// TestUpdateStoppedSinPendienteSoloMarcaParado: sin reinicio pendiente, un stop
// limpio es sólo un stop.
//
// Y no devuelve Cmd: nada que arrancar. Un Cmd aquí arrancaría el servicio que
// el usuario acaba de parar, que es el peor bug posible en este camino.
func TestUpdateStoppedSinPendienteSoloMarcaParado(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")
	m.services[path].Status = statusStopping

	out, cmd := m.Update(stoppedMsg{path: path})
	got := out.(Model)

	if cmd != nil {
		t.Error("un stop sin reinicio pendiente no debe arrancar nada")
	}
	if got.services[path].Status != statusStopped {
		t.Errorf("Status = %q, want stopped", got.services[path].Status)
	}
}

// TestUpdateJobResultNotificaElCodigoReal: el resultado de un one-shot se refleja
// en la barra con el código que salió, no con un "algo falló".
//
// Los tres casos producen tres textos distintos a propósito: un ok con el tiempo,
// un exit≠0 con el código, y un error de lanzamiento sin código. Fundir el
// segundo y el tercero haría que un fallo de disco pareciera un build roto.
func TestUpdateJobResultNotificaElCodigoReal(t *testing.T) {
	tests := []struct {
		name string
		msg  jobMsg
		want []string
	}{
		{
			name: "ok",
			msg:  jobMsg{path: "/x", kind: "build", elapsed: 2 * time.Second},
			want: []string{"build ok", "2s"},
		},
		{
			name: "el comando falla con su código",
			msg:  jobMsg{path: "/x", kind: "build", exitCode: 3, elapsed: time.Second},
			want: []string{"build failed", "exit 3"},
		},
		{
			name: "el comando no se pudo lanzar",
			msg:  jobMsg{path: "/x", kind: "build", err: errors.New("no such file"), elapsed: 0},
			want: []string{"build error", "no such file"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newJobsTestModel(t)
			m.jobs[tt.msg.path] = "build" // el proyecto estaba bloqueado

			got := updateMsg(t, m, tt.msg)

			for _, want := range tt.want {
				if !strings.Contains(got.message, want) {
					t.Errorf("el aviso %q no menciona %q", got.message, want)
				}
			}
			// Y el bloqueo se libera SIEMPRE: si no, el proyecto queda con el
			// candado puesto y no se puede volver a arrancar.
			if _, still := got.jobs[tt.msg.path]; still {
				t.Error("el proyecto sigue bloqueado tras el job: no se puede volver a arrancar")
			}
			// Y se registró el evento con el resultado, para que la lista de
			// actividad distinga un build ok de uno fallido.
			if len(got.events) == 0 {
				t.Error("el job no dejó evento")
			}
		})
	}
}

// TestUpdateJobResultRegistraElFallidoConSuCodigo: la actividad distingue el
// éxito del fallo, y guarda el código.
//
// Un historial donde un build fallido aparece igual que uno correcto es peor que
// no tener historial: el usuario lo lee como que todo va bien.
func TestUpdateJobResultRegistraElFallidoConSuCodigo(t *testing.T) {
	m, _ := newJobsTestModel(t)
	path := projectPath(t, m, "tienda-api")

	ok := updateMsg(t, m, jobMsg{path: path, kind: "build", command: "make", elapsed: time.Second})

	// OJO: los mapas de Model se COMPARTEN entre copias del valor receptor, así
	// que hay que copiar el slice antes del segundo update. Sin esta copia los dos
	// lados ven los dos eventos y el test pasa sin comprobar nada.
	okEvents := append([]timelineEvent(nil), ok.events[path]...)
	if len(okEvents) != 1 {
		t.Fatalf("hay %d eventos tras un build, want 1", len(okEvents))
	}
	if !okEvents[0].OK {
		t.Error("un build correcto no quedó registrado como ok")
	}

	ko := updateMsg(t, ok, jobMsg{path: path, kind: "build", command: "make", exitCode: 2, elapsed: time.Second})
	koEvents := ko.events[path]
	if len(koEvents) != 2 {
		t.Fatalf("hay %d eventos tras dos builds, want 2", len(koEvents))
	}
	if koEvents[1].OK {
		t.Error("un build con exit 2 quedó registrado como ok")
	}
	if koEvents[1].Kind != "build" {
		t.Errorf("Kind = %q, want build: sin el kind el timeline no dice qué se ejecutó", koEvents[1].Kind)
	}
}

// TestUpdateStatusMsgSoloMuestraElMensaje: statusMsg es el canal de avisos que
// usan el editor de logs y el resto de comandos sin estado propio.
//
// Lo que se comprueba es que no cambia NADA más: si un statusMsg tocase el estado
// de un servicio, un "editor closed" podría parar un servicio.
func TestUpdateStatusMsgSoloMuestraElMensaje(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")
	before := *m.services[path]

	got := updateMsg(t, m, statusMsg{message: "editor closed"})

	if got.message != "editor closed" {
		t.Errorf("message = %q, want el texto del aviso", got.message)
	}
	if *got.services[path] != before {
		t.Errorf("un statusMsg tocó el estado del servicio: %+v -> %+v", before, *got.services[path])
	}
}

// TestUpdateThreadsMsgAplicaAlServicioYToleraElError: el muestreo de hilos se
// atribuye a UN servicio y su error no rompe nada.
//
// El error es routine: threadsCmd se lanza desde el tick para cualquier servicio
// que el poll acaba de dar por vivo, y el proceso puede morir entre medias. Si eso
// fuera un error visible, la TUI parpadearía con avisos de servicios que se
// paran solos.
func TestUpdateThreadsMsgAplicaAlServicioYToleraElError(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")
	markRunning(&m, path, 4242)

	got := updateMsg(t, m, threadsMsg{
		path:    path,
		threads: []process.ThreadInfo{{TID: 1, Name: "main"}},
	})
	if len(got.threads[path]) != 1 {
		t.Errorf("los hilos no se atribuyeron a su servicio: %v", got.threads[path])
	}

	// Con error: no hay hilos, y el modelo sigue usable.
	got = updateMsg(t, got, threadsMsg{path: path, err: errors.New("no such process")})
	if len(got.threads[path]) != 0 {
		t.Errorf("un muestreo fallido dejó hilos: %v", got.threads[path])
	}
}

// TestUpdateHealthMsgGuardaElResultadoPorServicio: el resultado de health se
// guarda por path, y no se mezcla entre servicios.
//
// Mezclarlos sería un bug de lectura primero: la salud de `web` aparecería en la
// fila de `api`, y el usuario leería "sano" de un servicio que no lo está.
func TestUpdateHealthMsgGuardaElResultadoPorServicio(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")

	got := updateMsg(t, m, healthMsg{path: path, r: &healthResult{StatusCode: 200}})
	if _, ok := got.healthRes[path]; !ok {
		t.Fatal("el resultado de health no se guardó por servicio")
	}
	if len(got.healthRes) != 1 {
		t.Errorf("hay %d resultados de health, want 1: uno por servicio", len(got.healthRes))
	}
}

// TestUpdateTickProgramaElSiguienteYRespetaElExpirado: el tick re-arma el reloj y
// LIMPIA el mensaje cuando ha caducado.
//
// La caducidad es lo que evita que un aviso se quede pegado encima del árbol. Y
// un mensaje sin caducidad no se limpia nunca, porque `IsZero()` es la condición
// del if — un mensaje con caducidad cero se queda para siempre.
func TestUpdateTickProgramaElSiguienteYRespetaElExpirado(t *testing.T) {
	m, _ := newTestModel(t)
	m.message = "aviso viejo"
	m.messageExpiresAt = time.Now().Add(-time.Second) // ya caducado

	out, cmd := m.Update(tickMsg(time.Now()))
	got := out.(Model)

	if cmd == nil {
		t.Error("el tick no re-armó el reloj: la TUI se quedaría congelada tras el primer tick")
	}
	if got.message != "" {
		t.Errorf("un mensaje caducado no se limpió: %q", got.message)
	}
	if !got.messageExpiresAt.IsZero() {
		t.Error("el mensaje caducado dejó su caducidad puesta")
	}

	// Y uno que aún no ha caducado NO se limpia: el polling corre cada 2s y un
	// aviso de 3s tiene que sobrevivir a dos o tres ticks.
	m2, _ := newTestModel(t)
	m2.message = "aviso nuevo"
	m2.messageExpiresAt = time.Now().Add(3 * time.Second)
	got2 := updateMsg(t, m2, tickMsg(time.Now()))
	if got2.message != "aviso nuevo" {
		t.Errorf("un mensaje sin caducar se borró: %q", got2.message)
	}
}

// TestUpdateConsoleTickReArmaElTailSoloConServicioEnConsola: el tick de consola
// re-arma el reloj siempre, y pide tail sólo con un servicio seleccionado.
//
// Re-armarlo siempre es lo que mantiene vivo el reloj. Pedir tail sin selección
// sería leer un log que nadie está mirando.
func TestUpdateConsoleTickReArmaElTailSoloConServicioEnConsola(t *testing.T) {
	m, _ := newTestModel(t)

	// Con un proyecto configurado seleccionado en la pestaña de consola: tail.
	sel := moveCursorTo(t, m, "tienda-api")
	sel.activeTab = tabConsole
	_, cmd := sel.Update(consoleTickMsg(time.Now()))
	if cmd == nil {
		t.Error("el tick de consola no devolvió Cmd: sin re-armar el reloj la consola se congela")
	}
	// Y con la pestaña de consola y un servicio seleccionado, el batch pide el
	// tail además de re-armar el reloj.
	var sawTail bool
	for _, msg := range collectBatch(t, cmd) {
		if _, ok := msg.(consoleDeltaMsg); ok {
			sawTail = true
		}
	}
	if !sawTail {
		t.Error("con la pestaña de consola no se pidió el tail: el log no avanzaría solo")
	}

	// Sin selección (cursor en un header): no hay tail, pero tampoco reloj muerto.
	onHeader := m
	onHeader.cursor = findPrimary(onHeader, "tienda")
	if _, cmd := onHeader.Update(consoleTickMsg(time.Now())); cmd == nil {
		t.Error("el tick de consola debe re-armar el reloj aunque no haya nada que leer")
	}
}

// TestUpdateConsoleDeltaSoloAvanzaElOffsetDelFlujoQueSeLeyo: el delta de consola
// avanza los offsets SÓLO de los flujos que se pudieron leer.
//
// Si un offset avanzara con su flujo en error, los bytes de ese flujo se
// perderían para siempre: el siguiente tick leería desde más allá y el usuario no
// vería nunca esas líneas. Es la asymmetrical del error que hace que "no se puede
// leer" deba significar "no se avanza".
func TestUpdateConsoleDeltaSoloAvanzaElOffsetDelFlujoQueSeLeyo(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")
	cs := m.consoleStateFor(path)
	cs.off[0], cs.off[1] = 10, 20

	got := updateMsg(t, m, consoleDeltaMsg{
		path:   path,
		stdout: "salida nueva",
		stderr: "error nuevo",
		offS:   99,
		offE:   99,
		errS:   errors.New("stdout ilegible"),
	})

	newCS := got.consoleStateFor(path)
	if newCS.off[0] != 10 {
		t.Errorf("offS avanzó a %d con un error de lectura: los bytes de stdout se perderían", newCS.off[0])
	}
	if newCS.off[1] != 99 {
		t.Errorf("offE = %d, want 99: el flujo que sí se leyó avanza su offset", newCS.off[1])
	}
	if !strings.Contains(newCS.stderr, "error nuevo") {
		t.Errorf("el stderr leído no llegó al buffer: %q", newCS.stderr)
	}

	// Y un delta con salida para un servicio que no está en el modelo no crea
	// estado: no hay fila a la que pegárselo.
	got = updateMsg(t, m, consoleDeltaMsg{path: "/no/existe", stdout: "x", offS: 1})
	if len(got.services) != len(m.services) {
		t.Error("un delta de consola creó una fila de servicio nueva")
	}
}

// TestUpdateWindowSizeReajustaElArbolYLaTerminal: al cambiar de tamaño hay que
// reajustar tres cosas, y las tresImportan.
//
// El cursor fuera de la ventana es el peor de los tres: el usuario ve el árbol y
// no ve dónde está, y las teclas van al servicio equivocado.
func TestUpdateWindowSizeReajustaElArbolYLaTerminal(t *testing.T) {
	m, _ := newTestModel(t)
	// Cursor en el final del árbol, con ventana minúscula: fuera de pantalla.
	m.cursor = len(m.tree) - 1

	got := updateMsg(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	if got.width != 100 || got.height != 30 {
		t.Errorf("dimensiones = %dx%d, want 100x30", got.width, got.height)
	}
	if got.cursor < got.treeTop || got.cursor >= got.treeTop+got.treeVis() {
		t.Errorf("el cursor (%d) quedó fuera de la ventana [%d, %d): las teclas irían al servicio equivocado",
			got.cursor, got.treeTop, got.treeTop+got.treeVis())
	}
	if got.bodyH <= 0 {
		t.Errorf("bodyH = %d tras un resize: el layout no se recalculó", got.bodyH)
	}
}

// TestUpdateSpinnerTickReArmaSoloElSpinnerQueCorresponde: un tick de spinner
// re-arma exactamente un spinner, el que le pone el id.
//
// MEDIDO: Update pasa el MISMO msg a los dos spinners, y el segundo lo ignora
// porque su id no casa. Y es lo correcto: los dos spinners tienen ids distintos
// precisamente para eso, y el de arranque sólo debe avanzar cuando hay un
// servicio arrancándose. Si el Update los moviera a los dos, el spinner de
// arranque consumiría frames sin ningún servicio en él.
//
// No se compara el frame porque es un campo privado del paquete spinner: lo que
// se comprueba es el contrato observable —un tick de vuelta por cada tick
// recibido, y nunca dos— que es lo que mantiene la animación viva sin saltarse
// frames.
func TestUpdateSpinnerTickReArmaSoloElSpinnerQueCorresponde(t *testing.T) {
	t.Run("el tick del spinner de estado re-arma uno", func(t *testing.T) {
		m, _ := newTestModel(t)

		_, cmd := m.Update(tickOf(m.spinner.Tick))
		if cmd == nil {
			t.Fatal("el tick de spinner no devolvió Cmd: la animación se congela en un frame")
		}
		var ticks int
		for _, got := range collectBatch(t, cmd) {
			if _, ok := got.(spinner.TickMsg); ok {
				ticks++
			}
		}
		if ticks != 1 {
			t.Errorf("un tick produjo %d ticks de vuelta, want 1", ticks)
		}
	})

	t.Run("el tick del spinner de arranque también", func(t *testing.T) {
		m, _ := newTestModel(t)

		_, cmd := m.Update(tickOf(m.startSpinner.Tick))
		if cmd == nil {
			t.Fatal("el spinner de arranque no re-armó: un servicio arrancándose se quedaría congelado")
		}
		var ticks int
		for _, got := range collectBatch(t, cmd) {
			if _, ok := got.(spinner.TickMsg); ok {
				ticks++
			}
		}
		if ticks != 1 {
			t.Errorf("el spinner de arranque produjo %d ticks, want 1", ticks)
		}
	})
}

// tickOf adapta un getter de TickMsg al tipo concreto que Update hace switch.
func tickOf(fn func() tea.Msg) spinner.TickMsg {
	tick, _ := fn().(spinner.TickMsg)
	return tick
}

// TestUpdateTickPideThreadsYMetricsParaUnServicioVivo: el tick de estado lanza el
// muestreo de hilos de un servicio vivo, y las métricas sólo en su pestaña.
//
// La pestaña importa: pedir métricas en la pestaña de consola es trabajo que se
// tira a la basura, y en un workspace con veinte servicios son veinte lecturas de
// /proc por segundo que nadie ve.
func TestUpdateTickPideThreadsYMetricsParaUnServicioVivo(t *testing.T) {
	t.Run("servicio vivo", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		markRunning(&m, projectPath(t, m, "tienda-api"), 4242)
		m.activeTab = tabMetrics

		_, cmd := m.Update(tickMsg(time.Now()))
		if cmd == nil {
			t.Fatal("el tick no devolvió Cmd")
		}
		msgs := collectBatch(t, cmd)
		var sawRefresh, sawThreads, sawMetrics bool
		for _, msg := range msgs {
			switch msg.(type) {
			case refreshedMsg:
				sawRefresh = true
			case threadsMsg:
				sawThreads = true
			case metricsMsg:
				sawMetrics = true
			}
		}
		if !sawRefresh {
			t.Error("sin refreshedMsg el estado queda rancio")
		}
		if !sawThreads {
			t.Error("un servicio vivo sin muestreo de hilos deja la pestaña Threads vacía para siempre")
		}
		if !sawMetrics {
			t.Error("en la pestaña de métricas no se pidieron métricas")
		}
	})

	t.Run("servicio parado", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.activeTab = tabMetrics

		_, cmd := m.Update(tickMsg(time.Now()))
		msgs := collectBatch(t, cmd)
		for _, msg := range msgs {
			if _, ok := msg.(threadsMsg); ok {
				t.Error("se muestrearon hilos de un servicio parado")
			}
		}
	})

	t.Run("pestaña de salud", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.activeTab = tabHealth

		_, cmd := m.Update(tickMsg(time.Now()))
		msgs := collectBatch(t, cmd)
		var sawHealth bool
		for _, msg := range msgs {
			if _, ok := msg.(healthMsg); ok {
				sawHealth = true
			}
		}
		if !sawHealth {
			t.Error("en la pestaña de salud el tick no pidió health: la salud se congelaría")
		}
	})
}

// TestFindComposeFileSubeHastaElRootSinSalir: el compose file vive al nivel de
// los proyectos que orquesta, que no siempre es el root.
//
// Y hay dos reglas: busca SUBIENDO, y no sale del root. La segunda es la que
// importa para la seguridad: si saliera, un compose file de un directorio
// (por ejemplo de la home) orquestaría los proyectos de otro.
func TestFindComposeFileSubeHastaElRootSinSalir(t *testing.T) {
	isolateConfig(t)
	root := writeTestTree(t, false)

	// Sin compose file: error que lo dice, y el nombre del fichero.
	if _, err := findComposeFile(root, nil); err == nil {
		t.Error("sin compose file debería dar error")
	} else if !strings.Contains(err.Error(), orchestrate.ComposeFileName) {
		t.Errorf("el error no nombra el fichero que falta: %q", err)
	}

	// Con compose en el root: lo encuentra.
	writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), "primary_group = \"tienda\"\n")
	projects := []scanner.Project{{Path: filepath.Join(root, "tienda-api"), Configured: true}}
	cf, err := findComposeFile(root, projects)
	if err != nil {
		t.Fatalf("con compose en el root no lo encontró: %v", err)
	}
	if cf.PrimaryGroup != "tienda" {
		t.Errorf("PrimaryGroup = %q", cf.PrimaryGroup)
	}

	// Y con compose en un nivel INTERMEDIO (el padre del proyecto, que es como
	// se organiza en la vida real: el compose va junto al grupo de proyectos).
	sub := filepath.Join(root, "grupo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeStr(t, filepath.Join(sub, orchestrate.ComposeFileName), "primary_group = \"intermedio\"\n")
	nested := []scanner.Project{{Path: filepath.Join(sub, "app"), Configured: true}}
	cf, err = findComposeFile(root, nested)
	if err != nil {
		t.Fatalf("con compose en un nivel intermedio no lo encontró: %v", err)
	}
	if cf.PrimaryGroup != "intermedio" {
		t.Errorf("encontró el compose equivocado: PrimaryGroup = %q, want intermedio", cf.PrimaryGroup)
	}
}
