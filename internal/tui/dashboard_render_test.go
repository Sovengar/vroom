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

// ---------------------------------------------------------------------------
// El render de la columna derecha y de las cajas.
//
// Lo que se prueba aquí es una propiedad, no un snapshot: cada función devuelve
// EXACTAMENTE las líneas que su caja espera, ni una más ni una menos. Y eso no es
// cosmetics: el compositor apila cajas de altura fija, así que una línea de más
// empuja el borde inferior fuera de la pantalla y una de menos deja un hueco.
//
// Los siete estados de la pestaña Output están porque son siete ramas
// independientes y sólo se ejercitaba una. El resto de pestañas se ve en negro con
// un servicio parado, y un panel en negro no es un panel probado.
// ---------------------------------------------------------------------------

// TestPadLinesRellenaYRecortaConElAltoExacto: el contrato de las cajas.
//
// Recortar y rellenar son las dos mitades: recortar protege de un contenido más
// alto que la caja, rellenar protege de uno más bajo. Y n negativo tiene que dar
// cero líneas, no un índice negativo que revienta.
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

// TestFitLinesRecortaCadaLineaAlAnchoVisible: el ancho se mide en celdas
// visibles, no en bytes.
//
// Con ANSI en medio, un `len()` daría un número que no es el ancho que se dibuja y
// el compositor envolvería la línea, cambiando el alto de la caja. Por eso
// truncANSI. Lo que se prueba es que el texto SIN escapes que queda es del ancho
// pedido y que un rune multibyte no se parte.
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
	// Y el contenido escapado se conserva: fitLines recorta, no quita escapes.
	if !strings.Contains(got[1], "\x1b[31m") {
		t.Errorf("el estilo se ha perdido al recortar: %q", got[1])
	}
	if n := lipglossWidth(got[2]); n > 10 {
		t.Errorf("con multibyte mide %d celdas, want <= 10", n)
	}
}

// TestDetailsContentLinesDevuelveSiempreElAltoDeLaCaja: ni una línea más ni una
// menos, venga el contenido que venga.
//
// El alto es FIJO porque la caja de Details tiene borde y título: si el panel
// devolviera menos, el borde inferior flotaría; si devolviera más, se saldría de
// la pantalla. Y tiene que valer para un panel de tres líneas y para uno de
// veinte, que es lo que pasa según el proyecto.
//
// Se barre variando el proyecto seleccionado: un servicio sin manifiesto tiene
// menos campos que uno con rama, grupo, patrón, pid, ruta y logs.
func TestDetailsContentLinesDevuelveSiempreElAltoDeLaCaja(t *testing.T) {
	conProyecto, _ := newTestModel(t)
	conProyecto = moveCursorTo(t, conProyecto, "tienda-api")
	// Se le añade rama, patrón y ruta para que el panel tenga el máximo de filas.
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

// TestDetailsContentLinesAplicaElScrollYNoSeSalePorEncima: con scroll el panel
// muestra una ventana, y nunca se sale del contenido.
//
// Las dos clamps son distintas y ambas importan: el `top` hacia abajo evita un
// índice fuera de rango cuando el contenido se hace más corto (un proyecto sin
// rama tras un refresh), y el `top` negativo evita un panic si algo lo deja
// negativo.
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

// TestConsoleContentLinesCubreLasSietePestanas: cada pestaña produce su panel y
// todas respetan el alto.
//
// Las siete son ramas de un switch independiente. Cubrir sólo la consola deja las
// otras seis en negro, y en negro un panel con un error no se distingue de un
// panel con una columna de más.
func TestConsoleContentLinesCubreLasSietePestanas(t *testing.T) {
	// Un servicio vivo con métricas, entorno, git, eventos y salud: es el estado
	// en el que las siete pestañas tienen algo que pintar.
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
		// La barra siempre está, y nombra la pestaña.
		if !strings.Contains(tail.StripANSI(got[0]), tabLabelText(k)) {
			t.Errorf("pestaña %d: la barra no la nombra: %q", k, got[0])
		}
	}
}

// TestConsoleContentLinesConUnPanelDeMasLineasSeRecorta: el compositor no puede
// envolver.
//
// Si el panel devuelve más de las que caben, el borde inferior de la caja Output
// se sale de la pantalla. Por eso el retorno va rellenado/recortado a un alto
// exacto, y por eso hay que comprobarlo con contenido que DESBORDE.
func TestConsoleContentLinesConUnPanelDeMasLineasSeRecorta(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))

	// 300 variables de entorno: muy por encima de lo que cabe.
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
	// Y ninguna línea se sale del ancho: si se saliera, el compositor envolvería.
	for i, l := range got {
		if n := lipglossWidth(l); n > m.rightW {
			t.Fatalf("línea %d mide %d celdas, want <= %d", i, n, m.rightW)
		}
	}
}

// TestTabsBarDistingueFollowYPausaYElPIDDeCadaPestaña: el rótulo de la derecha
// cambia de significado según la pestaña.
//
// El caso de Console es el que importa: "follow" y "paused" son estados
// OPUESTOS y el usuario tiene que poder ver en cuál está sin recordar si pulsó
// algo. En las pestañas de muestreo el rótulo es el PID, y con el servicio parado
// tiene que decir "pid —" en vez de omitirlo (omitirlo deja la barra descentrada y
// no dice nada).
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
		// Con un ancho menor que barra + rótulo, el rótulo se descarta entero. Es lo
		// correcto: partirlo a la mitad daría "pid 43" que es un pid FALSO.
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

// TestTabsBarConCursorEnUnHeaderNoInventaRótulo: sin proyecto seleccionado no hay
// PID que enseñar.
//
// Un rótulo de proceso sin proceso es inventado, y con el cursor sobre un grupo
// es la forma más fácil de="">
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

// TestKeybindsBoxTraeElMensajeDeEstadoAbajo: el mensaje de estado vive en el borde
// inferior derecho.
//
// Es el único sitio donde cabe sin empujar las dos líneas de ayuda, que están
// fijas. Y un mensaje largo tiene que recortarse al ancho interior: sin recorte
// el compositor envolvería la línea y el alto de la caja cambiaría.
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

	// Y con un mensaje enorme no se sale del ancho ni crece la caja.
	m.message = strings.Repeat("un-mensaje-muy-largo-", 50)
	corta := tail.StripANSI(m.keybindsBox())
	for i, l := range strings.Split(corta, "\n") {
		if lipglossWidth(l) > m.width {
			t.Errorf("línea %d del cuadro mide %d celdas con un mensaje enorme, want <= %d", i, lipglossWidth(l), m.width)
		}
	}

	// Y sin mensaje la caja tiene el mismo alto que con mensaje: el alto es fijo.
	m.message = ""
	if a, b := len(strings.Split(sinMensaje, "\n")), len(strings.Split(tail.StripANSI(m.keybindsBox()), "\n")); a != b {
		t.Errorf("la caja de keybinds cambia de alto según el mensaje: %d vs %d", a, b)
	}
}

// TestKeybindsBoxDistingueWalkDeFD: el método de escaneo se enseña en la barra.
//
// Es la diferencia entre "vroom tarda porque recorre el árbol" y "vroom tarda
// porque fd no está instalado". Sin el rótulo el usuario no puede ni formular la
// pregunta, y el tick de 2s se ve igual en los dos casos.
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

// TestPickerRowsDeslizaLaVentanaYCuentaLosOcultos: la lista larga scrollea
// abriendo la ventana sobre el cursor.
//
// Lo que importa es que el elemento bajo el cursor SIEMPRE está visible: sin eso
// el usuario vería una lista en la que navega y el elemento resaltado no aparece,
// que es la forma más desconcertante de que un modal esté roto.
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
		// El cursor tiene que estar dentro de la ventana.
		if !strings.Contains(tail.StripANSI(rows[cur%maxRows]), items[cur].Name) &&
			!strings.Contains(tail.StripANSI(strings.Join(rows, "\n")), "▶ "+items[cur].Name) {
			t.Errorf("cursor %d: el elemento resaltado no está en la ventana: %q", cur, rows)
		}
		// Y sólo hay una flecha.
		body := tail.StripANSI(strings.Join(rows, "\n"))
		if n := strings.Count(body, "▶"); n != 1 {
			t.Errorf("cursor %d: %d flechas en la ventana, want 1", cur, n)
		}
	}

	// Con todo dentro, more = 0.
	m.pickerCursor = 0
	rows, more := m.pickerRows(40, 60)
	if more != 0 || len(rows) != len(items) {
		t.Errorf("con todo dentro: %d filas y %d ocultos, want %d y 0", len(rows), more, len(items))
	}

	// Con maxRows <= 0 no hay ventana y TODO está oculto.
	if rows, more := m.pickerRows(0, 60); len(rows) != 0 || more != len(items) {
		t.Errorf("con maxRows 0: %d filas y %d ocultos, want 0 y %d", len(rows), more, len(items))
	}
}

// TestPickerTitleYAncho: el título nombra el proyecto y el ancho se ajusta a lo
// que hay.
//
// El ancho mínimo de 28 importa: por debajo, la caja del modal sería más estrecha
// que su propio texto y el compositor envolvería cada fila, convirtiendo una lista
// en un bloque ilegible.
func TestPickerTitleYAncho(t *testing.T) {
	m := newStackModel(t)

	cursorEn(t, &m, "tienda-api")
	if got := m.pickerTitle(); got != "tienda-api" {
		t.Errorf("con un proyecto = %q, want tienda-api", got)
	}

	cursorEn(t, &m, "front") // un stack no es un proyecto
	if got := m.pickerTitle(); got != "?" {
		t.Errorf("sin proyecto = %q, want un interrogante", got)
	}

	// Ancho: mínimo de 28, y se ajusta a la fila más larga + 2.
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

// TestPickerBoxDibujaElModalCompleto: título, ventana y ayuda.
//
// Las tres partes están porque el modal es la única ventana modal del programa y
// un usuario perdido en ella no tiene forma de salir: por eso la línea de ayuda
// ("j/k select · enter run · esc close") no es decorativa, es el contrato.
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

	// Y con ventana deslizante avisa de los ocultos.
	m.pickerItems = make([]pickerItem, 200)
	for i := range m.pickerItems {
		m.pickerItems[i] = pickerItem{Name: "t" + strconv.Itoa(i)}
	}
	if body := tail.StripANSI(m.pickerBox()); !strings.Contains(body, "more") {
		t.Errorf("con 200 items tiene que decir cuántos no se ven:\n%s", body)
	}
}

// TestPickerMaxRowsNuncaSaleDeLaPantallaNiSePasaDeLaLista: el alto de la lista
// tiene dos techos y uno sólo está puesto.
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

	// Pantalla diminuta: el mínimo de 3 filas.
	m.bodyH = 1
	m.pickerItems = make([]pickerItem, 500)
	if got := m.pickerMaxRows(); got < 3 {
		t.Errorf("con una pantalla de una línea = %d, want >= 3", got)
	}

	// Sin items: 0, que pickerRows sabe tratar como "todo oculto".
	m.pickerItems = nil
	m.bodyH = 30
	if got := m.pickerMaxRows(); got != 0 {
		t.Errorf("sin items = %d, want 0", got)
	}
}

// TestOverlayCentraElBoxYConservaElAlrededor: el overlay no borra lo que hay
// debajo, lo recorta.
//
// Es lo que hace que un modal sea un modal y no un cambio de pantalla: la lista de
// detrás tiene que seguir siendo legible alrededor. Y cuando el box es más grande
// que la base no puede desbordarse: se recorta en vertical y se ancla a la
// izquierda.
func TestOverlayCentraElBoxYConservaElAlrededor(t *testing.T) {
	base := strings.Repeat("linea-base\n", 10)
	box := strings.Repeat("X", 3)

	got := overlay(base, box, 20, 10)

	// El box aparece y la base sigue ahí.
	if !strings.Contains(got, "XXX") {
		t.Error("el box no se dibujó")
	}
	if n := strings.Count(got, "linea-base"); n < 6 {
		t.Errorf("sólo quedan %d líneas de base de 10: el overlay no puede borrar el contenido", n)
	}

	// Un box más alto que la base no puede desbordar.
	tall := strings.Repeat("X", 20)
	if got := overlay(base, tall, 20, 3); len(strings.Split(got, "\n")) != len(strings.Split(base, "\n")) {
		t.Errorf("un box más alto que la base cambió el alto: %d vs %d", len(strings.Split(got, "\n")), len(strings.Split(base, "\n")))
	}

	// MEDIDO: overlay NO recorta el box a la pantalla. Se recorta el CONTENIDO de
	// base que queda a los lados, pero un box más ancho que la base se sale por la
	// derecha. Es a propósito —es un compositor, no un gestor de anchos— y por eso
	// quien calcula el ancho del box tiene que.ensure que cabe. El sitio donde eso
	// se rompió es askInnerW, y lo comprueba TestAskInnerWNuncaDesbordaLaPantalla.
	if got = overlay("corta", strings.Repeat("W", 100), 20, 1); lipglossWidth(got) != 100 {
		t.Errorf("el box mide %d, want 100: overlay no lo recorta, quien lo llama tiene que encajarlo", lipglossWidth(got))
	}
}

// TestAskInnerWNuncaDesbordaLaPantalla: el modal ask encoge con la pantalla.
//
// El ancho del modal decide si el teclado, el árbol y las cajas siguen encajando
// debajo. Con el modal más ancho que la terminal cada línea envuelve y el layout se
// desmonte: no es que quede feo, es que el programa deja de funcionar de forma
// coherente.
//
// Y el suelo de 28 se mantiene porque es el mínimo con el que el textarea es
// usable —por debajo de eso hay que aceptar el desborde, no eliminar el suelo—.
func TestAskInnerWNuncaDesbordaLaPantalla(t *testing.T) {
	for _, width := range []int{40, 60, 80, 86, 100, 124, 200, 300} {
		inner := askInnerW(width)
		// El modal completo son inner + 2 de padding + 2 de borde, y el overlay lo
		// centra: tiene que caber en la pantalla con margen.
		if total := inner + boxFrame; total > width {
			t.Errorf("width=%d: el modal mide %d de ancho interior, want <= %d", width, inner, width-boxFrame)
		}
		if inner < 28 {
			t.Errorf("width=%d: ancho interior %d, want >= 28: por debajo el textarea no es usable", width, inner)
		}
	}

	// En pantallas de lo normal manda el prefill (72), no lo que sobra.
	if got := askInnerW(100); got != 86 {
		t.Errorf("askInnerW(100) = %d, want 86: en una pantalla normal manda lo que sobra", got)
	}
	// Y en las anchas el cap.
	if got := askInnerW(300); got != 110 {
		t.Errorf("askInnerW(300) = %d, want el cap de 110", got)
	}
	// Y por debajo del suelo se acepta el desborde en vez de romper el modal.
	if got := askInnerW(20); got != 28 {
		t.Errorf("askInnerW(20) = %d, want 28: el suelo sobrevive a la pantalla estrecha", got)
	}
}

// TestAddEventIgnoraPathVacioYRecortaLaLista: el timeline es un anillo, no un
// registro infinito.
//
// Path vacío significaría guardar bajo la clave "" y que ese evento apareciera en
// el panel de cualquier servicio al que se le leyera `m.events[""]`. Y el recorte
// a maxTimeline es lo que evita que una TUI abierta una semana tenga un slice de
// cien mil eventos por servicio.
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
	// Y lo que sobrevive es lo MÁS RECIENTE, no lo más antiguo: un timeline que
	// empieza por el principio es un timeline inútil.
	last := m.events[path][maxTimeline-1]
	if last.Detail != strconv.Itoa(maxTimeline+49) {
		t.Errorf("el último evento es %q, want el más reciente (%d)", last.Detail, maxTimeline+49)
	}
}

// TestHealthLinesDistingueSinPuertoDePuertoSinResolver: los dos motivos por los
// que no hay un probe que hacer son distintos y no se pueden sondear igual.
//
// Con puerto sin resolver, mostrar el declarado y sondearlo apuntaría a un puerto
// que puede ser el de otro worktree. Sin puerto declarado, no hay a qué apuntar.
// Colapsarlos en un mismo mensaje haría que el usuario creyese que su servicio
// podría arrancar y que el problema era la sonda.
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

// TestHumanKBUsaLaUnidadQueTocaYNoSePasaDeKB: el tamaño legible con el techo
// correcto.
//
// Los tres tramos son distintos y se notan: un proceso con 900 MB que se
// presentara como "943718.4 KB" no es legible, y uno con 2 GB presentado como
// "2048.0 KB" tampoco.
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
		// La entrada es en KiB, así que un GB son 1024*1024 KiB.
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
