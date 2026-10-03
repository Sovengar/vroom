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

// ---------------------------------------------------------------------------
// Los tres paneles que sólo se ven en un estado de selección concreto: la tabla
// de hilos, el panel de detalles de un stack, y la retirada de rutas.
//
// Los tres estaban invisibles a la cobertura por la misma razón: exigen poner el
// cursor en un sitio que la suite nunca pone. El cursor sobre un nodo de grupo,
// sobre un stack, y el caso "seleccionado pero sin muestras todavía" son estados
// que el usuario alcanza en un segundo y que los tests nunca tocaban.
//
// Y el panel de detalles de un stack importa más de lo que parece: es lo que
// explica por qué `vroom stop` sobre un stack paró lo que paró. Un stack que no
// resuelve un nombre tiene que DECIRLO en el panel, no fallar el render entero.
// ---------------------------------------------------------------------------

// cursorEn busca la fila del árbol cuyo label contenga want y pone ahí el cursor.
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

// ---------------------------------------------------------------------------
// Hilos
// ---------------------------------------------------------------------------

// TestThreadsLinesSobreUnHeaderInvitaAPick: sin proyecto no hay hilos que enseñar,
// y el texto tiene que decir qué hacer.
//
// Los tres motivos por los que puede no haber filas son distintos —nodo de grupo,
// sin manifiesto, servicio parado— y confundirlos haría que el usuario creyera
// que el sampling está roto cuando lo que falta es un manifiesto.
func TestThreadsLinesSobreUnHeaderInvitaAPick(t *testing.T) {
	m, _ := newTestModel(t)
	cursorEn(t, &m, "tienda")

	lines := m.threadsLines(m.rightW, m.contentH)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want un único mensaje", lines)
	}
	// Sobre un header el texto tiene que pedir una acción, no decir "nada que ver".
	if !strings.Contains(lines[0], "pick a service") {
		t.Errorf("sobre un nodo de grupo = %q, want una invitación a elegir servicio", lines[0])
	}
}

// TestThreadsLinesSinProyectoYSinHeaderNoDevuelveNada: un cursor que no está
// sobre nada (árbol vacío o índice fuera de rango) no inventa filas.
//
// nil y no una línea vacía: el panel tiene que poder distinguir "nada que
// mostrar" de "un mensaje que existe". Un `[]string{""}` dibujaría una línea en
// blanco en medio del panel.
func TestThreadsLinesSinProyectoYSinHeaderNoDevuelveNada(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = -1 // fuera de rango: selectedItem falla y no hay header

	if lines := m.threadsLines(m.rightW, m.contentH); lines != nil {
		t.Errorf("lines = %v con el cursor fuera del árbol, want nil: un []string{\"\"} dibujaría una línea en blanco", lines)
	}
}

// TestThreadsLinesDistingueSinManifiestoDeParado: los dos mensajes tienen que ser
// distintos porque las dos acciones son distintas.
//
// Sin manifiesto la acción es escribir un fichero; parado la acción es arrancarlo.
// Decir "service not running" sobre un proyecto sin manifiesto empuja al usuario a
// pulsar start sobre algo que no se puede arrancar.
//
// Se cambia la bandera en el árbol YA CONSTRUIDO en vez de reconstruirlo: lo que
// se prueba es la rama del render, y un `buildTree` con un proyecto inyectado a
// mano podría no incluirlo (el escaneo tiene sus reglas de agrupación) y el test
// pasaría por no haber probado nada.
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

// TestThreadsLinesDiceQueEstaTomandoLaPrimeraMuestra: running pero sin filas
// significa "sampling en curso", no "el proceso no tiene hilos".
//
// Es la diferencia entre un panel que parpadea un segundo y un panel que dice que
// un proceso vivo tiene cero hilos, que es imposible.
func TestThreadsLinesDiceQueEstaTomandoLaPrimeraMuestra(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, 4242)

	// threads[path] es nil: hay un sampling en vuelo o el proceso acaba de morir.
	lines := m.threadsLines(m.rightW, m.contentH)
	if len(lines) != 1 || !strings.Contains(lines[0], "sampling") {
		t.Errorf("lines = %v, want un aviso de sampling en curso", lines)
	}
}

// TestThreadsLinesCuentaLosHilosQueNoCaben: la tabla se recorta al alto del panel
// y dice cuántos se quedaron fuera.
//
// Sin el "+N more" el usuario ve una tabla que acaba en un hilo cualquiera y no
// tiene forma de saber si su hilo interesante estaba ahí. Y el recorte tiene que
// respetar el presupuesto de filas: dibujar más de las que caben empuja el pie del
// panel fuera de la caja.
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

	// Un alto pequeño: header + margen + pocas filas.
	lines := m.threadsLines(80, 6)
	body := strings.Join(lines, "\n")

	if !strings.Contains(body, "NAME") {
		t.Errorf("la tabla tiene que traer su header: %q", body)
	}
	if !strings.Contains(body, "more threads") {
		t.Errorf("con 40 hilos y 6 filas hay que decir cuántos sobran: %q", body)
	}
	// Y el recorte no se pasa de las filas disponibles.
	if len(lines) > 6 {
		t.Errorf("la tabla devolvió %d líneas para un alto de 6", len(lines))
	}
	// Con hueco de sobra no dice "more": sería ruido.
	if alto := m.threadsLines(200, 60); strings.Contains(strings.Join(alto, "\n"), "more threads") {
		t.Error("con toda la tabla dentro, decir \"more threads\" es ruido")
	}
}

// TestThreadsLinesAguantaUnPanelEstrechisimo: el nombre de la columna tiene un
// mínimo aunque el panel no lo permita.
//
// Sin ese mínimo el header se convierte en un muro de %%s y de hecho las filas se
// imprimen con un %-0s que deja el nombre pegado al TID. Es el peor caso de ancho,
// y ocurre de verdad: la columna de detalles se pliega en pantallas estrechas.
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
		// MEDIDO: con un alto de 1 salen DOS líneas, no una: el header y el
		// "+N more". El presupuesto de filas se recorta a 1 como mínimo, así que
		// en un panel de una sola fila el usuario ve el rótulo y el recuento en
		// lugar de un panel vacío. Desbordar una fila en un panel de dos es peor
		// que mostrar el rótulo: no hay nada que recortar.
		if got := len(m.threadsLines(w, 1)); got != 2 {
			t.Errorf("w=%d, h=1: %d líneas, want 2 (header + \"more\"): con una fila no cabe ni el rótulo solo", w, got)
		}
		// Y con un alto normal sí se recorta.
		if got := len(m.threadsLines(w, 5)); got > 5 {
			t.Errorf("w=%d, h=5: %d líneas, want <= 5", w, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Detalles de un stack
// ---------------------------------------------------------------------------

// TestStackDetailsLinesExplicaElStack: el panel de un stack dice qué es, cuántas
// etapas tiene, cuántos servicios y en cuál están.
//
// Es la respuesta a "¿por qué stop paró eso?". Con sólo el nombre del stack el
// usuario tiene que adivinar el alcance de lo que se pulsó, y adivinar mal aquí
// significa parar un servicio que no quería.
func TestStackDetailsLinesExplicaElStack(t *testing.T) {
	m := newStackModel(t)
	cursorEn(t, &m, "front")

	stack := m.selectedStack()
	if stack == nil {
		t.Fatalf("el cursor no cayó en un stack: %d", m.selectedItemKind())
	}
	body := tail.StripANSI(strings.Join(m.stackDetailsLines(stack, m.rightW), "\n"))

	for _, want := range []string{
		"front",               // el nombre
		"orchestration stack", // el tipo
		"stages:",             // cuántas etapas
		"services:",           // cuántos servicios
		"group:",              // de qué grupo
		"tienda-web",          // los servicios, uno a uno
		"tienda-api",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("el panel del stack no menciona %q:\n%s", want, body)
		}
	}
	// Y el conteo es de los servicios DEL stack, no de todo el workspace.
	if !strings.Contains(body, "services: 2 (0 running)") {
		t.Errorf("conteo de servicios = %q, want los dos del stack y cero corriendo:\n%s", body, body)
	}
}

// TestStackDetailsLinesDistingueCorriendoYParado: el punto del header dice si hay
// algo vivo, y sólo uno de los dos textos puede aparecer.
//
// La versión "corriendo" se alcanza marcando un servicio vivo; el resto tiene que
// seguir diciendo parado. El estado del header decide si el usuario pulsa stop o
// start, así que mostrar los dos sería peor que mostrar ninguno.
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
		// Al menos UN servicio vivo del stack basta: el veredicto es agregado.
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

// TestStackDetailsLinesAvisaDeUnNombreAmbiguoSinRomperElPanel: un servicio del
// stack que no resuelve tiene que aparecer con un aviso y el resto seguir
// dibujándose.
//
// El criterio es el mismo del engine y de la CLI: ante un nombre ambiguo no se
// elige el primero. Romper el panel entero dejaría al usuario sin ver ni el
// stack ni el nombre que no se soluciona, que es justo lo que necesita ver.
func TestStackDetailsLinesAvisaDeUnNombreAmbiguoSinRomperElPanel(t *testing.T) {
	m := newStackModel(t)

	// Un segundo proyecto llamado igual: el nombre del stack pasa a ser ambiguo.
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
	// Y el panel entero se conserva: el header y el conteo siguen ahí.
	if !strings.Contains(body, "ambiguo") || !strings.Contains(body, "stages:") {
		t.Errorf("un aviso no puede vaciar el panel:\n%s", body)
	}
}

// TestStackDetailsLinesMuestraElPuertoDeCadaServicio: el puerto va junto al
// nombre del servicio.
//
// Es lo que permite comparar el panel con lo que hay en el árbol sin mentalizar
// la tabla de puertos: el servicio y su puerto en la misma línea.
func TestStackDetailsLinesMuestraElPuertoDeCadaServicio(t *testing.T) {
	m := newStackModel(t)
	// El manifiesto de tienda-api declara 8081.
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
	// Y el que no se ha resuelto no inventa uno.
	if strings.Contains(body, "999999") {
		t.Errorf("un puerto que no existe no puede aparecer:\n%s", body)
	}
}

// TestStackDetailsLinesSeEligePorSobreUnStackNoPorNombre: el panel de un stack se
// muestra porque el cursor está SOBRE un stack, no porque exista uno.
//
// Si `allDetailsLines` eligiera el stack por nombre o por ser el único, entonces
// con el cursor sobre un proyecto normal se vería el panel equivocado: el usuario
// leería "front: 2 servicios" con otro servicio seleccionado.
func TestStackDetailsLinesSeEligePorSobreUnStackNoPorNombre(t *testing.T) {
	m := newStackModel(t)

	// Cursor sobre un proyecto normal: NO es el panel del stack.
	m = moveCursorTo(t, m, "tienda-api")
	body := tail.StripANSI(strings.Join(m.detailsLines(m.rightW), "\n"))
	if strings.Contains(body, "[stack]") {
		t.Errorf("con un proyecto seleccionado el panel no puede ser el del stack:\n%s", body)
	}
	if !strings.Contains(body, "path:") {
		t.Errorf("con un proyecto seleccionado tiene que ser su panel de detalle:\n%s", body)
	}

	// Cursor sobre el stack: sí es el del stack.
	cursorEn(t, &m, "front")
	body = tail.StripANSI(strings.Join(m.detailsLines(m.rightW), "\n"))
	if !strings.Contains(body, "[stack]") {
		t.Errorf("con el cursor sobre el stack tiene que ser su panel:\n%s", body)
	}
}

// TestDetailsLinesSinNadaSeleccionadoLoDice: sin proyecto, sin stack y sin nodo,
// el panel dice que no hay nada seleccionado.
//
// Es el caso de un árbol recién escaneado con el cursor todavía sin colocar. Una
// línea vacía dejaría el panel en blanco y el usuario no sabría si es un fallo o
// si sencillamente no hay selección.
func TestDetailsLinesSinNadaSeleccionadoLoDice(t *testing.T) {
	m, _ := newTestModel(t)
	m.tree = nil // sin filas: nada puede estar seleccionado

	body := tail.StripANSI(strings.Join(m.detailsLines(m.rightW), "\n"))
	if !strings.Contains(body, "No project selected") {
		t.Errorf("sin selección el panel tiene que decirlo:\n%s", body)
	}
}

// TestClipLinesRecortaFilasYColumnas: el recorte tiene que aplicar las DOS
// dimensiones y en ese orden.
//
// Filas primero: recortar columnas de una línea que luego no se va a dibujar es
// trabajo tirado. Y si se invirtiera el orden, una línea con ANSI podría contar
// los bytes del escape como ancho visible y partir un rune por la mitad, que es
// justo lo que truncANSI evita.
func TestClipLinesRecortaFilasYColumnas(t *testing.T) {
	lines := []string{"uno", "dos", "tres", "cuatro"}

	got := clipLines(lines, 2, 40)
	if len(got) != 2 || got[0] != "uno" || got[1] != "dos" {
		t.Errorf("con h=2 = %v, want las dos primeras", got)
	}
	// Y el recorte no muta el slice de la cabecera... bueno, sí lo hace a propósito
	// (recorta in situ); lo que importa es el resultado.
	if h, w := 10, 3; len(clipLines([]string{"abcdefghij"}, h, w)) != 1 {
		t.Error("una sola línea con h=10 tiene que quedarse en una")
	}
	wide := clipLines([]string{"abcdefghij"}, 10, 4)
	if len(tail.StripANSI(wide[0])) > 4 {
		t.Errorf("clipLines no recortó columnas: %q (%d)", wide[0], len(tail.StripANSI(wide[0])))
	}
}

// TestPadRellenaARunesYNoTocaLoQueNoCabe: las etiquetas se alinean con pad, y
// un nombre más largo que la columna se deja intacto.
//
// Recortar una etiqueta larga rompería el alineado de las que vienen detrás; lo
// que se hace es dejar que desborde. Y el relleno va por RUNES, no por bytes: con
// una etiqueta con acento, rellenar por bytes deja la columna un byte más corta
// en cada fila y las columnas bailan.
func TestPadRellenaARunesYNoTocaLoQueNoCabe(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"port:", 10, "port:     "},
		{"", 4, "    "},
		// MEDIDO: "exacta10" son 8 caracteres, no 10, así que se rellena igual. El
		// nombre del caso era engañoso.
		{"exacta10", 10, "exacta10  "},
		{"demasiado", 4, "demasiado"},
	}
	for _, tt := range tests {
		if got := pad(tt.in, tt.n); got != tt.want {
			t.Errorf("pad(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}

	// Con acento: tres etiquetas con el mismo número de runes se alinean igual.
	for _, s := range []string{"url:", "ruta:", "puerto:"} {
		if n := len(pad(s, 10)); n != 10 {
			t.Errorf("pad(%q, 10) mide %d bytes, want 10: el relleno tiene que contar runes", s, n)
		}
	}
	// Y con un rune multibyte: sigue llenando hasta N runes, no N bytes.
	got := pad("añadir", 10)
	if len([]rune(got)) != 10 {
		t.Errorf("pad con multibyte mide %d runes, want 10: %q", len([]rune(got)), got)
	}
}

// ---------------------------------------------------------------------------
// Retirada de rutas
// ---------------------------------------------------------------------------

// TestReleaseRouteNoRetiraLoQueNoEraSuyo: RouteOwned false significa que el
// handle ya lo tenía otro.
//
// Retirarla pisaría la ruta de otro servicio. Y el meta conserva el nombre: el
// handle sobrevive a la revocación para que la reconciliación tenga dónde mirar,
// así que usarlo como autoridad de borrado borraría rutas ajenas.
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

// TestReleaseRouteRevocaLaPropiedadCuandoLaRetiradaSurtioEfecto: con el stub de
// retirada puesto y propietario, la ruta se retira Y se revoca.
//
// Es el camino normal del stop, y las dos mitades tienen que pasar a la vez:
// retirar sin revocar deja la propiedad puesta sobre una ruta que ya no existe, y
// revocar sin retirar deja la ruta zombie que el siguiente servicio no puede tomar.
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

// TestReleaseRouteNoRevocaSiLaRetiradaNoSurtioEfecto: el releaser dice que no
// estaba, y la propiedad se queda.
//
// Es el fallo cerrado: si la ruta ya no existe —la quitó otro, o nunca se tomó—,
// revocar la propiedad sería mentir sobre el estado. Con la propiedad puesta, la
// reconciliación puede volver a mirar y saber que hay algo pendiente.
func TestReleaseRouteNoRevocaSiLaRetiradaNoSurtioEfecto(t *testing.T) {
	// Un releaser que dice "no estaba": es lo que devuelve Remove con error en
	// portless.Release.
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

// TestTuiRouteReleaserEligeElStubYSinoElInerte: la elección del seam tiene tres
// ramas y sólo dos son provocables desde un test.
//
// El dobleUno de stub vacío es el orden de las comprobaciones: con el flag puesto y
// el puntero a nil tiene que caer al inerte, no devolver un Releaser envuelto en una
// func nil —que reventaría con SIGSEGV en el primer Remove—.
func TestTuiRouteReleaserEligeElStubYSinoElInerte(t *testing.T) {
	t.Run("stub instalado y con puntero: es el stub", func(t *testing.T) {
		rec := &recordingReleaser{}
		installRouteStub(t, rec)

		got := tuiRouteReleaser()
		if got == nil {
			t.Fatal("con el stub instalado tiene que salir el stub, no nil: nil es el cliente REAL")
		}
		// Y es el del test, no otro.
		if err := got.RemoveAbsent("vroom-test-x"); err != nil {
			t.Errorf("el releaser devuelto no es el del test: %v", err)
		}
		if len(rec.removed) != 1 {
			t.Errorf("la llamada no fue a parar al stub del test: %v", rec.removed)
		}
	})

	t.Run("flag puesto pero puntero nil: cae al inerte y no revienta", func(t *testing.T) {
		// Esto es lo que pasa si un test activa el flag y se olvida de instalar el
		// stub. Devolver un Releaser envuelto en una func nil reventaría con SIGSEGV
		// en el primer Remove, en producción, sólo bajo un test mal escrito.
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
		// Y no revienta al usarlo: es la mitad que importa.
		if err := got.RemoveAbsent("vroom-cualquiera"); err != nil {
			t.Errorf("el inerte devolvió error %v: su trabajo es NO hacer nada", err)
		}
	})

	t.Run("sin flag en un test binary: el inerte", func(t *testing.T) {
		// Ningún test puede comprobar la rama del cliente real: esta función corre
		// DENTRO de un .test, así que IsTestBinary() siempre da true. Que devuelva
		// nil aquí significaría que un test se connectaría al proxy real.
		if !portless.IsTestBinary() {
			t.Skip("este binario no es un test binary: el caso no aplica")
		}
		if got := tuiRouteReleaser(); got == nil {
			t.Error("sin stub dentro de un test binary tiene que salir el inerte, no nil")
		}
	})
}
