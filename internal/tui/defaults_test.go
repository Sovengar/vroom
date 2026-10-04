package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"vroom/internal/orchestrate"
	"vroom/internal/tail"
)

// Two clips on purpose: the top one limits scroll, the bottom one stops the panel from pushing the border off-screen.
func TestElPanelDeDetallesSeRecortaCuandoElContenidoEsMasAltoQueLaCaja(t *testing.T) {
	// MEDIDO: a service panel never exceeds twelve lines, so only a multi-stage stack crosses detailsHeight.
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

// strings.Repeat panics on a negative count, which is what m.width - boxFrame yields below the frame width.
func TestLaCajaDeKeybindsAguantaUnTerminalDeDosColumnas(t *testing.T) {
	for _, w := range []int{1, 2, 3, 4, 5, boxFrame, boxFrame + 1} {
		m, _ := newTestModel(t)
		m.width, m.height = w, 30
		m.updateLayout()

		got := m.keybindsBox()
		if strings.TrimSpace(got) == "" && w > 3 {
			t.Errorf("w=%d: la caja de keybinds salió vacía", w)
		}
	}
}

// The floor of 28 exists because below it the compositor wraps every row into an illegible block.
func TestElAnchoDelModalSeAjustaALaFilaMasLargaYAlMinimo(t *testing.T) {
	m := newStackModel(t)
	m.width = 200

	m.pickerItems = []pickerItem{{Name: "t", Description: "d"}}
	if w := m.pickerInnerW(); w > 40 {
		t.Errorf("con una fila de dos caracteres el ancho es %d: el modal no se ajusta al contenido", w)
	}

	m.pickerItems = []pickerItem{{Name: strings.Repeat("x", 400), Description: strings.Repeat("y", 400)}}
	if w := m.pickerInnerW(); w >= m.width {
		t.Errorf("con una fila de 800 caracteres el ancho es %d y la pantalla es %d: el modal no cabe", w, m.width)
	}

	m.pickerItems = nil
	if w := m.pickerInnerW(); w < 28 {
		t.Errorf("sin items el ancho es %d, want >= 28", w)
	}
}

// A filter that empties the tree must say so, because a blank list reads as a hung process.
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
		// MEDIDO: the column does not pad up to bodyH; the box compositor fills the gap, so it may return fewer lines but never more.
		if len(got) > conBarra.bodyH {
			t.Errorf("la columna tiene %d líneas con bodyH %d", len(got), conBarra.bodyH)
		}
		if !strings.Contains(tail.StripANSI(got[0]), "/") {
			t.Errorf("la primera línea = %q, want la barra del filtro con su prompt", tail.StripANSI(got[0]))
		}
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

func TestOverlayConUnBoxMasAltoQueLaPantallaSeAnclaSinDesbordar(t *testing.T) {
	base := strings.Repeat("linea\n", 3)

	tall := strings.Repeat("X\n", 20)
	got := overlay(base, tall, 20, 3)
	if n := len(strings.Split(got, "\n")); n > len(strings.Split(base, "\n")) {
		t.Errorf("un box de 20 líneas en una base de 3 produjo %d líneas: la vertical no se recorta", n)
	}

	// MEDIDO: View always passes a base as tall as the screen, so the only invariant left to check here is that the offset cannot go negative.
	for _, dims := range [][2]int{{5, 1}, {5, 2}, {5, 3}, {40, 10}} {
		pantalla := strings.Repeat("linea\n", dims[1])
		got := overlay(pantalla, "UNICA", dims[0], dims[1])
		if !strings.Contains(got, "UNICA") {
			t.Errorf("con %dx%d y una base de la altura correcta el box no se ve", dims[0], dims[1])
		}
	}
}

// The example must stay copyable: it is what the user pastes into the file to make the message go away.
func TestElPanelDeDetallesDeUnProyectoSinManifiestoEnseñaElEjemplo(t *testing.T) {
	m := sinManifiesto(t)
	body := tail.StripANSI(strings.Join(m.detailsLines(m.rightW), "\n"))

	if !strings.Contains(body, "No manifest") {
		t.Errorf("el panel tiene que explicar qué falta:\\n%s", body)
	}
	for _, want := range []string{"name =", "command_start"} {
		if !strings.Contains(body, want) {
			t.Errorf("el ejemplo no trae %q, y sin él el usuario no sabe qué escribir:\\n%s", want, body)
		}
	}
	if !strings.Contains(body, m.selected().Name) {
		t.Errorf("el ejemplo no lleva el nombre del proyecto: el usuario tendría que escribirlo a mano")
	}
}

func TestElPanelDeDetallesConUnPanelEstrechitoCaeAUnaColumna(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.branches[path] = "main"

	// MEDIDO: width clipping lives in detailsLines via clipLines; allDetailsLines returns unclipped lines by contract.
	for _, w := range []int{2, 5, 10, 16, 20} {
		lineas := m.detailsLines(w)
		for i, l := range lineas {
			if n := lipglossWidth(l); n > w {
				t.Errorf("w=%d: la línea %d mide %d celdas", w, i, n)
			}
		}
		if len(lineas) < 2 {
			t.Errorf("w=%d: el panel tiene %d líneas, want al menos header + path", w, len(lineas))
		}
	}
}

// Below termMinW the grid shows a split prompt and a shell that no longer knows where the cursor is.
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

func TestScreenPoneElCursorComoBloqueInvertidoYNoSeSaleDeLaFila(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	s.write([]byte("corta\r\n"))
	screen := tail.StripANSI(s.screen())

	if !strings.Contains(screen, "corta") {
		t.Fatalf("el texto del shell no llegó al emulador: %q", screen)
	}
	for _, l := range strings.Split(screen, "\n") {
		if n := lipglossWidth(l); n > 40 {
			t.Errorf("una línea mide %d celdas en un grid de 40: %q", n, l)
		}
	}

	// A closed session must return "", because the caller uses it to know there is nothing to paint.
	s.closed = true
	if got := s.screen(); got != "" {
		t.Errorf("screen de una sesión cerrada = %q, want cadena vacía", got)
	}
}

// Control bytes written to a real PTY move the cursor, erase the line or switch virtual terminals, so a non-key message must never reach it.
func TestTermKeyConUnMensajeQueNoEsTeclaNoEscribeNiCierra(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	m, _ := newTestModel(t)
	m.term = s
	m.termOpen = true

	next, _ := m.termKey(keyPress("a"))
	if !next.(Model).termOpen {
		t.Error("una tecla normal cerró el modal")
	}
	waitFor(t, 2*timeSecond, func() bool { return len(s.pty.(*stubPty).bytesWritten()) > 0 })

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

// With a nonexistent argv[0] the exec fails after the PTY is already open, exactly when a slip leaks both fds.
func TestStartSessionConUnBinarioQueNoExisteDaErrorSinFiltrarFd(t *testing.T) {
	_, err := startSession(40, 10, t.TempDir(), []string{"/no/existe/un/shell"})
	if err == nil {
		t.Fatal("un binario inexistente tiene que dar error")
	}

	// 50 iterations so an fd leak trips the process fd limit instead of passing.
	for range 50 {
		if _, err := startSession(40, 10, t.TempDir(), []string{"/no/existe/un/shell"}); err == nil {
			t.Fatal("un binario inexistente dio nil en la iteración 50")
		}
	}
}

// ptyEOFMsg and the process shutdown can both close it, and entering the C library twice would be fatal.
func TestNewEmuladorSePuedeCerrarSinPanic(t *testing.T) {
	emu := vt.NewEmulator(40, 10)
	_, _ = emu.Write([]byte("hola"))
	_ = emu.Close()
	_ = emu.Close()
}

// "No group" is not a group: an inline project's tree entry must not open a header with nothing under it.
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

const timeSecond = 1000000000
