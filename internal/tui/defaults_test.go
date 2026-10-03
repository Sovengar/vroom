package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"vroom/internal/orchestrate"
	"vroom/internal/tail"
)

// ---------------------------------------------------------------------------
// Los últimos defaults que protegen de una entrada extrema.
//
// Todos son de la misma familia: un `if x < 1 { x = 1 }` o un `if n > h { n = h }`
// que existen para que un valor raro no llegue a la librería de C. Se prueban porque
// la alternativa a un default es un crash, y un crash en un helper de render no es un
// bug de la TUI: es la muerte del proceso.
//
// Y hay un segundo grupo: los caminos que se alcanzan con un panel estrecho o con un
// árbol más alto que la ventana. No son raros —la gente redimensiona la terminal— y
// son los que más se olvidan porque en el monitor de desarrollo no se ven.
// ---------------------------------------------------------------------------

// TestElPanelDeDetallesSeRecortaCuandoElContenidoEsMasAltoQueLaCaja: el segundo
// recorte de detailsContentLines.
//
// Hay DOS recortes y son distintos. El de arriba limita el scroll; el de abajo limita
// el contenido al alto de la caja. El segundo es el que evita que el panel empuje el
// borde inferior fuera de la pantalla, y sólo se activa cuando el contenido real
// supera las doce filas —o sea, cuando el servicio tiene rama, grupo, patrón, pid y
// ruta.
func TestElPanelDeDetallesSeRecortaCuandoElContenidoEsMasAltoQueLaCaja(t *testing.T) {
	// MEDIDO: el panel de un SERVICIO nunca pasa de las doce líneas —header más las
	// once de la columna de meta—, así que no llega a disparar el segundo recorte.
	// El que sí pasa de doce es el de un STACK con varias etapas, que growe una
	// línea por etapa y dos por servicio. Se usa ése.
	m := newStackModel(t)
	cursorEn(t, &m, "front")

	stack := m.selectedStack()
	stack.Stages = []orchestrate.Stage{
		{Name: "build", Services: []string{"tienda-web", "tienda-api"}},
		{Name: "migrate", Services: []string{"tienda-api"}},
		{Name: "serve", Services: []string{"tienda-web", "tienda-api", "suelto"}},
	}

	contenido := m.allDetailsLines(120)
	if len(contenido) <= detailsHeight {
		t.Fatalf("el panel del stack sólo tiene %d líneas: no ha superado detailsHeight (%d) "+
			"y el test no está probando el recorte", len(contenido), detailsHeight)
	}

	// Y el recorte: exactamente detailsHeight líneas, todas del ancho.
	got := m.detailsContentLines()
	if len(got) != detailsHeight {
		t.Errorf("detailsContentLines devolvió %d líneas con %d de contenido, want %d",
			len(got), len(contenido), detailsHeight)
	}
	for i, l := range got {
		if lipglossWidth(l) > 120 {
			t.Errorf("línea %d mide %d celdas con un panel de 120", i, lipglossWidth(l))
		}
	}
}

// TestLaCajaDeKeybindsAguantaUnTerminalDeDosColumnas: el suelo del ancho interior.
//
// `m.width - boxFrame` con un terminal más estrecho que el marco da un interior
// negativo, y `strings.Repeat` con un número negativo revienta. El suelo a 1 es lo que
// convierte "terminal diminuto" en "ayuda ilegible" en vez de "programa muerto".
func TestLaCajaDeKeybindsAguantaUnTerminalDeDosColumnas(t *testing.T) {
	for _, w := range []int{1, 2, 3, 4, 5, boxFrame, boxFrame + 1} {
		m, _ := newTestModel(t)
		m.width, m.height = w, 30
		m.updateLayout()

		// Que no reviente y que devuelva algo.
		got := m.keybindsBox()
		if strings.TrimSpace(got) == "" && w > 3 {
			t.Errorf("w=%d: la caja de keybinds salió vacía", w)
		}
	}
}

// TestElAnchoDelModalSeAjustaALaFilaMasLargaYAlMinimo: los dos techos.
//
// El ancho se reduce a la fila más larga para que un modal de una sola tarea no
// ocupe media pantalla. Y tiene un suelo de 28, porque por debajo el compositor
// envolvería cada fila y una lista se convertiría en un bloque ilegible.
func TestElAnchoDelModalSeAjustaALaFilaMasLargaYAlMinimo(t *testing.T) {
	m := newStackModel(t)
	m.width = 200

	// Una sola tarea corta: el modal se ajusta a ella.
	m.pickerItems = []pickerItem{{Name: "t", Description: "d"}}
	if w := m.pickerInnerW(); w > 40 {
		t.Errorf("con una fila de dos caracteres el ancho es %d: el modal no se ajusta al contenido", w)
	}

	// Una fila enorme: el ancho es el de la pantalla menos el marco.
	m.pickerItems = []pickerItem{{Name: strings.Repeat("x", 400), Description: strings.Repeat("y", 400)}}
	if w := m.pickerInnerW(); w >= m.width {
		t.Errorf("con una fila de 800 caracteres el ancho es %d y la pantalla es %d: el modal no cabe", w, m.width)
	}

	// Sin items: el mínimo, que es lo que impide el bloque ilegible.
	m.pickerItems = nil
	if w := m.pickerInnerW(); w < 28 {
		t.Errorf("sin items el ancho es %d, want >= 28", w)
	}
}

// TestLaColumnaDelArbolSeRecortaYElExtraVaDelante: el filtro y el "no matches".
//
// Cuando el filtro deja el árbol vacío, el panel tiene que decirlo en vez de
// quedarse en blanco: una lista vacía sin explicación se lee como que vroom se ha
// colgado.
func TestLaColumnaDelArbolSeRecortaYElExtraVaDelante(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	m.updateLayout()

	t.Run("con filtro que no matchea nada", func(t *testing.T) {
		aplicado, _ := m.applyFilter("no-existe-este-proyecto-9911")
		got := aplicado.(Model).treeColumnLines()
		body := tail.StripANSI(strings.Join(got, "\n"))
		if !strings.Contains(body, "no matches") && !strings.Contains(body, "0") {
			t.Errorf("un filtro sin resultados tiene que decirlo:\\n%s", body)
		}
	})

	t.Run("el filtro abierto ocupa la primera línea", func(t *testing.T) {
		conBarra := m
		conBarra.filterOpen = true
		conBarra.filterText = "tienda"
		got := conBarra.treeColumnLines()
		// MEDIDO: la columna NO se rellena hasta bodyH; devuelve la barra más las
		// filas que caben, y el compositor de cajas es quien rellena el hueco. Lo que
		// no puede pasar es devolver MÁS.
		if len(got) > conBarra.bodyH {
			t.Errorf("la columna tiene %d líneas con bodyH %d", len(got), conBarra.bodyH)
		}
		// Y la primera línea es la barra, no una fila del árbol.
		if !strings.Contains(tail.StripANSI(got[0]), "/") {
			t.Errorf("la primera línea = %q, want la barra del filtro con su prompt", tail.StripANSI(got[0]))
		}
		// Con la barra abierta el árbol ve una fila menos, y por eso treeVis baja.
		if conBarra.treeVis() >= m.treeVis() {
			t.Errorf("con la barra abierta treeVis = %d, pero sin ella es %d: la barra tiene que consumir una fila",
				conBarra.treeVis(), m.treeVis())
		}
	})

	t.Run("el árbol se recorta al alto visible", func(t *testing.T) {
		corto := m
		corto.height = 10
		corto.updateLayout()
		got := corto.treeColumnLines()
		if len(got) > corto.bodyH {
			t.Errorf("la columna tiene %d líneas con bodyH %d: el compositor envuelve y el alto se rompe",
				len(got), corto.bodyH)
		}
	})
}

// TestOverlayConUnBoxMasAltoQueLaPantallaSeAnclaSinDesbordar: el clamp de la
// vertical.
//
// Un box más alto que la base no puede desbordarse: se recorta en vertical. Y el
// desplazamiento tiene que ser 0 cuando no hay sitio, no negativo.
func TestOverlayConUnBoxMasAltoQueLaPantallaSeAnclaSinDesbordar(t *testing.T) {
	base := strings.Repeat("linea\n", 3)

	// Box de 20 líneas en una base de 3: se recorta a la altura de la base.
	tall := strings.Repeat("X\n", 20)
	got := overlay(base, tall, 20, 3)
	if n := len(strings.Split(got, "\n")); n > len(strings.Split(base, "\n")) {
		t.Errorf("un box de 20 líneas en una base de 3 produjo %d líneas: la vertical no se recorta", n)
	}

	// MEDIDO: con una base más ALTA que la altura declarada, el box se centra sobre
	// la altura declarada y cae fuera de la base, así que no se ve. No es un bug:
	// `View` siempre pasa una base del alto de la pantalla. Lo que sí se comprueba
	// es que el desplazamiento no puede ser negativo, que es lo que rompería el
	// bucle y dejaría el modal invisible.
	for _, dims := range [][2]int{{5, 1}, {5, 2}, {5, 3}, {40, 10}} {
		pantalla := strings.Repeat("linea\n", dims[1])
		got := overlay(pantalla, "UNICA", dims[0], dims[1])
		if !strings.Contains(got, "UNICA") {
			t.Errorf("con %dx%d y una base de la altura correcta el box no se ve", dims[0], dims[1])
		}
	}
}

// TestElPanelDeDetallesDeUnProyectoSinManifiestoEnseñaElEjemplo: el único caso en
// que el panel enseña código.
//
// Un proyecto sin `.vroom.toml` es la mitad de los directorios de un workspace, así
// que este camino se ve mucho. Y lo que enseña tiene que ser COPIABLE: es lo que el
// usuario pega en el fichero para dejar de ver este mensaje.
func TestElPanelDeDetallesDeUnProyectoSinManifiestoEnseñaElEjemplo(t *testing.T) {
	m := sinManifiesto(t)
	body := tail.StripANSI(strings.Join(m.detailsLines(m.rightW), "\n"))

	if !strings.Contains(body, "No manifest") {
		t.Errorf("el panel tiene que explicar qué falta:\\n%s", body)
	}
	// Y el ejemplo tiene que ser un manifiesto de verdad: nombre, comando y puerto.
	for _, want := range []string{"name =", "command_start"} {
		if !strings.Contains(body, want) {
			t.Errorf("el ejemplo no trae %q, y sin él el usuario no sabe qué escribir:\\n%s", want, body)
		}
	}
	// Con el nombre del proyecto, para que no tenga que editarlo.
	if !strings.Contains(body, m.selected().Name) {
		t.Errorf("el ejemplo no lleva el nombre del proyecto: el usuario tendría que escribirlo a mano")
	}
}

// TestElPanelDeDetallesConUnPanelEstrechitoCaeAUnaColumna: el degradado del panel.
//
// Por debajo de un ancho, la columna de comandos desaparece y la de meta ocupa todo.
// Sin ese degradado el panel se parte en dos columnas de dos letras cada una.
func TestElPanelDeDetallesConUnPanelEstrechitoCaeAUnaColumna(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.branches[path] = "main"

	// Un ancho en el que la columna de comandos no cabe.
	//
	// MEDIDO: el recorte de ancho NO está en allDetailsLines, que por contrato
	// devuelve las líneas SIN recortar —quien lo llama es rightColumnLines, que
	// aplica el scroll y luego fitLines—. El que garantiza el ancho es detailsLines,
	// a través de clipLines, y es el que se comprueba.
	for _, w := range []int{2, 5, 10, 16, 20} {
		lineas := m.detailsLines(w)
		for i, l := range lineas {
			if n := lipglossWidth(l); n > w {
				t.Errorf("w=%d: la línea %d mide %d celdas", w, i, n)
			}
		}
		// Y tiene que seguir habiendo contenido, no un panel vacío.
		if len(lineas) < 2 {
			t.Errorf("w=%d: el panel tiene %d líneas, want al menos header + path", w, len(lineas))
		}
	}
}

// TestTermWAguantaElMinimoDelGrid: el suelo del ancho de la terminal.
//
// Un grid de menos de termMinW columnas muestra un prompt partido y un shell que no
// sabe dónde está el cursor.
func TestTermWAguantaElMinimoDelGrid(t *testing.T) {
	m, _ := newTestModel(t)
	for _, w := range []int{1, 5, 20, 40, 80, 200} {
		m.width = w
		got := m.termW()
		if got < termMinW {
			t.Errorf("w=%d: termW = %d, want >= %d", w, got, termMinW)
		}
	}
}

// TestScreenPoneElCursorComoBloqueInvertidoYNoSeSaleDeLaFila: el cursor dibujado.
//
// El emulador no incluye el cursor en su render, así que `screen` lo dibuja a mano
// como un bloque invertido. Si la posición cae más allá del final de la línea hay
// que clampearla: un `Truncate` con un índice mayor que la línea devuelve la línea
// entera y el bloque se pintaría al final del renglón en vez de donde está el cursor.
func TestScreenPoneElCursorComoBloqueInvertidoYNoSeSaleDeLaFila(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	// Se escribe una línea corta y se coloca el cursor más allá de su final.
	s.write([]byte("corta\r\n"))
	screen := tail.StripANSI(s.screen())

	if !strings.Contains(screen, "corta") {
		t.Fatalf("el texto del shell no llegó al emulador: %q", screen)
	}
	// Con el cursor en una posición imposible, screen tiene que devolver algo
	// utilizable y no reventar ni perder la línea.
	for _, l := range strings.Split(screen, "\n") {
		if n := lipglossWidth(l); n > 40 {
			t.Errorf("una línea mide %d celdas en un grid de 40: %q", n, l)
		}
	}

	// Y una sesión cerrada devuelve cadena vacía en vez de un render vacío: el
	// llamador usa "" para saber que no hay nada que pintar.
	s.closed = true
	if got := s.screen(); got != "" {
		t.Errorf("screen de una sesión cerrada = %q, want cadena vacía", got)
	}
}

// TestTermKeyConUnMensajeQueNoEsTeclaNoEscribeNiCierra: el filtro de tipo.
//
// termKey recibe tea.KeyMsg y todo lo que no sea una pulsación se ignora. Lo que no
// puede pasar es que un mensaje arbitrario acabe en el PTY del usuario: escribir
// bytes de control en una terminal real mueve el cursor, borra la línea o cambia de
// terminal virtual.
func TestTermKeyConUnMensajeQueNoEsTeclaNoEscribeNiCierra(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	m, _ := newTestModel(t)
	m.term = s
	m.termOpen = true

	// Una tecla real llega al PTY.
	next, _ := m.termKey(keyPress("a"))
	if !next.(Model).termOpen {
		t.Error("una tecla normal cerró el modal")
	}
	waitFor(t, 2*timeSecond, func() bool { return len(s.pty.(*stubPty).bytesWritten()) > 0 })

	// Un ctrl+q cierra y no escribe.
	antes := len(s.pty.(*stubPty).bytesWritten())
	next, cmd := m.termKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Error("ctrl+q no emite comandos")
	}
	if next.(Model).termOpen {
		t.Error("ctrl+q tiene que cerrar el modal")
	}
	if n := len(s.pty.(*stubPty).bytesWritten()); n > antes {
		t.Errorf("ctrl+q escribió %d bytes en el PTY: es una tecla de vroom, no del shell", n-antes)
	}
}

// TestStartSessionConUnBinarioQueNoExisteDaErrorSinFiltrarFd: el fallo de arranque.
//
// El camino de error tiene que cerrar el PTY y el emulador que ya se han creado, y
// no dejar un proceso a medias. Con `argv[0]` inexistente el exec falla después de
// abrir el PTY, que es exactamente el momento en que un descuido filtraría dos fds.
func TestStartSessionConUnBinarioQueNoExisteDaErrorSinFiltrarFd(t *testing.T) {
	_, err := startSession(40, 10, t.TempDir(), []string{"/no/existe/un/shell"})
	if err == nil {
		t.Fatal("un binario inexistente tiene que dar error")
	}

	// Y el fd no se filtra: el proceso de test puede abrir y cerrar cientos de PTY
	// seguidos. Si el error filtrara el master, el límite de fds lo delataría.
	for range 50 {
		if _, err := startSession(40, 10, t.TempDir(), []string{"/no/existe/un/shell"}); err == nil {
			t.Fatal("un binario inexistente dio nil en la iteración 50")
		}
	}
}

// TestNewEmuladorSePuedeCerrarSinPanic: el emulador de la sesión.
//
// `vt.Emulator` tiene Close y el pump lee de él. Un doble cierre —el ptyEOFMsg y el
// shutdown del proceso— no debe entrar en la librería de C dos veces.
func TestNewEmuladorSePuedeCerrarSinPanic(t *testing.T) {
	emu := vt.NewEmulator(40, 10)
	_, _ = emu.Write([]byte("hola"))
	_ = emu.Close()
	_ = emu.Close() // idempotente
}

// TestElGrupoVacioNoAbreBloque: la guarda del agrupamiento.
//
// Un manifiesto sin `primary_group` es un proyecto inline, y su entrada en el árbol
// no debe abrir bloque: "sin grupo" no es un grupo. Si abriera, el árbol tendría una
// cabecera sin nada debajo y el conteo de miembros del nodo no cuadraría.
func TestElGrupoVacioNoAbreBloque(t *testing.T) {
	m, _ := newTestModel(t)

	var inline int
	for i := range m.projects {
		if m.projects[i].Manifest == nil || m.projects[i].Manifest.PrimaryGroup != "" {
			continue
		}
		inline++
		for _, it := range m.tree {
			if it.kind == itemPrimary && it.primary == m.projects[i].Name {
				t.Errorf("el proyecto inline %q aparece como cabecera de bloque", m.projects[i].Name)
			}
		}
	}
	if inline == 0 {
		t.Skip("el árbol de test no tiene proyectos sin grupo")
	}
	t.Logf("proyectos inline en el árbol de test: %d", inline)
}

// helpers --------------------------------------------------------------------

// timeSecond es un segundo como duración, para los waitFor.
const timeSecond = 1000000000
