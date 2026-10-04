package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"vroom/internal/gitinfo"
	"vroom/internal/state"
	"vroom/internal/tail"
)

// Both halves are needed: clipping protects against content taller than the box, padding against content shorter, and a negative n must yield no lines at all.
func TestPadLinesRellenaYRecortaConElAltoExacto(t *testing.T) {
	tests := []struct {
		name  string
		in    []string
		n     int
		want  int
		check func([]string)
	}{
		{"rellena", []string{"a"}, 4, 4, func(g []string) {
			if g[1] != "" || g[3] != "" {
				t.Errorf("las líneas de relleno tienen que estar vacías: %q", g)
			}
		}},
		{"recorta", []string{"a", "b", "c", "d"}, 2, 2, func(g []string) {
			if g[1] != "b" {
				t.Errorf("el recorte tiene que conservar el principio: %q", g)
			}
		}},
		{"exacto", []string{"a", "b"}, 2, 2, nil},
		{"vacío a cero", []string{"a", "b"}, 0, 0, nil},
		{"negativo", []string{"a", "b"}, -5, 0, nil},
		{"vacío", nil, 3, 3, nil},
		{"más que el presupuesto", nil, 0, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := padLines(tt.in, tt.n)
			if len(got) != tt.want {
				t.Fatalf("len = %d, want %d: %q", len(got), tt.want, got)
			}
			if tt.check != nil {
				tt.check(got)
			}
		})
	}
}

// Width is measured in visible cells: len() would count ANSI bytes and make the compositor wrap the line, changing the box height.
func TestFitLinesRecortaCadaLineaAlAnchoVisible(t *testing.T) {
	lines := []string{
		strings.Repeat("x", 50),
		"\x1b[31m" + strings.Repeat("y", 50) + "\x1b[0m",
		strings.Repeat("€", 20),
	}
	got := fitLines(lines, 10)

	for i, l := range got {
		if n := lipglossWidth(l); n > 10 {
			t.Errorf("línea %d mide %d celdas visibles, want <= 10: %q", i, n, l)
		}
	}
	// Escaped content survives: fitLines truncates, it never strips escapes.
	if !strings.Contains(got[1], "\x1b[31m") {
		t.Errorf("el estilo se ha perdido al recortar: %q", got[1])
	}
	if n := lipglossWidth(got[2]); n > 10 {
		t.Errorf("con multibyte mide %d celdas, want <= 10", n)
	}
}

// The height is fixed because the Details box has a border and a title: fewer lines leave the bottom border floating, more lines push it off screen.
func TestDetailsContentLinesDevuelveSiempreElAltoDeLaCaja(t *testing.T) {
	conProyecto, _ := newTestModel(t)
	conProyecto = moveCursorTo(t, conProyecto, "tienda-api")
	// Branch, pattern and pid are added so the panel gets the maximum number of rows.
	path := pathOfSelected(t, conProyecto)
	conProyecto.branches[path] = "feature/una-rama-muy-larga-que-cabe"
	if p := conProyecto.projectByPath(path); p != nil && p.Manifest != nil {
		p.Manifest.ProcessPattern = "un.patron.de.proceso.largo"
	}
	conProyecto.services[path].Meta.Pid = 4321
	conProyecto.services[path].Meta.StartedAt = "2026-10-03 12:00:00"

	for _, w := range []int{30, 60, 140} {
		for _, m := range []Model{conProyecto} {
			m.width, m.height = w+40, 30
			m.updateLayout()
			got := m.detailsContentLines()
			if len(got) != detailsHeight {
				t.Errorf("w=%d con un panel de proyecto completo: %d líneas, want %d", w, len(got), detailsHeight)
			}
			for i, l := range got {
				if lipglossWidth(l) > m.rightW {
					t.Errorf("rightW=%d: línea %d mide %d celdas: el compositor la envolvería y el alto de la caja cambiaría",
						m.rightW, i, lipglossWidth(l))
				}
			}
		}
	}
}

// Both clamps matter: a top past the content indexes out of range after a refresh shrinks it, a negative one panics.
func TestDetailsContentLinesAplicaElScrollYNoSeSalePorEncima(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	base := tail.StripANSI(strings.Join(m.detailsContentLines(), "\n"))

	t.Run("scroll 0 es la posición natural", func(t *testing.T) {
		m.detailsTop = 0
		if got := tail.StripANSI(strings.Join(m.detailsContentLines(), "\n")); got != base {
			t.Error("detailsTop=0 tiene que dar el contenido sin desplazar")
		}
	})

	t.Run("scroll negativo se queda en cero", func(t *testing.T) {
		m.detailsTop = -10
		if got := tail.StripANSI(strings.Join(m.detailsContentLines(), "\n")); got != base {
			t.Error("un scroll negativo tiene que clampearse a 0, no mostrar basura del final")
		}
	})

	t.Run("scroll enorme se queda dentro", func(t *testing.T) {
		m.detailsTop = 10000
		got := m.detailsContentLines()
		if len(got) != detailsHeight {
			t.Fatalf("un scroll absurdo devolvió %d líneas, want %d", len(got), detailsHeight)
		}
	})
}

// The seven tabs are independent switch branches: an unexercised panel is indistinguishable from a panel one column short.
func TestConsoleContentLinesCubreLasSietePestanas(t *testing.T) {
	// A live service with metrics, env, git, events and health: the state in which all seven tabs have something to paint.
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	pid := livePID(t)
	markRunning(&m, path, pid)
	setManifestPort(&m, "tienda-api", 4321)

	m.metrics[path] = &metricsView{CPU: 3.5, RSSKB: 2048, FDs: 12, Threads: 4, At: time.Now()}
	m.envVars[path] = []string{"HOME=/root", "PATH=/usr/bin"}
	m.gitStatus[path] = gitinfo.Status{
		Branch:  "feature/login",
		Changed: []string{" M app.go"},
		Commits: []string{"abc1234 primer commit"},
	}
	m.events[path] = []timelineEvent{{At: time.Now(), Kind: "start", Detail: "arrancado", OK: true}}
	m.healthRes[path] = &healthResult{StatusCode: 200, Latency: 12 * time.Millisecond, ContentType: "text/html"}

	for k := tabKind(0); k < tabCount; k++ {
		m.activeTab = k
		got := m.consoleContentLines()
		if len(got) != m.contentH+1 {
			t.Errorf("pestaña %d: %d líneas, want %d (la barra + el contenido)", k, len(got), m.contentH+1)
		}
		if !strings.Contains(tail.StripANSI(got[0]), tabLabelText(k)) {
			t.Errorf("pestaña %d: la barra no la nombra: %q", k, got[0])
		}
	}
}

// The return is padded and clipped to an exact height on purpose: one line too many pushes the Output box border off screen.
func TestConsoleContentLinesConUnPanelDeMasLineasSeRecorta(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))

	vars := make([]string, 300)
	for i := range vars {
		vars[i] = "VARIABLE_DE_ENTORNO_LARGUISIMA_NUMERO_" + strings.Repeat("x", 30)
	}
	m.envVars[path] = vars
	m.activeTab = tabEnv

	got := m.consoleContentLines()
	if len(got) != m.contentH+1 {
		t.Errorf("con 300 variables devolvió %d líneas, want %d", len(got), m.contentH+1)
	}
	for i, l := range got {
		if n := lipglossWidth(l); n > m.rightW {
			t.Fatalf("línea %d mide %d celdas, want <= %d", i, n, m.rightW)
		}
	}
}

// "follow" and "paused" are opposite states the user must read at a glance, and a stopped service still says "pid -" because omitting it unbalances the bar and says nothing.
func TestTabsBarDistingueFollowYPausaYElPIDDeCadaPestaña(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 160, 30
	m.updateLayout()
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, 4321)

	t.Run("console: follow y paused", func(t *testing.T) {
		m.activeTab = tabConsole
		m.consoleFollow = true
		if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, "follow") || strings.Contains(got, "paused") {
			t.Errorf("con follow = %q", got)
		}
		m.consoleFollow = false
		if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, "paused") || strings.Contains(got, "follow") {
			t.Errorf("con paused = %q", got)
		}
	})

	t.Run("pestañas de muestreo: pid o pid —", func(t *testing.T) {
		for _, tab := range []tabKind{tabThreads, tabMetrics, tabEnv} {
			m.activeTab = tab
			if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, "pid 4321") {
				t.Errorf("pestaña %d con servicio vivo = %q, want el pid", tab, got)
			}
			m.services[path].Meta.Pid = 0
			if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, "pid —") {
				t.Errorf("pestaña %d con servicio parado = %q, want el texto pid —", tab, got)
			}
			markRunning(&m, path, 4321)
		}
	})

	t.Run("health: el puerto", func(t *testing.T) {
		setManifestPort(&m, "tienda-api", 4321)
		m.activeTab = tabHealth
		if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, ":4321") {
			t.Errorf("con puerto declarado = %q, want el puerto", got)
		}
	})

	t.Run("timeline: el número de eventos", func(t *testing.T) {
		m.activeTab = tabTimeline
		m.events[path] = []timelineEvent{{At: time.Now()}, {At: time.Now()}, {At: time.Now()}}
		if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, "3 events") {
			t.Errorf("con 3 eventos = %q", got)
		}
		m.events[path] = nil
		if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, "0 events") {
			t.Errorf("con 0 eventos = %q: el contador también es información", got)
		}
	})

	t.Run("barra estrecha: el rótulo se cae pero la barra se dibuja", func(t *testing.T) {
		// Below bar+label width the label is dropped whole: half of it would render "pid 43", a pid that does not exist.
		m.activeTab = tabConsole
		got := m.tabsBar(20)
		if lipglossWidth(got) > 20 {
			t.Errorf("la barra mide %d con un ancho de 20", lipglossWidth(got))
		}
		if strings.Contains(tail.StripANSI(got), "pid") {
			t.Errorf("no cabe un pid entero pero entró uno: %q", got)
		}
	})
}

// No selected project means no PID to show.
func TestTabsBarConCursorEnUnHeaderNoInventaRotulo(t *testing.T) {
	m, _ := newTestModel(t)
	cursorEn(t, &m, "tienda")

	for _, tab := range []tabKind{tabThreads, tabMetrics, tabEnv, tabHealth, tabTimeline} {
		m.activeTab = tab
		if got := tail.StripANSI(m.tabsBar(160)); strings.Contains(got, "pid ") {
			t.Errorf("pestaña %d sin servicio = %q: no puede inventar un pid", tab, got)
		}
	}
}

// The message lives at the bottom right because it is the only spot that does not push the two fixed help lines, and it must be clipped or the compositor would wrap it.
func TestKeybindsBoxTraeElMensajeDeEstadoAbajo(t *testing.T) {
	m, _ := newTestModel(t)

	sinMensaje := tail.StripANSI(m.keybindsBox())
	if len(sinMensaje) == 0 {
		t.Fatal("la caja de keybinds no puede estar vacía")
	}

	m.message = "servicioarrancado"
	withMensaje := tail.StripANSI(m.keybindsBox())
	if !strings.Contains(withMensaje, "servicioarrancado") {
		t.Errorf("el mensaje de estado no aparece: %q", withMensaje)
	}

	m.message = strings.Repeat("un-mensaje-muy-largo-", 50)
	corta := tail.StripANSI(m.keybindsBox())
	for i, l := range strings.Split(corta, "\n") {
		if lipglossWidth(l) > m.width {
			t.Errorf("línea %d del cuadro mide %d celdas con un mensaje enorme, want <= %d", i, lipglossWidth(l), m.width)
		}
	}

	m.message = ""
	if a, b := len(strings.Split(sinMensaje, "\n")), len(strings.Split(tail.StripANSI(m.keybindsBox()), "\n")); a != b {
		t.Errorf("la caja de keybinds cambia de alto según el mensaje: %d vs %d", a, b)
	}
}

// Without the label the user cannot tell "vroom is slow because it walks the tree" from "fd is missing": the 2s tick looks the same.
func TestKeybindsBoxDistingueWalkDeFD(t *testing.T) {
	m, _ := newTestModel(t)

	m.usedFD = false
	if got := tail.StripANSI(m.keybindsBox()); !strings.Contains(got, "walk") {
		t.Errorf("sin fd = %q, want la palabra walk", got)
	}
	m.usedFD = true
	if got := tail.StripANSI(m.keybindsBox()); !strings.Contains(got, "fd") {
		t.Errorf("con fd = %q, want la palabra fd", got)
	}
}

// The item under the cursor must always be visible: a list that navigates without showing the highlighted row is the most confusing way for a modal to look broken.
func TestPickerRowsDeslizaLaVentanaYCuentaLosOcultos(t *testing.T) {
	m := newStackModel(t)
	items := make([]pickerItem, 30)
	for i := range items {
		items[i] = pickerItem{Name: "tarea-" + string(rune('a'+i%26)) + "-" + strconv.Itoa(i)}
	}
	m.pickerItems = items

	const maxRows = 8
	for cur := range items {
		m.pickerCursor = cur
		rows, more := m.pickerRows(maxRows, 60)

		if len(rows) != maxRows {
			t.Fatalf("cursor %d: %d filas visibles, want %d", cur, len(rows), maxRows)
		}
		if got, want := maxRows+more, len(items); got != want {
			t.Fatalf("cursor %d: %d visibles + %d ocultos = %d, want %d", cur, len(rows), more, got, want)
		}
		if !strings.Contains(tail.StripANSI(rows[cur%maxRows]), items[cur].Name) &&
			!strings.Contains(tail.StripANSI(strings.Join(rows, "\n")), "▶ "+items[cur].Name) {
			t.Errorf("cursor %d: el elemento resaltado no está en la ventana: %q", cur, rows)
		}
		body := tail.StripANSI(strings.Join(rows, "\n"))
		if n := strings.Count(body, "▶"); n != 1 {
			t.Errorf("cursor %d: %d flechas en la ventana, want 1", cur, n)
		}
	}

	m.pickerCursor = 0
	rows, more := m.pickerRows(40, 60)
	if more != 0 || len(rows) != len(items) {
		t.Errorf("con todo dentro: %d filas y %d ocultos, want %d y 0", len(rows), more, len(items))
	}

	if rows, more := m.pickerRows(0, 60); len(rows) != 0 || more != len(items) {
		t.Errorf("con maxRows 0: %d filas y %d ocultos, want 0 y %d", len(rows), more, len(items))
	}
}

// The 28 floor matters: below it the modal box is narrower than its own text and the compositor wraps every row into an unreadable block.
func TestPickerTitleYAncho(t *testing.T) {
	m := newStackModel(t)

	cursorEn(t, &m, "tienda-api")
	if got := m.pickerTitle(); got != "tienda-api" {
		t.Errorf("con un proyecto = %q, want tienda-api", got)
	}

	cursorEn(t, &m, "front") // a stack, not a project
	if got := m.pickerTitle(); got != "?" {
		t.Errorf("sin proyecto = %q, want un interrogante", got)
	}

	m.width = 200
	m.pickerItems = []pickerItem{{Name: "corta"}, {Name: "una-tarea-con-el-nombre-mas-largo-de-lote"}}
	if w := m.pickerInnerW(); w < 28 {
		t.Errorf("el ancho del modal es %d, want >= 28: por debajo el compositor envuelve", w)
	}
	m.pickerItems = []pickerItem{{Name: "x", Description: strings.Repeat("d", 100)}}
	if w := m.pickerInnerW(); w > m.width-14 {
		t.Errorf("el ancho del modal es %d con una pantalla de %d: no puede exceder la pantalla", w, m.width)
	}
	m.pickerItems = nil
	if w := m.pickerInnerW(); w < 28 {
		t.Errorf("con la lista vacía el ancho es %d, want >= 28", w)
	}
}

// The help line is the contract, not decoration: this is the only modal in the program and a lost user has no other way out.
func TestPickerBoxDibujaElModalCompleto(t *testing.T) {
	m := newStackModel(t)
	m.pickerItems = []pickerItem{
		{Name: "build", Description: "compila el proyecto"},
		{Name: "test", Description: "lanza los tests"},
	}
	m.pickerCursor = 0

	body := tail.StripANSI(m.pickerBox())
	for _, want := range []string{"tasks", "build", "test", "j/k select", "esc close"} {
		if !strings.Contains(body, want) {
			t.Errorf("el modal no trae %q:\n%s", want, body)
		}
	}

	m.pickerKind = pickerAgents
	m.pickerItems = []pickerItem{{Name: "opencode"}, {Name: "claude"}}
	if body := tail.StripANSI(m.pickerBox()); !strings.Contains(body, "choose an agent") {
		t.Errorf("el modal de agentes tiene su propio título:\n%s", body)
	}

	m.pickerItems = make([]pickerItem, 200)
	for i := range m.pickerItems {
		m.pickerItems[i] = pickerItem{Name: "t" + strconv.Itoa(i)}
	}
	if body := tail.StripANSI(m.pickerBox()); !strings.Contains(body, "more") {
		t.Errorf("con 200 items tiene que decir cuántos no se ven:\n%s", body)
	}
}

// Two ceilings exist and only the screen one is enforced in production.
func TestPickerMaxRowsNuncaSaleDeLaPantallaNiSePasaDeLaLista(t *testing.T) {
	m := newStackModel(t)

	m.pickerItems = make([]pickerItem, 3)
	if got := m.pickerMaxRows(); got != 3 {
		t.Errorf("con 3 items y sitio de sobra = %d, want 3", got)
	}
	m.pickerItems = make([]pickerItem, 500)
	if got, want := m.pickerMaxRows(), m.bodyH-7; got != want {
		t.Errorf("con 500 items = %d, want %d (el alto de la pantalla manda)", got, want)
	}

	m.bodyH = 1
	m.pickerItems = make([]pickerItem, 500)
	if got := m.pickerMaxRows(); got < 3 {
		t.Errorf("con una pantalla de una línea = %d, want >= 3", got)
	}

	m.pickerItems = nil
	m.bodyH = 30
	if got := m.pickerMaxRows(); got != 0 {
		t.Errorf("sin items = %d, want 0", got)
	}
}

// The overlay clips the base instead of erasing it, which is what makes a modal a modal and not a screen change.
func TestOverlayCentraElBoxYConservaElAlrededor(t *testing.T) {
	base := strings.Repeat("linea-base\n", 10)
	box := strings.Repeat("X", 3)

	got := overlay(base, box, 20, 10)

	if !strings.Contains(got, "XXX") {
		t.Error("el box no se dibujó")
	}
	if n := strings.Count(got, "linea-base"); n < 6 {
		t.Errorf("sólo quedan %d líneas de base de 10: el overlay no puede borrar el contenido", n)
	}

	tall := strings.Repeat("X", 20)
	if got := overlay(base, tall, 20, 3); len(strings.Split(got, "\n")) != len(strings.Split(base, "\n")) {
		t.Errorf("un box más alto que la base cambió el alto: %d vs %d", len(strings.Split(got, "\n")), len(strings.Split(base, "\n")))
	}

	// MEDIDO: overlay does NOT clip the box to the screen; it clips the surrounding base content, so whoever sizes the box must make it fit (askInnerW is where that broke).
	if got = overlay("corta", strings.Repeat("W", 100), 20, 1); lipglossWidth(got) != 100 {
		t.Errorf("el box mide %d, want 100: overlay no lo recorta, quien lo llama tiene que encajarlo", lipglossWidth(got))
	}
}

// The modal width decides whether the tree and the boxes still fit underneath: wider than the terminal every line wraps and the layout comes apart, so the 28 floor is kept even if it overflows.
func TestAskInnerWNuncaDesbordaLaPantalla(t *testing.T) {
	for _, width := range []int{40, 60, 80, 86, 100, 124, 200, 300} {
		inner := askInnerW(width)
		if total := inner + boxFrame; total > width {
			t.Errorf("width=%d: el modal mide %d de ancho interior, want <= %d", width, inner, width-boxFrame)
		}
		if inner < 28 {
			t.Errorf("width=%d: ancho interior %d, want >= 28: por debajo el textarea no es usable", width, inner)
		}
	}

	if got := askInnerW(100); got != 86 {
		t.Errorf("askInnerW(100) = %d, want 86: en una pantalla normal manda lo que sobra", got)
	}
	if got := askInnerW(300); got != 110 {
		t.Errorf("askInnerW(300) = %d, want el cap de 110", got)
	}
	if got := askInnerW(20); got != 28 {
		t.Errorf("askInnerW(20) = %d, want 28: el suelo sobrevive a la pantalla estrecha", got)
	}
}

// An empty path would store under key "" and that event would then show up on every service reading m.events[""].
func TestAddEventIgnoraPathVacioYRecortaLaLista(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")

	m.addEvent("", "start", "sin servicio", time.Second, true)
	if len(m.events) != 0 {
		t.Errorf("un evento sin servicio se guardó: %v", m.events)
	}

	for i := range maxTimeline + 50 {
		m.addEvent(path, "start", strconv.Itoa(i), time.Second, true)
	}
	if got := len(m.events[path]); got != maxTimeline {
		t.Errorf("la lista tiene %d eventos, want %d", got, maxTimeline)
	}
	last := m.events[path][maxTimeline-1]
	if last.Detail != strconv.Itoa(maxTimeline+49) {
		t.Errorf("el último evento es %q, want el más reciente (%d)", last.Detail, maxTimeline+49)
	}
}

// With an unresolved port, showing and probing the declared one would point at a port that may belong to another worktree.
func TestHealthLinesDistingueSinPuertoDePuertoSinResolver(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	t.Run("sin puerto declarado", func(t *testing.T) {
		m.services[path].Meta = state.Meta{State: state.StateStopped}
		if p := m.projectByPath(path); p != nil {
			p.Manifest.Port = 0
		}
		if got := tail.StripANSI(strings.Join(m.healthLines(80), "\n")); !strings.Contains(got, "no port configured") {
			t.Errorf("= %q, want que pida declarar un puerto", got)
		}
	})

	t.Run("puerto sin resolver", func(t *testing.T) {
		setManifestPort(&m, "tienda-api", 4321)
		m.services[path].Meta = state.Meta{State: state.StatePortUnresolved, Port: 4321}
		if got := tail.StripANSI(strings.Join(m.healthLines(80), "\n")); !strings.Contains(got, "unresolved") {
			t.Errorf("= %q, want que diga que el puerto no se resolvió", got)
		}
	})

	t.Run("configurado y vivo sin probe todavía", func(t *testing.T) {
		setManifestPort(&m, "tienda-api", 4321)
		markRunning(&m, path, livePID(t))
		m.healthRes[path] = nil
		if got := tail.StripANSI(strings.Join(m.healthLines(80), "\n")); !strings.Contains(got, "probing") {
			t.Errorf("= %q, want que diga que la sonda está en curso", got)
		}
	})

	t.Run("probe fallido muestra el motivo y la URL", func(t *testing.T) {
		markRunning(&m, path, livePID(t))
		m.healthRes[path] = &healthResult{Err: "connection refused"}
		body := tail.StripANSI(strings.Join(m.healthLines(80), "\n"))
		if !strings.Contains(body, "connection refused") {
			t.Errorf("un probe fallido tiene que decir por qué: %q", body)
		}
		if !strings.Contains(body, "url:") {
			t.Errorf("un probe fallido tiene que enseñar la URL para probarla a mano: %q", body)
		}
	})
}

// The three tiers are visibly different: 900 MB shown as "943718.4 KB" is unreadable, and so is 2 GB shown as "2048.0 KB".
func TestHumanKBUsaLaUnidadQueTocaYNoSePasaDeKB(t *testing.T) {
	tests := []struct {
		kb   int64
		want string
	}{
		{0, "0 KB"},
		{1, "1 KB"},
		{1023, "1023 KB"},
		{1024, "1.0 MB"},
		{1536, "1.5 MB"},
		{1024*1024 - 1, "1024.0 MB"},
		// The input is in KiB, so a GB is 1024*1024 KiB.
		{1024 * 1024, "1.0 GB"},
		{3 * 1024 * 1024, "3.0 GB"},
		{1024*1024*1024 - 1, "1024.0 GB"},
	}
	for _, tt := range tests {
		if got := humanKB(tt.kb); got != tt.want {
			t.Errorf("humanKB(%d) = %q, want %q", tt.kb, got, tt.want)
		}
	}
}
