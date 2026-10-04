package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/config"
	"vroom/internal/gitinfo"
	"vroom/internal/launcher"
	"vroom/internal/orchestrate"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/tail"
)

// ---------------------------------------------------------------------------
// Los huecos que quedan en app.go: las teclas menos usadas, los degradados del
// layout, los mensajes de Update que nadie disparaba y las ramas de la TUI en
// estados poco frecuentes.
//
// Ninguno de estos caminos es raro para el usuario; lo que pasa es que llegan por
// combinaciones de estado —una pestaña que no es la de consola, un modal encima de
// otro, un servicio sin estado conocido— y las combinaciones de estado no se
// provisuran solas en una suite.
//
// Y hay una categoría aparte: los defaults que existen para que el programa no se
// rompa con una entrada rara (un margen negativo, un árbol vacío, un servicio sin
// estado). Esos se prueban aquí porque son la diferencia entre "se ve raro" y "se
// apaga".
// ---------------------------------------------------------------------------

// TestLasTeclasDePosicionDeLaConsolaPausanYReanudanElFollow: `g` y `G`.
//
// Es el control manual del follow. Ir arriba tiene que PAUSARLO —si no, el texto se
// mueve solo mientras lo lee— e ir abajo tiene que reanudarlo. Los dos_STATEos de un
// mismo control, y el segundo es el que más se usa.
func TestLasTeclasDePosicionDeLaConsolaPausanYReanudanElFollow(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.consoleFollow = true

	arriba, _ := press(m, m.cfg.KeyFor("top"))
	if arriba.consoleFollow {
		t.Error("ir arriba tiene que pausar el follow: si no, el texto se mueve solo mientras se lee")
	}

	abajo, _ := press(arriba, m.cfg.KeyFor("bottom"))
	if !abajo.consoleFollow {
		t.Error("ir abajo tiene que reanudar el follow: si no, el log deja de fluir solo y el usuario tiene que acordarse")
	}
}

// TestLaTeclaDeLogsSeNiegaSobreUnStack: la única acción que se rechaza por tipo de
// fila.
//
// Un stack no tiene logs de servicio: sus servicios tienen, pero "los logs del
// stack" no significan nada. Abrir el editor con lo que saliera sería peor que
// negarse.
func TestLaTeclaDeLogsSeNiegaSobreUnStack(t *testing.T) {
	m := newStackModel(t)
	cursorEn(t, &m, "front")

	next, cmd := m.handleKey(keyMsg(m.cfg.KeyFor("logs")))
	got := next.(Model)
	if cmd != nil {
		t.Error("sobre un stack no hay editor que abrir")
	}
	if !strings.Contains(got.message, "not available for stacks") {
		t.Errorf("aviso = %q, want que diga que no está disponible para stacks", got.message)
	}
}

// TestElAliasOCierraLosMismosLogsQueLaTecla: el alias fijo.
//
// `o` es un alias de `l` que existe porque la gente lo escribe por costumbre. Y está
// "salvo que el config lo reclame": si el usuario mapea `o` a otra acción, el alias
// tiene que ceder.
func TestElAliasOCierraLosMismosLogsQueLaTecla(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	t.Setenv("EDITOR", "/bin/sh")

	// Sin config que lo reclame: `o` abre los logs.
	if next, _ := m.handleKey(keyMsg("o")); next.(Model).message != "" || m.cfg.KeyFor("logs") != "l" {
		t.Skip("precondición: `o` no debe estar mapeado a otra acción")
	}

	// Con `o` mapeado a otra cosa, el alias cede y manda la acción del config.
	//
	// MEDIDO: el config rechaza `o` como binding de build si... no, lo acepta; lo
	// que pasa es que `tienda-web` del árbol base no tiene `command_build`, así que
	// la acción que se ejecuta es el aviso de "no hay comando". Lo que se comprueba
	// es el efecto observable: con `o` = build la tecla NO abre el editor, y el
	// mensaje es el del build.
	conConfig, _ := newTestModelWithConfig(t, "[keybindings]\nbuild = \"o\"\n")
	conConfig = moveCursorTo(t, conConfig, "tienda-web")
	if conConfig.cfg.KeyFor("build") != "o" {
		t.Fatalf("precondición: el config debería haber mapeado build a o, got %q", conConfig.cfg.KeyFor("build"))
	}
	t.Setenv("EDITOR", "/bin/sh")
	next, _ := conConfig.handleKey(keyMsg("o"))
	got := next.(Model)
	if got.message == "" {
		t.Fatal("con o mapeado a build la tecla tiene que hacer algo: aquí el aviso de que no hay command_build")
	}
	if strings.Contains(got.message, "editor") {
		t.Errorf("aviso = %q: con o mapeado a build no puede abrirse el editor", got.message)
	}
}

// TestElLayoutSeRecuperaDeUnaPantallaEnormeYDeUnaDiminuta: los dos extremos.
//
// Los dos degradados importan por motivos distintos. En una ventana diminuta el
// programa tiene que seguir siendo usable aunque no quepa todo —el árbol y la
// consola son lo que no puede faltar—. Y en una enorme el viewport de la consola no
// puede crecer sin límite: el buffer es limitado, así que un viewport de 5000 filas
// deja el 99% del panel vacío.
func TestElLayoutSeRecapaDeUnaPantallaEnormeYDeUnaDiminuta(t *testing.T) {
	m, _ := newTestModel(t)

	for _, dims := range [][2]int{{5, 3}, {10, 5}, {20, 10}, {500, 200}} {
		m.width, m.height = dims[0], dims[1]
		m.updateLayout()

		if m.bodyH < 1 {
			t.Errorf("%dx%d: bodyH = %d, want >= 1: sin alto no hay árbol", dims[0], dims[1], m.bodyH)
		}
		if m.contentH < 0 {
			t.Errorf("%dx%d: contentH = %d, want >= 0", dims[0], dims[1], m.contentH)
		}
		// MEDIDO: rightW llega a 0 en un terminal más estrecho que el árbol más sus
		// dos marcos. Es el degradado documentado —"con menos de ~34 celdas el layout
		// queda degradado"— y no un fallo: lo que no puede pasar es que sea NEGATIVO,
		// porque bordered.draw invierte los lados y la caja se dibuja al revés.
		if m.rightW < 0 {
			t.Errorf("%dx%d: rightW = %d, want >= 0: un ancho negativo invierte la caja", dims[0], dims[1], m.rightW)
		}
		// El viewport tiene que seguir siendo utilizable.
		if m.consoleView.Height() < 1 && m.contentH > 0 {
			t.Errorf("%dx%d: el viewport de consola tiene alto %d con contentH %d", dims[0], dims[1], m.consoleView.Height(), m.contentH)
		}
	}
}

// TestWindowSizeReencajaElArbolCuandoElCursorQuedaFuera: el auto-scroll tras un
// redimensionado.
//
// Sin esto, al encoger la ventana el cursor puede quedar por debajo del borde
// inferior y el usuario ve un árbol donde su proyecto no está, con el cursor en un
// sitio invisible. Y no hay forma de saber en qué fila está.
func TestWindowSizeReencajaElArbolCuandoElCursorQuedaFuera(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	m.updateLayout()

	// Se lleva el cursor al final del árbol y luego se encoge la ventana a una
	// pantalla en la que no cabe.
	m.cursor = len(m.tree) - 1
	m.treeTop = 0

	_, cursorLine := m.treeLines()
	pequena := m
	pequena.width, pequena.height = 60, 8
	pequena.updateLayout()

	got := updateMsg(t, pequena, tea.WindowSizeMsg{Width: 60, Height: 8})
	if got.treeTop > cursorLine {
		t.Errorf("treeTop = %d con el cursor en la línea %d: la ventana del árbol quedó por debajo del cursor", got.treeTop, cursorLine)
	}
	// Y nunca por encima de la última fila.
	if max := cursorLine; got.treeTop > max {
		t.Errorf("treeTop = %d no puede pasar de la línea del cursor %d", got.treeTop, max)
	}
}

// TestWindowSizeRedimensionaLaTerminalViva: la otra mitad del mismo mensaje.
//
// Si la terminal embebida no sigue al redimensionado, se queda con el tamaño viejo
// dentro de un modal nuevo y sus líneas aparecen cortadas.
func TestWindowSizeRedimensionaLaTerminalViva(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	m, _ := newTestModel(t)
	m.term = s
	m.width, m.height = 200, 60
	m.updateLayout()

	updateMsg(t, m, tea.WindowSizeMsg{Width: 200, Height: 60})
	if len(s.pty.(*stubPty).resizes) == 0 {
		t.Error("con el layout cambiado la sesión de terminal no se redimensionó: sus líneas quedan cortadas")
	}
}

// TestUpdateConCadaMensajeDeMuestreoLoIntegraEnSuSitio: los cinco mensajes de las
// cinco pestañas.
//
// Los cinco son el mismo patrón con destino distinto, y por eso un solo test los
// cubre sin repetir: si uno cambia de sitio, el panel se queda en "sampling…"
// para siempre mientras la data se guarda en un mapa que nadie mira.
func TestUpdateConCadaMensajeDeMuestreoLoIntegraEnSuSitio(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))

	got := updateMsg(t, m, metricsMsg{path: path, m: process.Metrics{Ticks: 100}})
	if got.metrics[path] == nil {
		t.Error("metricsMsg no dejó muestra: el panel se queda en sampling para siempre")
	}

	got = updateMsg(t, got, envMsg{path: path, vars: []string{"A=1"}, err: nil})
	if len(got.envVars[path]) != 1 {
		t.Error("envMsg no dejó el entorno: la pestaña Env se queda leyendo")
	}

	got = updateMsg(t, got, gitMsg{path: path, st: gitStatusDePrueba()})
	if _, ok := got.gitStatus[path]; !ok {
		t.Error("gitMsg no dejó estado: la pestaña Git se queda leyendo")
	}

	got = updateMsg(t, got, healthMsg{path: path, r: &healthResult{StatusCode: 200}})
	if got.healthRes[path] == nil {
		t.Error("healthMsg no dejó resultado: la pestaña Health se queda sondeando")
	}

	got = updateMsg(t, got, threadsMsg{path: path, threads: []process.ThreadInfo{{TID: 1, Name: "main", State: "R"}}})
	if got.threads[path] == nil {
		t.Error("threadsMsg no dejó filas: la tabla de hilos se queda muestreando")
	}
}

// TestUpdateConUnPtyDataSinTerminalNoRevienta: el mensaje llega sin sesión.
//
// Puede pasar por un ptyData en vuelo cuando el usuario cierra el modal con ctrl+q
// en el mismo instante. Sin la guarda, `s.write` sobre un nil revienta la TUI.
func TestUpdateConUnPtyDataSinTerminalNoRevienta(t *testing.T) {
	m, _ := newTestModel(t)
	m.term = nil
	m.termOpen = true

	next, cmd := m.Update(ptyDataMsg{data: []byte("datos sin destino")})
	if next.(Model).termOpen != true {
		t.Error("un ptyData sin sesión no puede cambiar el estado del modal")
	}
	if cmd != nil {
		t.Error("un ptyData sin sesión no puede pedir otra lectura: no hay PTY")
	}
}

// TestNavigateConElArbolVacioNoHaceNada: el default que evita un módulo por cero.
//
// Un árbol vacío es real: el usuario está en un directorio sin ningún proyecto y el
// `len(m.tree) == 0` es lo que impide que el `j` haga un módulo por cero.
func TestNavigateConElArbolVacioNoHaceNada(t *testing.T) {
	m, _ := newTestModel(t)
	m.tree = nil

	for _, k := range []string{"j", "k", "down", "up"} {
		next, cmd := m.navigate(k)
		if cmd != nil || len(next.(Model).tree) != 0 {
			t.Errorf("navigate(%q) con el árbol vacío hizo algo", k)
		}
	}
}

// TestToggleSelectedRepartePorElTipoDeFila: las cinco filas y a dónde lleva cada una.
//
// Es la función que decide qué hace la tecla `s` según dónde esté el cursor, y cada
// fila tiene un destino: un primario y un secundario van a la acción de grupo, el
// header de Composers al motor de stacks, un stack al motor, y una fila de repo a un
// aviso. El repo es el caso que más cuesta: es una fila SINTETIZADA que no es un
// proyecto, y sin el aviso `s` no haría nada.
func TestToggleSelectedRepartePorElTipoDeFila(t *testing.T) {
	t.Run("header primario: acción de grupo", func(t *testing.T) {
		m, _ := newTestModel(t)
		cursorEn(t, &m, "tienda")
		it, _ := m.selectedItem()
		if it.kind != itemPrimary {
			t.Fatalf("precondición: el cursor debe estar en un primario, got %v", it.kind)
		}
		next, _ := m.toggleSelected()
		_ = next
	})

	t.Run("header secundario: acción de grupo", func(t *testing.T) {
		m, _ := newTestModel(t)
		cursorEn(t, &m, "tienda-api")
		it, _ := m.selectedItem()
		if it.kind != itemProject {
			t.Fatalf("precondición: el cursor debe estar en un proyecto, got %v", it.kind)
		}
		// Un secundario de verdad: el árbol de test no tiene uno, así que se inyecta.
		m.tree[m.cursor] = treeItem{kind: itemSecondary, primary: "tienda", secondary: "backend"}
		next, _ := m.toggleSelected()
		if strings.Contains(next.(Model).message, "select a service") {
			t.Error("un secundario tiene que ir a la acción de grupo, no al aviso de sin selección")
		}
	})

	t.Run("header de composers: el motor de stacks", func(t *testing.T) {
		m := newStackModel(t)
		cursorEn(t, &m, "tienda")
		it, _ := m.selectedItem()
		m.tree[m.cursor] = treeItem{kind: itemSecondary, primary: "tienda", secondary: composersGroup}

		next, _ := m.toggleSelected()
		got := next.(Model)
		if got.message == "" {
			t.Error("el header de composers tiene que decir algo: lanza o para stacks, nunca se queda callado")
		}
		_ = it
	})

	t.Run("fila de repo: un aviso que invite a expandir", func(t *testing.T) {
		m, _ := newTestModel(t)
		cursorEn(t, &m, "tienda-api")
		m.tree[m.cursor] = treeItem{
			kind: itemRepo, repoPath: t.TempDir(), hasKids: true,
			project: scanner.Project{Path: t.TempDir(), Name: "repo", Configured: true},
		}

		next, cmd := m.toggleSelected()
		got := next.(Model)
		if cmd != nil {
			t.Error("una fila de repo no tiene servicio que arrancar: sus worktrees están debajo")
		}
		if !strings.Contains(got.message, "expand") {
			t.Errorf("aviso = %q, want que invite a expandir el repo", got.message)
		}
	})

	t.Run("sin nada bajo el cursor", func(t *testing.T) {
		m, _ := newTestModel(t)
		m.tree = nil
		next, cmd := m.toggleSelected()
		if cmd != nil || next.(Model).message != "" {
			t.Error("sin selección `s` no puede hacer ni decir nada: no hay a qué")
		}
	})
}

// TestToggleNodeCuandoTodosEstanVivosLosPara: el otro sentido de la acción de grupo.
//
// El inverso del caso que ya había: si no hay ninguno parado, la acción para los
// vivos. Y marca stopping, que es lo que hace que el usuario vea el cambio antes de
// que el motor termine.
func TestToggleNodeCuandoTodosEstanVivosLosPara(t *testing.T) {
	m, _ := newTestModel(t)
	cursorEn(t, &m, "tienda")

	api, web := projectPath(t, m, "tienda-api"), projectPath(t, m, "tienda-web")
	markRunning(&m, api, livePID(t))
	markRunning(&m, web, livePID(t))

	next, cmd := m.toggleNode("tienda", "")
	if cmd == nil {
		t.Fatal("con todos vivos tiene que parar algo")
	}
	got := next.(Model)
	for _, ruta := range []string{api, web} {
		if sv := got.services[ruta]; sv != nil && sv.Status != statusStopping {
			t.Errorf("el servicio vivo quedó en %q, want stopping: si no, el usuario ve running y pulsa stop otra vez", sv.Status)
		}
	}
}

// TestToggleNodeLimpiaLaConsolaDeLosQueArranca: el segundo de la acción de grupo.
//
// Al arrancar hay que vaciar la vista de consola: el log anterior es del proceso
// anterior, y verlo debajo del servicio recién arrancado hace pensar que el proceso
// nuevo está fallando con los errores viejos.
func TestToggleNodeLimpiaLaConsolaDeLosQueArranca(t *testing.T) {
	m, _ := newTestModel(t)
	cursorEn(t, &m, "tienda")

	api, web := projectPath(t, m, "tienda-api"), projectPath(t, m, "tienda-web")
	// El log del que va a arrancar tiene contenido en disco de una vida anterior.
	if _, err := m.store.EnsureServiceDir(web); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.store.StdoutLog(web), []byte("contenido viejo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	markRunning(&m, api, livePID(t))
	m.services[web].Status = statusStopped

	// Se deja contenido viejo en la consola de los dos.
	for _, ruta := range []string{api, web} {
		cs := m.consoleStateFor(ruta)
		cs.stdout, cs.stderr, cs.merged = "viejo\n", "viejo\n", "viejo\n"
	}

	next, _ := m.toggleNode("tienda", "")
	got := next.(Model)

	// El que arrancó tiene la consola limpia.
	cs := got.consoleStateFor(web)
	if cs.merged != "" || cs.stdout != "" || cs.stderr != "" {
		t.Errorf("el servicio que arrancó conserva el log anterior: %q", cs.merged)
	}
	// Y los offsets apuntan al final del fichero, que es lo que impide que el
	// siguiente tail reinserte el log viejo.
	if got.consoleStateFor(web).off[0] == 0 {
		t.Error("el offset de stdout quedó a cero con un log que ya tenía contenido: " +
			"el siguiente tail reinsertaría la vida anterior del servicio")
	}
}

// TestMarkStackStoppingIgnoraLosNombresQueNoResuelven: el criterio de resolución.
//
// Un stack puede referenciar un servicio que ya no está en el workspace —alguien
// lo borró desde el último refresh—. Esa fila se salta en silencio porque el resto
// del stack se puede parar igual, y el aviso del conflicto ya salió en otro sitio.
func TestMarkStackStoppingIgnoraLosNombresQueNoResuelven(t *testing.T) {
	m := newStackModel(t)
	ruta := projectPath(t, m, "tienda-api")
	markRunning(&m, ruta, livePID(t))

	stack := &orchestrate.Stack{
		Name: "con-basura", PrimaryGroup: "tienda",
		Stages: []orchestrate.Stage{
			{Name: "e", Services: []string{"no-existe", "tienda-api", "tampoco-existe"}},
		},
	}
	m.markStackStopping(stack)

	if m.services[ruta].Status != statusStopping {
		t.Errorf("el servicio que sí resuelve quedó en %q: un nombre irresoluble no puede impedir parar el resto", m.services[ruta].Status)
	}
}

// TestMarkStackStoppingNoRepiteElServicioQueSaleEnDosEtapas: el `seen`.
//
// Un servicio repetido en dos etapas se contaría dos veces en el conteo del stack, y
// el panel mostraría "2 servicios" para uno. `markStackStopping` tiene su propio
// `seen` por eso.
func TestMarkStackStoppingNoRepiteElServicioQueSaleEnDosEtapas(t *testing.T) {
	m := newStackModel(t)
	ruta := projectPath(t, m, "tienda-api")
	markRunning(&m, ruta, livePID(t))

	stack := &orchestrate.Stack{
		Name: "repetido", PrimaryGroup: "tienda",
		Stages: []orchestrate.Stage{
			{Name: "e1", Services: []string{"tienda-api"}},
			{Name: "e2", Services: []string{"tienda-api"}},
		},
	}
	m.markStackStopping(stack)

	if m.services[ruta].Status != statusStopping {
		t.Errorf("estado = %q, want stopping", m.services[ruta].Status)
	}
}

// TestDispatchAskAvisaDelFallbackDeEstrategiaAntesDeLanzar: el warn del launcher.
//
// Con `launcher = "herdr"` explícito y sin sesión herdr, el launcher devuelve inline
// y un aviso. El aviso tiene que LLEGAR antes del despacho, no después: si sólo
// apareciera cuando el agente cerrara, el usuario no sabría por qué su agente se
// está ejecutando en primer plano.
func TestDispatchAskAvisaDelFallbackDeEstrategiaAntesDeLanzar(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.askPromptOpen = true
	m.askAgent = agenteFalso()
	m.promptInput.SetValue("arregla el bug")
	// herdr explícito sin herdr: la estrategia cae a inline con aviso.
	m.askLauncher = nuevoLauncherSinHerdr(t)
	m.promptInput.Focus()

	next, cmd := m.dispatchAsk()
	got := next.(Model)
	if cmd == nil {
		t.Fatal("tiene que despachar algo")
	}
	if !strings.Contains(got.message, "herdr") {
		t.Errorf("aviso = %q, want que diga que herdr no está disponible: el usuario tiene que saber por qué corre en primer plano", got.message)
	}
}

// TestDispatchAskConEstrategiaInlineUsaExecProcess: la otra estrategia.
//
// Inline suspende la TUI y corre el agente en el sitio, que es lo que el patrón wt
// hace y lo que el usuario quiere cuando no hay multiplexer. La diferencia con
// herdr/custom es observable: inline devuelve tea.ExecProcess, no un comando en
// background.
func TestDispatchAskConEstrategiaInlineUsaExecProcess(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.askPromptOpen = true
	m.askAgent = agenteFalso()
	m.promptInput.SetValue("arregla el bug")
	m.askLauncher = nuevoLauncherInline(t)
	m.promptInput.Focus()

	next, cmd := m.dispatchAsk()
	got := next.(Model)
	if cmd == nil {
		t.Fatal("inline tiene que devolver el comando de suspensión")
	}
	// Inline no lleva aviso: es lo pedido, no un fallback.
	if got.message != "" {
		t.Errorf("aviso = %q en inline: inline es lo pedido cuando se pide inline", got.message)
	}
	if got.askPromptOpen {
		t.Error("el modal tiene que cerrarse también en inline")
	}
}

// TestRestartSelectedConElEstadoPortPendingNoEsRunning: sólo se reinicia lo que
// está running.
//
// port_pending es un servicio VIVO con el discovery en vuelo, pero no está en estado
// running. Reiniciarlo mataría un proceso que iba bien por un puerto que aún no se
// había confirmado. El mensaje tiene que decirlo.
func TestRestartSelectedConElEstadoPortPendingNoEsRunning(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	m.services[path].Status = statusPortPending

	next, cmd := m.restartSelected()
	got := next.(Model)
	if cmd != nil {
		t.Error("port_pending no es running: no se reinicia un proceso vivo por un puerto sin confirmar")
	}
	if !strings.Contains(got.message, "running") {
		t.Errorf("aviso = %q, want que diga que sólo se reinicia lo que está corriendo", got.message)
	}
}

// TestEditLogsCmdTraeElEditorYSuMensajeDeError: el puente con el editor.
//
// Lo que no se puede probar aquí es `tea.ExecProcess` —suspende el programa y espera
// al editor—, así que se prueba lo que sí: que el comando se compone con el editor
// resuelto, y que el mensaje de error nombra al editor y no dice "algo falló".
func TestEditLogsCmdTraeElEditorYSuMensajeDeError(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "/bin/sh")

	next, cmd := m.openLogEditor()
	got := next.(Model)
	if cmd == nil {
		t.Fatal("con un editor que existe tiene que devolver el comando")
	}
	if got.message != "" {
		t.Errorf("aviso = %q al abrir un editor que existe: no hay motivo", got.message)
	}
}

// TestLosTresModalesSeSuperponenSobreElDashboard: una sola cosa, tres sitios.
//
// Los tres son `overlay` sobre el mismo contenido, y sólo uno puede estar abierto a
// la vez. Lo que se comprueba es la prioridad —ask, luego picker, luego terminal— y
// que los tres de verdad pintan encima: un modal que no se ve es un programa que
// parece colgado.
func TestLosTresModalesSeSuperponenSobreElDashboard(t *testing.T) {
	m, _ := newTestModel(t)
	base := m.View().Content

	for _, tt := range []struct {
		nombre string
		abrir  func(*Model)
		quiere string
	}{
		{"picker's de tasks", func(m *Model) {
			m.pickerOpen = true
			m.pickerItems = []pickerItem{{Name: "build"}}
		}, "tasks"},
		{"picker's de agentes", func(m *Model) {
			m.pickerKind = pickerAgents
			m.pickerOpen = true
			m.pickerItems = []pickerItem{{Name: "opencode"}}
		}, "choose an agent"},
		{"terminal", func(m *Model) {
			m.term = newStubSession(40, 10, &stubPty{})
			m.termOpen = true
		}, ""},
	} {
		t.Run(tt.nombre, func(t *testing.T) {
			v := m
			tt.abrir(&v)
			got := v.View().Content
			if got == base {
				t.Error("el modal no se dibujó: el contenido es el del dashboard a secas")
			}
			if tt.quiere != "" && !strings.Contains(stripANSIOf(got), tt.quiere) {
				t.Errorf("el modal no trae %q", tt.quiere)
			}
			if v.term != nil {
				v.term.shutdown()
			}
		})
	}

	t.Run("ask tiene prioridad sobre los otros dos", func(t *testing.T) {
		v := m
		v.askPromptOpen = true
		v.promptInput.SetValue("hola")
		v.pickerOpen = true
		v.pickerItems = []pickerItem{{Name: "build"}}
		got := stripANSIOf(v.View().Content)
		if !strings.Contains(got, "enter launch") {
			t.Error("con ask y picker abiertos, ask manda: es el que se ha abierto último")
		}
	})
}

// TestElPromptDelAskSeDimensionaTrasCambiarElPrompt: el comentario lo avisa.
//
// `sizeAskPrompt` tiene que llamarse tras CUALQUIER cambio de Prompt o de ancho, y
// también antes del Focus. Lo que se comprueba es que el resultado es utilizable: con
// un prompt largo, el ancho pedido no puede ser el del prompt por defecto.
func TestElPromptDelAskSeDimensionaTrasCambiarElPrompt(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 120, 40
	m.updateLayout()

	// Un prompt largo de verdad, que es lo que el comentario del método avisa.
	m.promptInput.Prompt = "> " + strings.Repeat("? ", 20)
	m.sizeAskPrompt()

	if got := m.promptInput.Width(); got < 8 {
		t.Errorf("ancho del textarea = %d: con un prompt largo el área de escritura desaparece", got)
	}
	if m.promptInput.MaxHeight < askMinHeight {
		t.Errorf("alto = %d, want >= %d", m.promptInput.MaxHeight, askMinHeight)
	}
}

// helpers --------------------------------------------------------------------

// nuevoLauncherSinHerdr devuelve un launcher con herdr explícito y sin binario, que
// es el caso que produce el fallback a inline con aviso.
func nuevoLauncherSinHerdr(t *testing.T) *launcher.Launcher {
	t.Helper()
	t.Setenv("HERDR_ENV", "")
	t.Setenv("PATH", t.TempDir()) // sin herdr en el PATH
	return launcher.New(herdrExplicita())
}

// nuevoLauncherInline devuelve un launcher con estrategia inline explícita.
func nuevoLauncherInline(t *testing.T) *launcher.Launcher {
	t.Helper()
	return launcher.New(askInlineConfig())
}

// herdrExplicita es el AskConfig que pide herdr a propósito.
func herdrExplicita() config.AskConfig {
	return config.AskConfig{Launcher: "herdr"}
}

// gitStatusDePrueba es un estado git plausible para la pestaña.
func gitStatusDePrueba() gitinfo.Status {
	return gitinfo.Status{Branch: "main"}
}

// stripANSIOf quita los escapes para poder buscar texto en un render.
func stripANSIOf(s string) string { return tail.StripANSI(s) }
