package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/agents"
	"vroom/internal/config"
	"vroom/internal/launcher"
	"vroom/internal/manifest"
	"vroom/internal/mise"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
	"vroom/internal/tail"
)

func sinSeleccion(t *testing.T) Model {
	t.Helper()
	m, _ := newTestModel(t)
	cursorEn(t, &m, "tienda") // the primary header, not a project
	if m.selected() != nil {
		t.Fatalf("el cursor sigue sobre un proyecto: %v", m.selected())
	}
	return m
}

func sinManifiesto(t *testing.T) Model {
	t.Helper()
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "suelto")
	it, ok := m.selectedItem()
	if !ok {
		t.Fatal("no hay item bajo el cursor")
	}
	it.project.Configured = false
	it.project.Manifest = nil
	m.tree[m.cursor] = it
	return m
}

// An action that cannot act must say why; the test checks only that the message exists and is not another reason's.
func TestAccionesSinSeleccionAvisanYNoActuan(t *testing.T) {
	acciones := []struct {
		nombre string
		acc    func(Model) (tea.Model, tea.Cmd)
		quiere string // a word that must appear in the notice
	}{
		{"restart", func(m Model) (tea.Model, tea.Cmd) { return m.restartSelected() }, "select a service"},
		{"clearConsole", func(m Model) (tea.Model, tea.Cmd) { return m.clearConsole() }, "select a service"},
		{"openLogEditor", func(m Model) (tea.Model, tea.Cmd) { return m.openLogEditor() }, "select a service"},
		{"runInstall", func(m Model) (tea.Model, tea.Cmd) { return m.runInstall() }, "select a service"},
		{"runBuild", func(m Model) (tea.Model, tea.Cmd) { return m.runBuild() }, "select a service"},
		{"openPicker", func(m Model) (tea.Model, tea.Cmd) { return m.openPicker() }, "select a service"},
	}
	for _, a := range acciones {
		t.Run(a.nombre, func(t *testing.T) {
			m := sinSeleccion(t)
			next, cmd := a.acc(m)
			got := next.(Model)

			if cmd != nil {
				t.Errorf("%s sin selección launched algo: %+v", a.nombre, cmd)
			}
			if got.message == "" {
				t.Errorf("%s sin selección no dijo nada: el usuario no tiene forma de saber por qué", a.nombre)
			}
			if !strings.Contains(got.message, a.quiere) {
				t.Errorf("%s = %q, want que mencione %q", a.nombre, got.message, a.quiere)
			}
			if len(got.pendingRestart) > 0 {
				t.Errorf("%s marcó un reinicio pendiente sin haber hecho nada", a.nombre)
			}
		})
	}
}

// A missing manifest must not reuse the "select a service" message: the project is selected, only unusable.
func TestAccionesSinManifiestoAvisanConElMotivoDelManifiesto(t *testing.T) {
	acciones := []struct {
		nombre string
		acc    func(Model) (tea.Model, tea.Cmd)
	}{
		{"clearConsole", func(m Model) (tea.Model, tea.Cmd) { return m.clearConsole() }},
		{"openLogEditor", func(m Model) (tea.Model, tea.Cmd) { return m.openLogEditor() }},
		{"runInstall", func(m Model) (tea.Model, tea.Cmd) { return m.runInstall() }},
		{"runBuild", func(m Model) (tea.Model, tea.Cmd) { return m.runBuild() }},
		{"openPicker", func(m Model) (tea.Model, tea.Cmd) { return m.openPicker() }},
	}
	for _, a := range acciones {
		t.Run(a.nombre, func(t *testing.T) {
			m := sinManifiesto(t)
			next, cmd := a.acc(m)
			got := next.(Model)

			if cmd != nil {
				t.Errorf("%s sin manifiesto lanzó algo", a.nombre)
			}
			if !strings.Contains(got.message, "manifest") {
				t.Errorf("%s = %q, want que el motivo sea el manifiesto", a.nombre, got.message)
			}
			if strings.Contains(got.message, "select a service") {
				t.Errorf("%s = %q: el proyecto SÍ está seleccionado, el motivo es otro", a.nombre, got.message)
			}
		})
	}
}

// The message must name the manifest field to write: the user is one file away from fixing it.
func TestAccionesSinElComandoDicenQueCampoEscribir(t *testing.T) {
	// the base tree has neither command_install nor command_build
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	for _, tt := range []struct {
		nombre string
		acc    func(Model) (tea.Model, tea.Cmd)
		campo  string
	}{
		{"runInstall", func(m Model) (tea.Model, tea.Cmd) { return m.runInstall() }, "command_install"},
		{"runBuild", func(m Model) (tea.Model, tea.Cmd) { return m.runBuild() }, "command_build"},
	} {
		t.Run(tt.nombre, func(t *testing.T) {
			next, cmd := tt.acc(m)
			got := next.(Model)
			if cmd != nil {
				t.Errorf("%s con un manifiesto sin comando lanzó algo", tt.nombre)
			}
			if !strings.Contains(got.message, tt.campo) {
				t.Errorf("%s = %q, want que nombre el campo %q: es lo que el usuario tiene que escribir", tt.nombre, got.message, tt.campo)
			}
		})
	}
}

// A restart is stop+start and needs something running: skipping the stop skips a command_stop that may clear a volume or queue.
func TestRestartSelectedSoloReiniciaLoQueEstaCorriendo(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	m.services[path].Status = statusStopped
	next, cmd := m.restartSelected()
	got := next.(Model)
	if cmd != nil {
		t.Error("reiniciar un servicio parado no puede ser un stop+start")
	}
	if !strings.Contains(got.message, "running") {
		t.Errorf("= %q, want que diga que sólo se reinicia lo que está corriendo", got.message)
	}
	if len(got.pendingRestart) != 0 {
		t.Errorf("marcó un reinicio pendiente sin arrancar nada: %v", got.pendingRestart)
	}

	markRunning(&got, path, livePID(t))
	next, cmd = got.restartSelected()
	conRestart := next.(Model)
	if cmd == nil {
		t.Fatal("reiniciar un servicio vivo tiene que emitir el comando de stop")
	}
	if !conRestart.pendingRestart[path] {
		t.Error("no quedó marcado el reinicio pendiente: al volver del stop no se encadenaría el start")
	}
	if conRestart.services[path].Status != statusStopping {
		t.Errorf("Status = %q durante el reinicio, want stopping: si no, el usuario ve running y pulsa stop otra vez",
			conRestart.services[path].Status)
	}
}

// The nil guard exists because group actions call this without a manifest; a nil deref here would panic the whole TUI.
func TestManifestStopDevuelveElComandoYNoRevientaSinManifiesto(t *testing.T) {
	if got := manifestStop(scanner.Project{}); got != "" {
		t.Errorf("sin manifiesto = %q, want cadena vacía: un comando inventado sería un shell ejecutado por sorpresa", got)
	}
	if got := manifestStop(scanner.Project{Manifest: nil}); got != "" {
		t.Errorf("con manifiesto nil = %q", got)
	}
	if got := manifestStop(scanner.Project{Manifest: &manifest.Manifest{Name: "p", Command: "./p", Stop: "docker compose down"}}); got != "docker compose down" {
		t.Errorf("= %q, want el command_stop del manifiesto", got)
	}
}

// A corrupt mode must render as "merged", the real default: naming a nonexistent stream makes the toggle look broken.
func TestStreamModeStringCubreLosTresMasElDesconocido(t *testing.T) {
	tests := []struct {
		in   streamMode
		want string
	}{
		{streamMerged, "merged"},
		{streamStdout, "stdout"},
		{streamStderr, "stderr"},
		{streamMode(99), "merged"},
		{streamMode(-1), "merged"},
	}
	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("streamMode(%d).String() = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The default to stopped is deliberate and the dangerous part: a status invented tomorrow in process would offer "start" on a running service.
func TestMapUIStatusTraduceCadaEstadoYNoInventaUnoNuevo(t *testing.T) {
	tests := []struct {
		in   process.Status
		want uiStatus
	}{
		{process.StatusRunning, statusRunning},
		{process.StatusStopped, statusStopped},
		{process.StatusUnknown, statusUnknown},
		{process.StatusPortPending, statusPortPending},
		{process.StatusPortUnresolved, statusPortUnresolved},
		{process.StatusNoPort, statusNoPort},
		{process.Status("inventado"), statusStopped},
		{process.Status(""), statusStopped},
	}
	for _, tt := range tests {
		if got := mapUIStatus(tt.in); got != tt.want {
			t.Errorf("mapUIStatus(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestStatusBadgeCubreLosOchoEstadosMasLosNoConfigurados(t *testing.T) {
	p := scanner.Project{
		Path: "/p", Name: "p", Configured: true,
		Manifest: manifestWithPort(4321),
	}
	nueva := func(s uiStatus) *ServiceState {
		return &ServiceState{Status: s, Meta: state.Meta{Pid: 4321, Pgid: 4321, Port: 4321, State: state.StateRunning}}
	}

	tests := []struct {
		nombre string
		status uiStatus
		quiere string
	}{
		{"running", statusRunning, "running"},
		{"starting", statusStarting, "starting"},
		{"stopping", statusStopping, "stopping"},
		{"port pending", statusPortPending, "port pending"},
		{"port unresolved", statusPortUnresolved, "unresolved"},
		{"no port", statusNoPort, "no port"},
		{"unknown", statusUnknown, "unknown"},
		{"stopped", statusStopped, "stopped"},
	}
	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			got := tail.StripANSI(statusBadge(p, nueva(tt.status), "◐", "◌"))
			if !strings.Contains(got, tt.quiere) {
				t.Errorf("statusBadge(%s) = %q, want %q", tt.nombre, got, tt.quiere)
			}
		})
	}

	t.Run("sin configurar", func(t *testing.T) {
		p2 := scanner.Project{Path: "/p", Name: "p"}
		if got := tail.StripANSI(statusBadge(p2, nil, "◐", "◌")); !strings.Contains(got, "unconfigured") {
			t.Errorf("= %q, want unconfigured", got)
		}
	})

	t.Run("manifiesto inválido", func(t *testing.T) {
		p3 := scanner.Project{Path: "/p", Name: "p", ManifestErr: "falta command_start"}
		got := tail.StripANSI(statusBadge(p3, nil, "◐", "◌"))
		if !strings.Contains(got, "invalid") || strings.Contains(got, "unconfigured") {
			t.Errorf("= %q: un manifiesto malformado no es lo mismo que uno ausente", got)
		}
	})
}

// At n <= 1 trunc keeps the first rune and truncTail the last: swapping them swaps a character that both read as garbage.
func TestTruncYTruncTailConservanExtremosDistintos(t *testing.T) {
	tests := []struct {
		in        string
		n         int
		wantTrunc string
		wantTail  string
	}{
		{"abcdef", 10, "abcdef", "abcdef"},
		{"abcdef", 6, "abcdef", "abcdef"},
		{"abcdef", 4, "abc…", "…def"},
		{"abcdef", 1, "a", "f"},
		{"abcdef", 0, "", ""},
		{"abcdef", -3, "", ""},
		{"áéíóú", 3, "áé…", "…óú"},
	}
	for _, tt := range tests {
		if got := trunc(tt.in, tt.n); got != tt.wantTrunc {
			t.Errorf("trunc(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.wantTrunc)
		}
		if got := truncTail(tt.in, tt.n); got != tt.wantTail {
			t.Errorf("truncTail(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.wantTail)
		}
	}

	for n := range 8 {
		if got := trunc("áéíóúñ", n); strings.ToValidUTF8(got, "") != got {
			t.Errorf("trunc con n=%d partió un rune: %q", n, got)
		}
		if got := truncTail("áéíóúñ", n); strings.ToValidUTF8(got, "") != got {
			t.Errorf("truncTail con n=%d partió un rune: %q", n, got)
		}
	}
}

// padW fills to a VISIBLE width and never truncates: a negative width would cut escape codes in half.
func TestPadWNoRecortaYMideEnCeldasVisibles(t *testing.T) {
	if got := padW("abc", 10); len(got) != 10 {
		t.Errorf("padW(\"abc\", 10) mide %d bytes, want 10", len(got))
	}
	if got := padW("abc", 2); got != "abc" {
		t.Errorf("padW(\"abc\", 2) = %q: rellenar nunca recorta", got)
	}
	if got := padW("abc", -5); got != "abc" {
		t.Errorf("padW con ancho negativo = %q, want el original sin tocar", got)
	}

	coloured := "\x1b[31mabc\x1b[0m"
	got := padW(coloured, 8)
	if n := lipglossWidth(got); n != 8 {
		t.Errorf("padW con ANSI mide %d celdas visibles, want 8: %q", n, got)
	}
	if !strings.Contains(got, "\x1b[31m") {
		t.Errorf("el relleno se comió el estilo: %q", got)
	}
}

// The compact line must carry the ellipsis: an unmarked cut would show a shortcut that does not exist.
func TestDashboardHelpSeAdaptaAlAnchoYRespetaLosBindings(t *testing.T) {
	kb := map[string]string{
		"start_stop": "S", "restart": "R", "build": "B", "install": "I",
		"tasks": "t", "ask": "a", "clear": "c", "stream": "M", "logs": "L", "refresh": "r",
	}

	t.Run("respeta los bindings", func(t *testing.T) {
		h1 := dashboardHelp1(200, kb)
		for _, want := range []string{"S start/stop", "R restart", "B build", "I install"} {
			if !strings.Contains(h1, want) {
				t.Errorf("la línea 1 no trae %q: %q", want, h1)
			}
		}
		h2 := dashboardHelp2(200, kb)
		for _, want := range []string{"t tasks", "a ask", "c clear", "L logfile", "r refresh"} {
			if !strings.Contains(h2, want) {
				t.Errorf("la línea 2 no trae %q: %q", want, h2)
			}
		}
	})

	t.Run("con mapa nil usa los defaults", func(t *testing.T) {
		h1 := dashboardHelp1(200, nil)
		if !strings.Contains(h1, "start/stop") {
			t.Errorf("con mapa nil la línea 1 pierde el start/stop: %q", h1)
		}
		h2 := dashboardHelp2(200, nil)
		if !strings.Contains(h2, "tasks") {
			t.Errorf("con mapa nil la línea 2 pierde las tasks: %q", h2)
		}
	})

	t.Run("la línea 2 se compacta en vez de partirse", func(t *testing.T) {
		ancha := dashboardHelp2(200, kb)
		estrecha := dashboardHelp2(40, kb)

		if lipglossWidth(ancha) > 200 || lipglossWidth(estrecha) > 40 {
			t.Errorf("las líneas se pasan del ancho: %d y %d", lipglossWidth(ancha), lipglossWidth(estrecha))
		}
		// MEDIDO: the compact line is five segments (~57 chars) and does not fit 40 columns, so trunc marks the cut with the ellipsis.
		if !strings.HasSuffix(estrecha, "…") && lipglossWidth(estrecha) <= 40 {
			t.Errorf("una línea recortada a 40 tiene que llevar …: %q", estrecha)
		}
		// start/stop is deliberately absent here: it lives in help line 1, and duplicating it would rob space from the rarer shortcuts.
		for _, want := range []string{"j/k move", "enter collapse", "a ask", "t tasks", "c clear", "M stream", "L logfile", "r refresh", "1-7 tabs"} {
			if !strings.Contains(ancha, want) {
				t.Errorf("la versión completa perdió %q: %q", want, ancha)
			}
		}
		if strings.Contains(ancha, "start/stop") {
			t.Errorf("start/stop está en la línea 1: duplicarlo aquí roba espacio a los atajos menos frecuentes\n%q", ancha)
		}
		for _, perdido := range []string{"move", "collapse", "refresh", "tabs"} {
			if strings.Contains(estrecha, perdido) {
				t.Errorf("la versión compacta todavía trae %q: la compactación no está quitando nada", perdido)
			}
		}
	})

	t.Run("anchos degenerados no rompen", func(t *testing.T) {
		for _, w := range []int{0, -5, 1} {
			if h := dashboardHelp1(w, kb); h == "" {
				t.Errorf("dashboardHelp1(%d) = cadena vacía", w)
			}
			if h := dashboardHelp2(w, kb); h == "" {
				t.Errorf("dashboardHelp2(%d) = cadena vacía", w)
			}
		}
	})
}

func TestHandleMouseRuedaSobreDetallesYConsola(t *testing.T) {
	t.Run("sobre un header scrollea detalles", func(t *testing.T) {
		m := sinSeleccion(t)
		if !m.detailsShown {
			t.Fatal("precondición: el panel de detalles tiene que estar visible")
		}
		// Start from a non-zero scroll: WheelUp clamps at 0, so from 0 the test would prove nothing.
		m.detailsTop = 6
		arriba := m.detailsTop

		got := wheel(m, tea.MouseWheelUp)
		if got.detailsTop >= arriba {
			t.Errorf("la rueda arriba no subió el detalle: %d -> %d", arriba, got.detailsTop)
		}
		got = wheel(got, tea.MouseWheelDown)
		if got.detailsTop != arriba {
			t.Errorf("abajo y arriba no se cancelan: %d -> %d, want %d", arriba, got.detailsTop, arriba)
		}
		abajo := wheel(m, tea.MouseWheelDown)
		if abajo.detailsTop == 0 {
			t.Error("la rueda abajo desde el top no scrollea: el panel no se puede bajar nunca")
		}
		if got.consoleFollow != m.consoleFollow {
			t.Error("scrollear detalles no puede tocar el follow de la consola")
		}
	})

	t.Run("sobre un proyecto scrollea la consola", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.activeTab = tabConsole
		m.consoleFollow = true

		got := wheel(m, tea.MouseWheelUp)
		if got.consoleFollow {
			t.Error("subir en la consola tiene que pausar el follow: si no, el texto se mueve solo mientras se lee")
		}
		for range 50 {
			got = wheel(got, tea.MouseWheelDown)
		}
		if !got.consoleFollow {
			t.Error("llegar al final tiene que reanudar el follow: si no, el log deja de fluir solo y el usuario tiene que recordarlo")
		}
	})

	t.Run("en una pestaña que no es consola no hace nada", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.consoleFollow = true
		for _, tab := range []tabKind{tabThreads, tabMetrics, tabGit, tabEnv, tabTimeline, tabHealth} {
			m.activeTab = tab
			got := wheel(m, tea.MouseWheelUp)
			if got.consoleFollow != true {
				t.Errorf("pestaña %d: la rueda cambió el follow de la consola", tab)
			}
		}
	})
}

// Driven through the stream key, not called directly: the viewport is a value, so a direct call on a Model copy would be lost.
func TestSyncConsoleViewVaciaLaConsolaParaLoQueNoTieneHistorial(t *testing.T) {
	streamKey := func(t *testing.T, m Model) string {
		t.Helper()
		k := m.cfg.KeyFor("stream")
		if k == "" {
			t.Skip("no hay tecla de stream en los bindings")
		}
		return k
	}

	t.Run("sobre el proyecto mantiene su contenido", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		path := pathOfSelected(t, m)
		// All three buffers are filled because the stream key cycles through them: one alone would make "empty" look like "broken".
		cs := m.consoleStateFor(path)
		cs.merged = "salida vieja del servicio\n"
		cs.stdout = "salida vieja del servicio\n"
		cs.stderr = "salida vieja del servicio\n"
		m.consoleFollow = true

		got, _ := press(m, streamKey(t, m))
		if !strings.Contains(tail.StripANSI(got.consoleView.View()), "salida vieja") {
			t.Errorf("la consola del proyecto se vació: %q", tail.StripANSI(got.consoleView.View()))
		}
	})

	t.Run("sobre un header pone el texto de ayuda", func(t *testing.T) {
		m := sinSeleccion(t)
		got, _ := press(m, streamKey(t, m))
		if !strings.Contains(tail.StripANSI(got.consoleView.View()), "pick a service") {
			t.Errorf("sobre un grupo = %q, want el texto de ayuda", tail.StripANSI(got.consoleView.View()))
		}
	})

	t.Run("sin selección la vacía", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.consoleStateFor(pathOfSelected(t, m)).merged = "salida del otro servicio\n"
		m.tree = nil

		got, _ := press(m, streamKey(t, m))
		if v := tail.StripANSI(strings.TrimSpace(got.consoleView.View())); v != "" {
			t.Errorf("sin selección la consola = %q: deja ver el log del servicio anterior como si fuera suyo", v)
		}
	})

	t.Run("sin manifiesto la vacía", func(t *testing.T) {
		m := sinManifiesto(t)
		got, _ := press(m, streamKey(t, m))
		if v := tail.StripANSI(strings.TrimSpace(got.consoleView.View())); v != "" {
			t.Errorf("sin manifiesto la consola = %q, want vacía", v)
		}
	})
}

// -O goes only to vim: other editors reject it and the user would lose the logs.
func TestBuildEditorCmdAbreLosDosLogsYElOrdenSigueElStream(t *testing.T) {
	// MEDIDO: exec.Command puts the binary in Args[0], so editor flags must be appended after it, not before.
	cmd := buildEditorCmd("nvim", "/l/out.log", "/l/err.log", false)
	if got := strings.Join(cmd.Args, " "); got != "nvim -O /l/out.log /l/err.log" {
		t.Errorf("argv = %q, want nvim -O stdout stderr", got)
	}

	cmd = buildEditorCmd("nvim", "/l/out.log", "/l/err.log", true)
	if got := strings.Join(cmd.Args, " "); got != "nvim -O /l/err.log /l/out.log" {
		t.Errorf("con stderr activo argv = %q, want stderr primero", got)
	}

	for _, editor := range []string{"code", "nano", "emacs", "subl"} {
		cmd = buildEditorCmd(editor, "/l/out.log", "/l/err.log", false)
		if got := strings.Join(cmd.Args, " "); got != editor+" /l/out.log /l/err.log" {
			t.Errorf("%s recibió %q: -O sólo es de vim", editor, got)
		}
	}

	cmd = buildEditorCmd("code --wait", "/l/out.log", "/l/err.log", false)
	if got := strings.Join(cmd.Args, " "); got != "code --wait /l/out.log /l/err.log" {
		t.Errorf("argv = %q, want que los args del editor se conserven", got)
	}

	cmd = buildEditorCmd("", "/l/out.log", "/l/err.log", false)
	if got := strings.Join(cmd.Args, " "); got != "nvim -O /l/out.log /l/err.log" {
		t.Errorf("sin editor = %q, want el default nvim con split", got)
	}
}

// VISUAL wins over EDITOR so a per-session GUI editor beats a permanent terminal EDITOR.
func TestResolveEditorRespetaVisualYEditorEnEseOrden(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if got := resolveEditor(); got != "nvim" {
		t.Errorf("sin nada = %q, want nvim", got)
	}

	t.Setenv("EDITOR", "nano")
	if got := resolveEditor(); got != "nano" {
		t.Errorf("con EDITOR = %q", got)
	}

	t.Setenv("VISUAL", "code")
	if got := resolveEditor(); got != "code" {
		t.Errorf("con VISUAL y EDITOR = %q, want VISUAL: la precedencia está al revés", got)
	}
}

// In every text modal q types a q and esc is the exit: quitting on q would discard what was written.
func TestAskKeySoloSaleConElPromptVacioYEscCancala(t *testing.T) {
	abrir := func(t *testing.T, prompt string) Model {
		t.Helper()
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.askPromptOpen = true
		m.askAgent = agents.Agent{Name: "opencode", Cmd: []string{"opencode"}}
		m.promptInput.SetValue(prompt)
		m.sizeAskPrompt()
		return m
	}

	t.Run("esc cancela y limpia", func(t *testing.T) {
		m := abrir(t, "un borrador")
		got, cmd := m.askKey(keyMsg("esc"))
		gotModel := got.(Model)
		if cmd != nil {
			t.Error("esc no puede cerrar el programa")
		}
		if gotModel.askPromptOpen {
			t.Error("esc tiene que cerrar el modal")
		}
		if gotModel.promptInput.Value() != "" {
			t.Errorf("esc dejó el prompt con %q: al reabrir hay que borrarlo a mano", gotModel.promptInput.Value())
		}
	})

	t.Run("q con texto escribe una q", func(t *testing.T) {
		m := abrir(t, "hola")
		next, cmd := m.askKey(keyMsg("q"))
		if cmd != nil {
			t.Error("q con texto no puede salir del programa: se perdería lo escrito, y en un prompt a un agente escribir q es normal")
		}
		if !next.(Model).askPromptOpen {
			t.Error("q con texto cerró el modal")
		}
	})

	t.Run("q con el prompt vacío sale", func(t *testing.T) {
		m := abrir(t, "")
		if _, cmd := m.askKey(keyMsg("q")); cmd == nil {
			t.Error("q con el prompt vacío tiene que salir")
		}
	})

	t.Run("ctrl+c sale SIEMPRE, haya texto o no", func(t *testing.T) {
		// Regression: ctrl+c used to exit only on an empty ask prompt, leaving no way out without deleting the draft.
		for _, prompt := range []string{"", "manda ctrl+c al servicio", "un borrador largo"} {
			m := abrir(t, prompt)
			if _, cmd := m.askKey(keyMsg("ctrl+c")); cmd == nil {
				t.Errorf("ctrl+c con el prompt %q tiene que salir del programa", prompt)
			}
		}
	})
}

// Without a project there is no cwd, so the agent would start in vroom's own directory and edit vroom.
func TestDispatchAskRechazaElPromptVacioYLaFaltaDeProyecto(t *testing.T) {
	t.Run("prompt vacío", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.askPromptOpen = true
		m.promptInput.SetValue("   \n  ") // whitespace only

		next, cmd := m.dispatchAsk()
		got := next.(Model)
		if cmd != nil {
			t.Error("un prompt vacío no puede lanzar un agente")
		}
		if !strings.Contains(got.message, "empty prompt") {
			t.Errorf("= %q, want que diga que el prompt está vacío", got.message)
		}
		if !got.askPromptOpen {
			t.Error("el modal se cerró: el usuario tendría que volver a abrirlo para escribir")
		}
	})

	t.Run("sin proyecto", func(t *testing.T) {
		m := sinSeleccion(t)
		m.askPromptOpen = true
		m.promptInput.SetValue("arregla el bug")

		next, cmd := m.dispatchAsk()
		got := next.(Model)
		if cmd != nil {
			t.Error("sin proyecto no hay cwd donde lanzar el agente")
		}
		if !strings.Contains(got.message, "select a service") {
			t.Errorf("= %q", got.message)
		}
	})

	t.Run("con prompt y proyecto cierra el modal", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.askPromptOpen = true
		m.askAgent = agents.Agent{Name: "opencode", Cmd: []string{"false"}}
		m.promptInput.SetValue("arregla el bug")
		// inline strategy: the command exists even though no agent binary does
		m.askLauncher = launcher.New(askInlineConfig())
		m.promptInput.Focus()

		next, cmd := m.dispatchAsk()
		got := next.(Model)
		if cmd == nil {
			t.Fatal("un ask válido tiene que lanzar algo")
		}
		if got.askPromptOpen {
			t.Error("el modal tiene que cerrarse al lanzar: si no, se solapa con el agente")
		}
		if got.promptInput.Value() != "" {
			t.Errorf("el prompt quedó con %q: al reabrir el modal aparece el borrador anterior", got.promptInput.Value())
		}
	})
}

// An unknown placeholder is left verbatim on purpose: erasing it would hide a template the user can fix.
func TestExpandAskPromptSustituyeLoConocidoYDejaLoDesconocido(t *testing.T) {
	got := expandAskPrompt(
		"revisa {name} en {dir}, logs en {logs}, rama {branch}",
		"tienda-api", "/srv/api", "/var/lib/vroom/api",
	)
	for _, want := range []string{"tienda-api", "/srv/api", "/var/lib/vroom/api"} {
		if !strings.Contains(got, want) {
			t.Errorf("no sustituyó %q: %q", want, got)
		}
	}
	if !strings.Contains(got, "{branch}") {
		t.Errorf("un placeholder desconocido tiene que quedar intacto: %q", got)
	}

	if got := expandAskPrompt("texto plano", "a", "b", "c"); got != "texto plano" {
		t.Errorf("= %q", got)
	}
	if got := expandAskPrompt("x{name}y", "", "", ""); got != "xy" {
		t.Errorf("= %q, want xy", got)
	}
	if got := expandAskPrompt("{nombre}", "n", "", ""); got != "{nombre}" {
		t.Errorf("= %q, want que el desconocido quede intacto", got)
	}
}

// Height has three ceilings (screen, absolute cap, usable minimum): skipping any yields a modal that cannot be typed in.
func TestSizeAskPromptDimensionaElTextareaParaLaPantalla(t *testing.T) {
	for _, tt := range []struct{ w, h int }{
		{200, 60}, {120, 40}, {100, 30}, {80, 12}, {60, 6}, {40, 3},
	} {
		m, _ := newTestModel(t)
		m.width, m.height = tt.w, tt.h
		m.updateLayout()
		m.sizeAskPrompt()

		// bubbles stores the requested width MINUS 2, which is the "> " prompt.
		wantW := askInnerW(tt.w)
		if got := m.promptInput.Width(); got != wantW-2 {
			t.Errorf("%dx%d: ancho del textarea = %d, want %d (= askInnerW - 2 por el prompt)", tt.w, tt.h, got, wantW-2)
		}
		if got := m.promptInput.MaxHeight; got < askMinHeight {
			t.Errorf("%dx%d: alto mínimo del textarea = %d, want >= %d", tt.w, tt.h, got, askMinHeight)
		}
		if got := m.promptInput.MaxHeight; got > askMaxHeightCap {
			t.Errorf("%dx%d: alto del textarea = %d, want <= %d (el cap)", tt.w, tt.h, got, askMaxHeightCap)
		}
	}
}

// The three exits are distinct: q/ctrl+c quit the program, esc closes only the modal, enter acts.
func TestPickerKeyNavegaYEnterLanzaElItemElegido(t *testing.T) {
	base := func() Model {
		m := newStackModel(t)
		m.pickerItems = []pickerItem{
			{Name: "build", Description: "compila"},
			{Name: "test", Description: "los tests"},
			{Name: "lint"},
		}
		m.pickerOpen = true
		return m
	}
	nav := func(m Model, key string) Model {
		next, _ := m.pickerKey(key)
		return next.(Model)
	}

	t.Run("j/k recorren la lista y dan la vuelta", func(t *testing.T) {
		got := nav(base(), "j")
		if got.pickerCursor != 1 {
			t.Errorf("j → cursor %d, want 1", got.pickerCursor)
		}
		got = nav(got, "down")
		if got.pickerCursor != 2 {
			t.Errorf("down → cursor %d, want 2", got.pickerCursor)
		}
		got = nav(got, "j")
		if got.pickerCursor != 0 {
			t.Errorf("j al final → cursor %d, want 0 (cíclico)", got.pickerCursor)
		}
		got = nav(got, "k")
		if got.pickerCursor != 2 {
			t.Errorf("k al principio → cursor %d, want 2 (cíclico)", got.pickerCursor)
		}
	})

	t.Run("navegar con la lista vacía no revienta", func(t *testing.T) {
		vacia := base()
		vacia.pickerItems = nil
		vacia.pickerCursor = 0
		for _, k := range []string{"j", "k"} {
			if got := nav(vacia, k); got.pickerCursor != 0 {
				t.Errorf("%s con la lista vacía movió el cursor a %d", k, got.pickerCursor)
			}
		}
		if _, cmd := vacia.pickerKey("enter"); cmd != nil {
			t.Error("enter con la lista vacía no puede lanzar nada")
		}
	})

	t.Run("esc cierra el modal sin salir del programa", func(t *testing.T) {
		next, cmd := base().pickerKey("esc")
		got := next.(Model)
		if cmd != nil {
			t.Error("esc no puede cerrar el programa: sólo el modal")
		}
		if got.pickerOpen {
			t.Error("esc tiene que cerrar el modal")
		}
	})

	t.Run("q y ctrl+c salen del programa", func(t *testing.T) {
		for _, k := range []string{"q", "ctrl+c"} {
			if _, cmd := base().pickerKey(k); cmd == nil {
				t.Errorf("%s en el modal tiene que salir del programa, como en cualquier otra parte", k)
			}
		}
	})

	t.Run("enter sobre una task lanza el job y cierra el modal", func(t *testing.T) {
		m := base()
		m.pickerCursor = 1
		m.pickerKind = pickerTasks
		next, cmd := m.pickerKey("enter")
		got := next.(Model)
		if cmd == nil {
			t.Fatal("enter sobre una task tiene que lanzar el job")
		}
		if got.pickerOpen {
			t.Error("el modal tiene que cerrarse al lanzar: si no, se solapa con la salida")
		}
	})

	t.Run("enter sobre un agente abre el prompt de ask", func(t *testing.T) {
		m := base()
		m.pickerKind = pickerAgents
		m.pickerItems = []pickerItem{{Name: "opencode", agentCmd: []string{"opencode"}}}
		m.pickerCursor = 0
		next, _ := m.pickerKey("enter")
		got := next.(Model)
		if !got.askPromptOpen {
			t.Error("elegir un agente tiene que abrir el prompt de ask")
		}
		if got.askAgent.Name != "opencode" {
			t.Errorf("askAgent = %q, want opencode: se ha elegido otro", got.askAgent.Name)
		}
		if got.pickerOpen {
			t.Error("elegir agente cierra el picker: si no se solapan los dos modales")
		}
	})

	t.Run("teclas no contempladas se ignoran", func(t *testing.T) {
		next, cmd := base().pickerKey("x")
		got := next.(Model)
		if cmd != nil || !got.pickerOpen || got.pickerCursor != 0 {
			t.Error("una tecla sin acción tiene que no hacer nada: ni cerrar el modal ni mover el cursor")
		}
	})
}

// Without mise.toml the message must name the project, since the cursor can be on a group.
func TestOpenPickerDistingueLosCuatroRechazos(t *testing.T) {
	t.Run("sin mise.toml lo dice con el nombre", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api") // no mise.toml here

		next, cmd := m.openPicker()
		got := next.(Model)
		if cmd != nil || got.pickerOpen {
			t.Fatal("sin mise.toml el modal no se abre")
		}
		if !strings.Contains(got.message, "tienda-api") {
			t.Errorf("= %q, want que nombre el proyecto: el cursor puede estar sobre un grupo", got.message)
		}
		if !strings.Contains(got.message, "mise.toml") {
			t.Errorf("= %q, want que nombre el fichero que falta", got.message)
		}
	})

	t.Run("con mise.toml abre el modal con sus tasks", func(t *testing.T) {
		m := newJobsTestModelWithMiseOnAPI(t)
		next, cmd := m.openPicker()
		got := next.(Model)
		if cmd != nil {
			t.Error("abrir el picker no lanza nada por sí solo")
		}
		if !got.pickerOpen {
			t.Fatalf("el modal no se abrió: %q", got.message)
		}
		if got.pickerKind != pickerTasks {
			t.Errorf("pickerKind = %v, want pickerTasks", got.pickerKind)
		}
		if len(got.pickerItems) == 0 {
			t.Fatal("hay tasks en el mise.toml y el picker no trae ninguna")
		}
		if got.pickerCursor != 0 {
			t.Errorf("el cursor abre en %d, want 0: repetir el modal debe empezar por arriba", got.pickerCursor)
		}
	})

	t.Run("mise.toml ilegible propaga el motivo", func(t *testing.T) {
		m := newJobsTestModelWithMiseOnAPI(t)
		if err := removeFileAndMakeDir(miseTomlPath(t, m)); err != nil {
			t.Fatal(err)
		}
		next, _ := m.openPicker()
		got := next.(Model)
		if got.pickerOpen {
			t.Error("un mise.toml ilegible no puede abrir un modal de tasks")
		}
		if got.message == "" {
			t.Error("un mise.toml ilegible tiene que decir algo: si no, el modal no abre y no hay explicación")
		}
	})

	t.Run("mise.toml vacío lo distingue de la ausencia", func(t *testing.T) {
		m := newJobsTestModelWithMiseOnAPI(t)
		if err := writeFileTo(miseTomlPath(t, m), "[other]\nx = 1\n"); err != nil {
			t.Fatal(err)
		}
		next, _ := m.openPicker()
		got := next.(Model)
		if got.pickerOpen {
			t.Error("sin tasks no hay nada que elegir")
		}
		if !strings.Contains(got.message, "no tasks") {
			t.Errorf("= %q, want que diga que el fichero existe pero no tiene tasks: son dos arreglos distintos", got.message)
		}
	})
}

// projectByPath must return a pointer: a copy would swallow writes like m.projectByPath(p).Configured = false.
func TestProjectByPathDevuelveElPunteroAModificar(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")

	p := m.projectByPath(path)
	if p == nil {
		t.Fatalf("no encontró el proyecto %q", path)
	}
	p.Manifest.Port = 9999
	if m.projectByPath(path).Manifest.Port != 9999 {
		t.Error("projectByPath devolvió una copia: el cambio no llegó al modelo")
	}

	// An unknown path returns nil, not a pointer to an empty struct a caller would mistake for a hit.
	if got := m.projectByPath("/no/existe/nada"); got != nil {
		t.Errorf("una ruta inexistente devolvió %+v, want nil: un struct vacío haría que el caller creyera que encontró algo", got)
	}
}

// -1 is the out-of-range sentinel that makes callers fall back to scrolling the details panel.
func TestSelectedItemKindDevuelveMenosUnoSinItem(t *testing.T) {
	m, _ := newTestModel(t)

	m.cursor = -1
	if got := m.selectedItemKind(); got != -1 {
		t.Errorf("sin item = %v, want -1", got)
	}

	m = moveCursorTo(t, m, "tienda-api")
	if got := m.selectedItemKind(); got != itemProject {
		t.Errorf("sobre un proyecto = %v, want itemProject", got)
	}
	cursorEn(t, &m, "tienda")
	if got := m.selectedItemKind(); got != itemPrimary {
		t.Errorf("sobre un header = %v, want itemPrimary", got)
	}
}

// newJobsTestModelWithMiseOnAPI parks the cursor on tienda-web, the only project the jobs tree gives a mise.toml.
func newJobsTestModelWithMiseOnAPI(t *testing.T) Model {
	t.Helper()
	m, _ := newJobsTestModel(t)
	m = moveCursorTo(t, m, "tienda-web")
	if !mise.HasMiseToml(projectPath(t, m, "tienda-web")) {
		t.Fatalf("precondición: tienda-web debería traer mise.toml, y mise.HasMiseToml lo niega")
	}
	return m
}

func miseTomlPath(t *testing.T, m Model) string {
	t.Helper()
	return filepath.Join(projectPath(t, m, "tienda-web"), "mise.toml")
}

// Replacing a file with a directory is the cheap way to provoke a real read error instead of a simulated permission one.
func removeFileAndMakeDir(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	return os.Mkdir(path, 0o755)
}

func writeFileTo(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}

func manifestWithPort(port int) *manifest.Manifest {
	return &manifest.Manifest{Name: "p", Command: "./p", Port: port}
}

func askInlineConfig() config.AskConfig {
	return config.AskConfig{Launcher: "inline"}
}

func wheel(m Model, b tea.MouseButton) Model {
	next, _ := m.Update(tea.MouseWheelMsg{Button: b})
	return next.(Model)
}

func keyMsg(name string) tea.KeyMsg {
	var km tea.Msg
	switch name {
	case "esc":
		km = tea.KeyPressMsg{Code: tea.KeyEsc}
	case "ctrl+c":
		km = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		km = tea.KeyPressMsg{Code: rune(name[0]), Text: name}
	}
	k, _ := km.(tea.KeyMsg)
	return k
}

// The panic key exits everywhere and text keys depend on the text: that split is why q stays a literal q inside prompts.
func TestCtrlCSaleDesdeTodosLosModalesSinExcepcion(t *testing.T) {
	ctrlc := keyMsg("ctrl+c")

	t.Run("prompt de ask con texto", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.askPromptOpen = true
		m.askAgent = agenteFalso()
		m.promptInput.SetValue("un borrador que no quiero perder")
		if _, cmd := m.askKey(ctrlc); cmd == nil {
			t.Error("ctrl+c con texto tiene que salir: es la tecla de pánico del framework")
		}
	})

	t.Run("filtro del árbol", func(t *testing.T) {
		m, _ := newTestModel(t)
		m.filterOpen = true
		m.filterInput.SetValue("tienda")
		if _, cmd := m.filterKey(ctrlc); cmd == nil {
			t.Error("ctrl+c en el filtro tiene que salir")
		}
	})

	t.Run("picker", func(t *testing.T) {
		m := newStackModel(t)
		m.pickerItems = []pickerItem{{Name: "build"}}
		m.pickerOpen = true
		if _, cmd := m.pickerKey("ctrl+c"); cmd == nil {
			t.Error("ctrl+c en el picker tiene que salir")
		}
	})

	t.Run("vista principal", func(t *testing.T) {
		m, _ := newTestModel(t)
		if _, cmd := m.handleKey(ctrlc); cmd == nil {
			t.Error("ctrl+c en la vista principal tiene que salir")
		}
	})

	t.Run("q NO es la tecla de pánico", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.askPromptOpen = true
		m.promptInput.SetValue("busca este error: q no es un problema")
		if _, cmd := m.askKey(keyMsg("q")); cmd != nil {
			t.Error("q con texto escrito no puede salir: perder un prompt por una q sería peor que la tecla de menos")
		}
	})
}
