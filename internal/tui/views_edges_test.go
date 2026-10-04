package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/orchestrate"
	"vroom/internal/portless"
	"vroom/internal/state"
	"vroom/internal/tail"
)

func cursorEn(t *testing.T, m *Model, want string) {
	t.Helper()
	rows, _ := m.treeLines()
	for i, r := range rows {
		if strings.Contains(r, want) {
			m.cursor = i
			return
		}
	}
	t.Fatalf("no hay ninguna fila del árbol que contenga %q: %v", want, rows)
}

// The three reasons for having no rows are distinct: group node, no manifest, stopped; conflating them would read as broken sampling.
func TestThreadsLinesSobreUnHeaderInvitaAPick(t *testing.T) {
	m, _ := newTestModel(t)
	cursorEn(t, &m, "tienda")

	lines := m.threadsLines(m.rightW, m.contentH)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want un único mensaje", lines)
	}
	if !strings.Contains(lines[0], "pick a service") {
		t.Errorf("sobre un nodo de grupo = %q, want una invitación a elegir servicio", lines[0])
	}
}

// nil, not an empty line: the panel must tell "nothing to show" apart from "a message exists".
func TestThreadsLinesSinProyectoYSinHeaderNoDevuelveNada(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = -1 // out of range: selectedItem fails and there is no header

	if lines := m.threadsLines(m.rightW, m.contentH); lines != nil {
		t.Errorf("lines = %v con el cursor fuera del árbol, want nil: un []string{\"\"} dibujaría una línea en blanco", lines)
	}
}

// The flag is flipped on the ALREADY BUILT tree on purpose: a hand-injected project could be dropped by buildTree's grouping rules and the test would prove nothing.
func TestThreadsLinesDistingueSinManifiestoDeParado(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "suelto")
	path := pathOfSelected(t, m)

	m.services[path] = &ServiceState{Status: statusStopped}
	if p := m.projectByPath(path); p != nil {
		p.Configured = false
	}
	it, ok := m.selectedItem()
	if !ok {
		t.Fatal("el cursor dejó de estar sobre el proyecto")
	}
	it.project.Configured = false
	m.tree[m.cursor] = it

	lines := m.threadsLines(m.rightW, m.contentH)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want un único mensaje", lines)
	}
	if !strings.Contains(lines[0], "No manifest") {
		t.Errorf("línea = %q, want que pida crear un .vroom.toml", lines[0])
	}
	if strings.Contains(lines[0], "not running") {
		t.Errorf("línea = %q: confundir \"sin manifiesto\" con \"parado\" manda al usuario a pulsar start sobre algo que no arranca", lines[0])
	}
}

func TestThreadsLinesDiceQueEstaTomandoLaPrimeraMuestra(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, 4242)

	lines := m.threadsLines(m.rightW, m.contentH)
	if len(lines) != 1 || !strings.Contains(lines[0], "sampling") {
		t.Errorf("lines = %v, want un aviso de sampling en curso", lines)
	}
}

func TestThreadsLinesCuentaLosHilosQueNoCaben(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, 4242)

	rows := make([]threadRow, 40)
	for i := range rows {
		rows[i] = threadRow{TID: i + 1, Name: "hilo", State: "S", CPU: float64(i)}
	}
	m.threads[path] = rows

	lines := m.threadsLines(80, 6)
	body := strings.Join(lines, "\n")

	if !strings.Contains(body, "NAME") {
		t.Errorf("la tabla tiene que traer su header: %q", body)
	}
	if !strings.Contains(body, "more threads") {
		t.Errorf("con 40 hilos y 6 filas hay que decir cuántos sobran: %q", body)
	}
	if len(lines) > 6 {
		t.Errorf("la tabla devolvió %d líneas para un alto de 6", len(lines))
	}
	if alto := m.threadsLines(200, 60); strings.Contains(strings.Join(alto, "\n"), "more threads") {
		t.Error("con toda la tabla dentro, decir \"more threads\" es ruido")
	}
}

// The name column keeps a minimum width even when the panel cannot afford it, or the header degrades into a wall of format verbs.
func TestThreadsLinesAguantaUnPanelEstrechisimo(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, 4242)
	m.threads[path] = []threadRow{
		{TID: 1, Name: "un-nombre-de-hilo-muy-largo", State: "R", CPU: 12.5},
	}

	for _, w := range []int{0, 1, 5, 12, 21, 40} {
		lines := m.threadsLines(w, 10)
		if len(lines) < 2 {
			t.Fatalf("w=%d: lines = %v, want header + al menos una fila", w, lines)
		}
		if !strings.Contains(lines[0], "NAME") {
			t.Errorf("w=%d: header = %q", w, lines[0])
		}
		// MEDIDO: at height 1 the panel emits TWO lines (header plus "+N more"): overflowing a 2-row panel beats showing the label alone.
		if got := len(m.threadsLines(w, 1)); got != 2 {
			t.Errorf("w=%d, h=1: %d líneas, want 2 (header + \"more\"): con una fila no cabe ni el rótulo solo", w, got)
		}
		if got := len(m.threadsLines(w, 5)); got > 5 {
			t.Errorf("w=%d, h=5: %d líneas, want <= 5", w, got)
		}
	}
}

// This panel answers "why did stop kill that?": a stack name alone leaves the scope of the pressed key to guesswork.
func TestStackDetailsLinesExplicaElStack(t *testing.T) {
	m := newStackModel(t)
	cursorEn(t, &m, "front")

	stack := m.selectedStack()
	if stack == nil {
		t.Fatalf("el cursor no cayó en un stack: %d", m.selectedItemKind())
	}
	body := tail.StripANSI(strings.Join(m.stackDetailsLines(stack, m.rightW), "\n"))

	for _, want := range []string{
		"front",
		"orchestration stack",
		"stages:",
		"services:",
		"group:",
		"tienda-web",
		"tienda-api",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("el panel del stack no menciona %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "services: 2 (0 running)") {
		t.Errorf("conteo de servicios = %q, want los dos del stack y cero corriendo:\n%s", body, body)
	}
}

// The header verdict is what makes the user press stop or start, so showing both would be worse than showing neither.
func TestStackDetailsLinesDistingueCorriendoYParado(t *testing.T) {
	t.Run("parado", func(t *testing.T) {
		m := newStackModel(t)
		cursorEn(t, &m, "front")
		stack := m.selectedStack()
		body := tail.StripANSI(strings.Join(m.stackDetailsLines(stack, m.rightW), "\n"))
		if !strings.Contains(body, "stopped") {
			t.Errorf("un stack sin servicios vivos tiene que decir stopped:\n%s", body)
		}
		if strings.Contains(body, "running ●") {
			t.Errorf("no puede decir running con nada vivo:\n%s", body)
		}
	})

	t.Run("corriendo", func(t *testing.T) {
		m := newStackModel(t)
		cursorEn(t, &m, "front")
		stack := m.selectedStack()
		markRunning(&m, projectPath(t, m, "tienda-api"), livePID(t))
		body := tail.StripANSI(strings.Join(m.stackDetailsLines(stack, m.rightW), "\n"))
		if !strings.Contains(body, "running") {
			t.Errorf("con un servicio vivo el header tiene que decir running:\n%s", body)
		}
		if !strings.Contains(body, "services: 2 (1 running)") {
			t.Errorf("conteo = %q, want 2 con 1 corriendo:\n%s", body, body)
		}
	})
}

// Same rule as the engine and the CLI: an ambiguous name is never resolved to the first match.
func TestStackDetailsLinesAvisaDeUnNombreAmbiguoSinRomperElPanel(t *testing.T) {
	m := newStackModel(t)

	dup := m.projectByPath(projectPath(t, m, "tienda-api"))
	clone := *dup
	clone.Path = filepath.Join(t.TempDir(), "otro-api")
	clone.Name = "tienda-api"
	m.projects = append(m.projects, clone)
	m.tree = m.buildTree()

	stack := &orchestrate.Stack{
		Name:         "ambiguo",
		PrimaryGroup: "tienda",
		Stages: []orchestrate.Stage{
			{Name: "e1", Services: []string{"tienda-api", "no-existe"}},
		},
	}
	body := tail.StripANSI(strings.Join(m.stackDetailsLines(stack, m.rightW), "\n"))

	if !strings.Contains(body, "no-existe") {
		t.Errorf("el servicio irresoluble tiene que aparecer igualmente:\n%s", body)
	}
	if !strings.Contains(body, "⚠") {
		t.Errorf("un servicio que no resuelve tiene que bring aviso:\n%s", body)
	}
	if !strings.Contains(body, "ambiguo") || !strings.Contains(body, "stages:") {
		t.Errorf("un aviso no puede vaciar el panel:\n%s", body)
	}
}

// Showing the port on the service's own line is what lets the panel be compared with the tree without memorising the port table.
func TestStackDetailsLinesMuestraElPuertoDeCadaServicio(t *testing.T) {
	m := newStackModel(t)
	// tienda-api's manifest declares 8081
	path := projectPath(t, m, "tienda-api")
	markRunning(&m, path, livePID(t))

	stack := &orchestrate.Stack{
		Name:         "front",
		PrimaryGroup: "tienda",
		Stages:       []orchestrate.Stage{{Name: "e", Services: []string{"tienda-api", "tienda-web"}}},
	}
	body := tail.StripANSI(strings.Join(m.stackDetailsLines(stack, m.rightW), "\n"))

	if !strings.Contains(body, "8081") {
		t.Errorf("el puerto declarado del servicio tiene que estar en su línea:\n%s", body)
	}
	if strings.Contains(body, "999999") {
		t.Errorf("un puerto que no existe no puede aparecer:\n%s", body)
	}
}

// allDetailsLines must pick the stack by the cursor being on it, not by name: picking by name would show "front: 2 services" for another service.
func TestStackDetailsLinesSeEligePorSobreUnStackNoPorNombre(t *testing.T) {
	m := newStackModel(t)

	m = moveCursorTo(t, m, "tienda-api")
	body := tail.StripANSI(strings.Join(m.detailsLines(m.rightW), "\n"))
	if strings.Contains(body, "[stack]") {
		t.Errorf("con un proyecto seleccionado el panel no puede ser el del stack:\n%s", body)
	}
	if !strings.Contains(body, "path:") {
		t.Errorf("con un proyecto seleccionado tiene que ser su panel de detalle:\n%s", body)
	}

	cursorEn(t, &m, "front")
	body = tail.StripANSI(strings.Join(m.detailsLines(m.rightW), "\n"))
	if !strings.Contains(body, "[stack]") {
		t.Errorf("con el cursor sobre el stack tiene que ser su panel:\n%s", body)
	}
}

// A blank line would leave the user unable to tell a failure from a plain lack of selection.
func TestDetailsLinesSinNadaSeleccionadoLoDice(t *testing.T) {
	m, _ := newTestModel(t)
	m.tree = nil

	body := tail.StripANSI(strings.Join(m.detailsLines(m.rightW), "\n"))
	if !strings.Contains(body, "No project selected") {
		t.Errorf("sin selección el panel tiene que decirlo:\n%s", body)
	}
}

// Rows are clipped first: clipping columns of a line that is never drawn is wasted, and in the other order an ANSI line could count escape bytes as width and split a rune.
func TestClipLinesRecortaFilasYColumnas(t *testing.T) {
	lines := []string{"uno", "dos", "tres", "cuatro"}

	got := clipLines(lines, 2, 40)
	if len(got) != 2 || got[0] != "uno" || got[1] != "dos" {
		t.Errorf("con h=2 = %v, want las dos primeras", got)
	}
	// clipLines clips in place on purpose; the result is what matters.
	if h, w := 10, 3; len(clipLines([]string{"abcdefghij"}, h, w)) != 1 {
		t.Error("una sola línea con h=10 tiene que quedarse en una")
	}
	wide := clipLines([]string{"abcdefghij"}, 10, 4)
	if len(tail.StripANSI(wide[0])) > 4 {
		t.Errorf("clipLines no recortó columnas: %q (%d)", wide[0], len(tail.StripANSI(wide[0])))
	}
}

// A long label is left overflowing on purpose: truncating it would break the alignment of the columns behind it.
func TestPadRellenaARunesYNoTocaLoQueNoCabe(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"port:", 10, "port:     "},
		{"", 4, "    "},
		// MEDIDO: "exacta10" is 8 characters, so it is padded anyway; the case name was misleading.
		{"exacta10", 10, "exacta10  "},
		{"demasiado", 4, "demasiado"},
	}
	for _, tt := range tests {
		if got := pad(tt.in, tt.n); got != tt.want {
			t.Errorf("pad(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}

	for _, s := range []string{"url:", "ruta:", "puerto:"} {
		if n := len(pad(s, 10)); n != 10 {
			t.Errorf("pad(%q, 10) mide %d bytes, want 10: el relleno tiene que contar runes", s, n)
		}
	}
	got := pad("añadir", 10)
	if len([]rune(got)) != 10 {
		t.Errorf("pad con multibyte mide %d runes, want 10: %q", len([]rune(got)), got)
	}
}

// RouteOwned=false means the handle already belonged to someone else, and the meta keeps the name so reconciliation still knows where to look.
func TestReleaseRouteNoRetiraLoQueNoEraSuyo(t *testing.T) {
	meta := &state.Meta{RouteName: "vroom-test-ajena", RouteOwned: false}
	releaseRoute(meta)

	if meta.RouteOwned {
		t.Error("RouteOwned = true tras retirar una ruta que no era nuestra")
	}
	if meta.RouteName != "vroom-test-ajena" {
		t.Errorf("RouteName = %q: la ruta de otro no puede desaparecer del meta", meta.RouteName)
	}
}

// Both halves must hold at once: removing without revoking leaves ownership on a route that is gone, revoking without removing leaves a zombie route the next service cannot take.
func TestReleaseRouteRevocaLaPropiedadCuandoLaRetiradaSurtioEfecto(t *testing.T) {
	rec := &recordingReleaser{}
	installRouteStub(t, rec)

	meta := &state.Meta{RouteName: "vroom-test-mia", RouteOwned: true}
	releaseRoute(meta)

	if len(rec.removed) != 1 || rec.removed[0] != "vroom-test-mia" {
		t.Errorf("removed = %v, want exactamente [vroom-test-mia]", rec.removed)
	}
	if meta.RouteOwned {
		t.Error("la retirada surtió efecto pero la propiedad sigue concedida: la ruta queda zombie")
	}
}

// Failing closed: if the route is already gone (someone else removed it, or it was never taken) revoking would lie about the state, while keeping it lets reconciliation retry.
func TestReleaseRouteNoRevocaSiLaRetiradaNoSurtioEfecto(t *testing.T) {
	// A releaser that reports "was not there", which is what Remove returns on error.
	t.Cleanup(func() {
		tuiReleaseStub = nil
		routeStubInstalled = false
	})
	tuiReleaseStub = func(name string) error { return os.ErrNotExist }
	routeStubInstalled = true

	meta := &state.Meta{RouteName: "vroom-test-ya-no-existe", RouteOwned: true}
	releaseRoute(meta)

	if !meta.RouteOwned {
		t.Error("la retirada no surtió efecto pero se revocó la propiedad: la reconciliación pierde el rastro")
	}
}

// The flag-plus-nil combination is the check order: returning a Releaser wrapped around a nil func would SIGSEGV on the first Remove.
func TestTuiRouteReleaserEligeElStubYSinoElInerte(t *testing.T) {
	t.Run("stub instalado y con puntero: es el stub", func(t *testing.T) {
		rec := &recordingReleaser{}
		installRouteStub(t, rec)

		got := tuiRouteReleaser()
		if got == nil {
			t.Fatal("con el stub instalado tiene que salir el stub, no nil: nil es el cliente REAL")
		}
		if err := got.RemoveAbsent("vroom-test-x"); err != nil {
			t.Errorf("el releaser devuelto no es el del test: %v", err)
		}
		if len(rec.removed) != 1 {
			t.Errorf("la llamada no fue a parar al stub del test: %v", rec.removed)
		}
	})

	t.Run("flag puesto pero puntero nil: cae al inerte y no revienta", func(t *testing.T) {
		// This is what a test that sets the flag and forgets the stub does; a nil-func Remove would crash in production under a badly written test.
		t.Cleanup(func() {
			tuiReleaseStub = nil
			routeStubInstalled = false
		})
		routeStubInstalled = true
		tuiReleaseStub = nil

		got := tuiRouteReleaser()
		if got == nil {
			t.Fatal("un flag a medias tiene que caer al inerte, no a nil: nil significa cliente real")
		}
		if err := got.RemoveAbsent("vroom-cualquiera"); err != nil {
			t.Errorf("el inerte devolvió error %v: su trabajo es NO hacer nada", err)
		}
	})

	t.Run("sin flag en un test binary: el inerte", func(t *testing.T) {
		// No test can reach the real-client branch because IsTestBinary() is always true inside a .test, and returning nil there would make a test connect to the real proxy.
		if !portless.IsTestBinary() {
			t.Skip("este binario no es un test binary: el caso no aplica")
		}
		if got := tuiRouteReleaser(); got == nil {
			t.Error("sin stub dentro de un test binary tiene que salir el inerte, no nil")
		}
	})
}
