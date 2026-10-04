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

// updateMsg is the Bubbletea loop without Bubbletea: Update is a pure function of (model, msg), so it is called directly.
func updateMsg(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update devolvió %T, want Model", next)
	}
	return got
}

// Scan and poll are not synchronized, so a path deleted from disk may come back; recreating the row would grow the tree with phantom services.
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

// starting/stopping are owned by startedMsg/stoppedMsg: the poll must not overwrite them or a booting service reads as stopped.
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
	if got.branches[path] != "feature/nueva" {
		t.Errorf("branches[%s] = %q, want feature/nueva: la rama se propaga aunque el estado sea transitorio", path, got.branches[path])
	}
}

// Only one warning is shown because map order is random: the assertion is the behaviour, not which warning wins.
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

// A read bug, not a write bug: the log is truncated on start, so the in-memory buffer must be cleared or the previous service's lines show first.
func TestUpdateStartedConExitoFijaElEstadoYLimpiaLaConsola(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")
	m.services[path].Status = statusStarting

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

	gotCS := got.consoleStateFor(path)
	if gotCS.stdout != "" || gotCS.stderr != "" || gotCS.merged != "" {
		t.Errorf("la consola en memoria no se limpió: %q / %q", gotCS.stdout, gotCS.stderr)
	}
	if gotCS.off[0] != 0 || gotCS.off[1] != 0 {
		t.Errorf("los offsets quedaron en %d/%d: con el log truncado hay que releer desde 0", gotCS.off[0], gotCS.off[1])
	}

	if len(got.events[path]) == 0 {
		t.Error("un arranque correcto no dejó evento: el usuario no ve que arrancó nada")
	}
}

// MEDIDO: notify overwrites, so only the last warning shows; concatenating would bury the first (usually the cause) and the rest already goes to the service's stderr log.
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

// A failed stop must keep pendingRestart: clearing it loses the restart silently, looking exactly like it succeeded.
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

// The intermediate starting state is what the start spinner shows: reporting running here would hide the boot.
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

// No Cmd here: it would start the service the user just stopped.
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

// A launch error and a non-zero exit stay distinct: fusing them makes a disk failure look like a broken build.
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
			m.jobs[tt.msg.path] = "build"

			got := updateMsg(t, m, tt.msg)

			for _, want := range tt.want {
				if !strings.Contains(got.message, want) {
					t.Errorf("el aviso %q no menciona %q", got.message, want)
				}
			}
			// The lock is always released, or the project stays latched and cannot be started again.
			if _, still := got.jobs[tt.msg.path]; still {
				t.Error("el proyecto sigue bloqueado tras el job: no se puede volver a arrancar")
			}
			if len(got.events) == 0 {
				t.Error("el job no dejó evento")
			}
		})
	}
}

// A history where a failed build reads like a successful one is worse than no history at all.
func TestUpdateJobResultRegistraElFallidoConSuCodigo(t *testing.T) {
	m, _ := newJobsTestModel(t)
	path := projectPath(t, m, "tienda-api")

	ok := updateMsg(t, m, jobMsg{path: path, kind: "build", command: "make", elapsed: time.Second})

	// Model's maps are shared across copies of the value receiver, so the slice is copied before the second update or both sides see both events.
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

// statusMsg must touch nothing else: an "editor closed" notice that changed state could stop a service.
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

// A sampling error is routine (the process can die between poll and read); surfacing it would make the TUI blink with phantom warnings.
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

	got = updateMsg(t, got, threadsMsg{path: path, err: errors.New("no such process")})
	if len(got.threads[path]) != 0 {
		t.Errorf("un muestreo fallido dejó hilos: %v", got.threads[path])
	}
}

// Mixing results would show web's health on api's row, and the user would read "healthy" from a service that is not.
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

// A message with a zero expiry is never cleared, because IsZero() is the branch condition: every notice needs an expiry.
func TestUpdateTickProgramaElSiguienteYRespetaElExpirado(t *testing.T) {
	m, _ := newTestModel(t)
	m.message = "aviso viejo"
	m.messageExpiresAt = time.Now().Add(-time.Second)

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

	// A 3s notice must survive the 2s polling tick instead of being cleared with the expired one.
	m2, _ := newTestModel(t)
	m2.message = "aviso nuevo"
	m2.messageExpiresAt = time.Now().Add(3 * time.Second)
	got2 := updateMsg(t, m2, tickMsg(time.Now()))
	if got2.message != "aviso nuevo" {
		t.Errorf("un mensaje sin caducar se borró: %q", got2.message)
	}
}

// The clock is re-armed even with nothing selected: a dead clock freezes the console, and tailing an unwatched log is wasted work.
func TestUpdateConsoleTickReArmaElTailSoloConServicioEnConsola(t *testing.T) {
	m, _ := newTestModel(t)

	sel := moveCursorTo(t, m, "tienda-api")
	sel.activeTab = tabConsole
	_, cmd := sel.Update(consoleTickMsg(time.Now()))
	if cmd == nil {
		t.Error("el tick de consola no devolvió Cmd: sin re-armar el reloj la consola se congela")
	}
	var sawTail bool
	for _, msg := range collectBatch(t, cmd) {
		if _, ok := msg.(consoleDeltaMsg); ok {
			sawTail = true
		}
	}
	if !sawTail {
		t.Error("con la pestaña de consola no se pidió el tail: el log no avanzaría solo")
	}

	onHeader := m
	onHeader.cursor = findPrimary(onHeader, "tienda")
	if _, cmd := onHeader.Update(consoleTickMsg(time.Now())); cmd == nil {
		t.Error("el tick de consola debe re-armar el reloj aunque no haya nada que leer")
	}
}

// A stream whose read failed must not advance its offset: those bytes would be lost forever, skipped by every later tick.
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

	got = updateMsg(t, m, consoleDeltaMsg{path: "/no/existe", stdout: "x", offS: 1})
	if len(got.services) != len(m.services) {
		t.Error("un delta de consola creó una fila de servicio nueva")
	}
}

// A cursor outside the visible window is the worst case: keys would act on the wrong service.
func TestUpdateWindowSizeReajustaElArbolYLaTerminal(t *testing.T) {
	m, _ := newTestModel(t)
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

// MEDIDO: both spinners get the same msg and the non-matching id ignores it, so the observable contract is one tick back per tick received, never two.
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

func tickOf(fn func() tea.Msg) spinner.TickMsg {
	tick, _ := fn().(spinner.TickMsg)
	return tick
}

// Metrics are requested only on their tab: on a large workspace that is /proc reads per second thrown away.
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

// Leaving the root is a safety rule, not a layout one: a compose file from the home directory would orchestrate another workspace's projects.
func TestFindComposeFileSubeHastaElRootSinSalir(t *testing.T) {
	isolateConfig(t)
	root := writeTestTree(t, false)

	if _, err := findComposeFile(root, nil); err == nil {
		t.Error("sin compose file debería dar error")
	} else if !strings.Contains(err.Error(), orchestrate.ComposeFileName) {
		t.Errorf("el error no nombra el fichero que falta: %q", err)
	}

	writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), "primary_group = \"tienda\"\n")
	projects := []scanner.Project{{Path: filepath.Join(root, "tienda-api"), Configured: true}}
	cf, err := findComposeFile(root, projects)
	if err != nil {
		t.Fatalf("con compose en el root no lo encontró: %v", err)
	}
	if cf.PrimaryGroup != "tienda" {
		t.Errorf("PrimaryGroup = %q", cf.PrimaryGroup)
	}

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
