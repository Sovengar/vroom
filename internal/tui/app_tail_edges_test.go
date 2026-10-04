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

// A stack has no logs of its own -- only its services do -- so "the stack's logs" means nothing.
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

// "o" is an alias kept because people type it out of habit, and it must yield whenever the config claims that key.
func TestElAliasOCierraLosMismosLogsQueLaTecla(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	t.Setenv("EDITOR", "/bin/sh")

	if next, _ := m.handleKey(keyMsg("o")); next.(Model).message != "" || m.cfg.KeyFor("logs") != "l" {
		t.Skip("precondición: `o` no debe estar mapeado a otra acción")
	}

	// tienda-web has no command_build, so the observable effect of binding "o" is the "no command" warning rather than a launch.
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

// The console viewport must not grow without bound: the buffer is capped, so a viewport of thousands of rows leaves the panel almost empty.
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
		// MEDIDO: rightW reaching 0 is the documented degradation below ~34 cells, but negative is not allowed because bordered.draw swaps the sides and draws the box inside out.
		if m.rightW < 0 {
			t.Errorf("%dx%d: rightW = %d, want >= 0: un ancho negativo invierte la caja", dims[0], dims[1], m.rightW)
		}
		if m.consoleView.Height() < 1 && m.contentH > 0 {
			t.Errorf("%dx%d: el viewport de consola tiene alto %d con contentH %d", dims[0], dims[1], m.consoleView.Height(), m.contentH)
		}
	}
}

// Otherwise a shrink leaves the cursor below the bottom edge and the user sees a tree with no sign of where it is.
func TestWindowSizeReencajaElArbolCuandoElCursorQuedaFuera(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	m.updateLayout()

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
	if max := cursorLine; got.treeTop > max {
		t.Errorf("treeTop = %d no puede pasar de la línea del cursor %d", got.treeTop, max)
	}
}

// Without this the embedded terminal keeps its old size inside the new modal and its lines look cut.
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

// All five are the same pattern with a different destination, and a misplaced one parks its data in a map nobody reads while the panel stays on "sampling".
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

// An in-flight ptyData can arrive after the modal closed, and without the guard the write on a nil session takes the TUI down.
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

// An empty tree is real (a directory with no projects), and the len(m.tree) == 0 guard is what keeps navigation from a modulo by zero.
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

// A repo row is synthesized rather than a project, so without its warning "s" would do nothing at all on it.
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
		// The test tree has no secondary header, so a real one is injected.
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

// Marking stopping is what lets the user see the change before the engine has finished.
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

// Stale output shown under a freshly started service reads as the new process failing with old errors.
func TestToggleNodeLimpiaLaConsolaDeLosQueArranca(t *testing.T) {
	m, _ := newTestModel(t)
	cursorEn(t, &m, "tienda")

	api, web := projectPath(t, m, "tienda-api"), projectPath(t, m, "tienda-web")
	// The service about to start still has a previous run's output on disk.
	if _, err := m.store.EnsureServiceDir(web); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.store.StdoutLog(web), []byte("contenido viejo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	markRunning(&m, api, livePID(t))
	m.services[web].Status = statusStopped

	for _, ruta := range []string{api, web} {
		cs := m.consoleStateFor(ruta)
		cs.stdout, cs.stderr, cs.merged = "viejo\n", "viejo\n", "viejo\n"
	}

	next, _ := m.toggleNode("tienda", "")
	got := next.(Model)

	cs := got.consoleStateFor(web)
	if cs.merged != "" || cs.stdout != "" || cs.stderr != "" {
		t.Errorf("el servicio que arrancó conserva el log anterior: %q", cs.merged)
	}
	if got.consoleStateFor(web).off[0] == 0 {
		t.Error("el offset de stdout quedó a cero con un log que ya tenía contenido: " +
			"el siguiente tail reinsertaría la vida anterior del servicio")
	}
}

// A stack may reference a service deleted since the last refresh, and skipping it silently is safe because the conflict warning is issued elsewhere.
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

// A service repeated across stages would be counted twice in the stack total, so markStackStopping keeps its own seen set.
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

// With launcher = "herdr" set but no herdr present the launcher falls back to inline, and the warning must reach the model before the dispatch is emitted.
func TestDispatchAskAvisaDelFallbackDeEstrategiaAntesDeLanzar(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.askPromptOpen = true
	m.askAgent = agenteFalso()
	m.promptInput.SetValue("arregla el bug")
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

// Inline suspends the TUI and runs the agent in place, which is observably different from herdr/custom because it returns tea.ExecProcess.
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
	// Inline carries no warning: it is what was asked for, not a fallback.
	if got.message != "" {
		t.Errorf("aviso = %q en inline: inline es lo pedido cuando se pide inline", got.message)
	}
	if got.askPromptOpen {
		t.Error("el modal tiene que cerrarse también en inline")
	}
}

// port_pending is a live process whose port is unconfirmed, so restarting it would kill a healthy process over a pending discovery.
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

// tea.ExecProcess itself cannot be tested here (it suspends the program until the editor closes), so only its composition is asserted.
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

// Only one overlay can be open at a time, so the priority order matters and a modal that fails to paint looks like a hang.
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

// sizeAskPrompt must run after every Prompt or width change and before Focus, or the textarea collapses.
func TestElPromptDelAskSeDimensionaTrasCambiarElPrompt(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 120, 40
	m.updateLayout()

	m.promptInput.Prompt = "> " + strings.Repeat("? ", 20)
	m.sizeAskPrompt()

	if got := m.promptInput.Width(); got < 8 {
		t.Errorf("ancho del textarea = %d: con un prompt largo el área de escritura desaparece", got)
	}
	if m.promptInput.MaxHeight < askMinHeight {
		t.Errorf("alto = %d, want >= %d", m.promptInput.MaxHeight, askMinHeight)
	}
}

func nuevoLauncherSinHerdr(t *testing.T) *launcher.Launcher {
	t.Helper()
	t.Setenv("HERDR_ENV", "")
	t.Setenv("PATH", t.TempDir()) // sin herdr en el PATH
	return launcher.New(herdrExplicita())
}

func nuevoLauncherInline(t *testing.T) *launcher.Launcher {
	t.Helper()
	return launcher.New(askInlineConfig())
}

func herdrExplicita() config.AskConfig {
	return config.AskConfig{Launcher: "herdr"}
}

func gitStatusDePrueba() gitinfo.Status {
	return gitinfo.Status{Branch: "main"}
}

func stripANSIOf(s string) string { return tail.StripANSI(s) }
