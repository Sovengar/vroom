package bordered

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// Los tests de este archivo cubren las ramas de RenderWithTitlesEx que el
// wrap_test.go no toca: el clamp de ancho, los caracteres de borde vacios, el
// titulo mas ancho que la caja, los tres alineamientos, el borde con color, y
// el camino de wrap DENTRO del render (no solo de wrapLine).
//
// Son las ramas por las que la caja se deforma: si el titulo no se trunca, se
// sale de la caja; si un caracter de borde vacio no se sustituye por espacio,
// la linea del borde mide una celda menos que el resto y la caja se ve rota.
// ---------------------------------------------------------------------------

// TestRenderAnchoMinimoNoRompeLaCaja: por debajo de 2 celdas de ancho no hay
// caja posible, asi que se sube a 2 en vez de restar un ancho negativo. El
// assert importante es que NO revienta con panic ni produce anchuras negativa.
func TestRenderAnchoMinimoNoRompeLaCaja(t *testing.T) {
	for _, width := range []int{-5, -1, 0, 1} {
		out := RenderWithTitleEx(lipgloss.RoundedBorder(), nil, AlignLeft, "T", "x", width)
		for i, line := range strings.Split(out, "\n") {
			if w := ansi.StringWidth(line); w < 0 {
				t.Errorf("width=%d linea %d con ancho negativo %d: %q", width, i, w, line)
			}
		}
	}
}

// TestRenderConCaracteresDeBordeVaciosLosSustituyePorEspacio: un Border con
// caracteres vacios (lipgloss lo permite para bordes parciales) debe rellenarse
// con espacios. Si no, la linea mide una celda menos y las tres lineas de la
// caja dejan de alinearse.
func TestRenderConCaracteresDeBordeVaciosLosSustituyePorEspacio(t *testing.T) {
	// Top/Left/Right/Bottom vacios; las esquinas si presentes.
	border := lipgloss.Border{
		TopLeft:     "+",
		TopRight:    "+",
		BottomLeft:  "+",
		BottomRight: "+",
	}

	const width = 12
	out := RenderWithTitleEx(border, nil, AlignLeft, " hi ", "cuerpo", width)

	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != width {
			t.Errorf("linea %d ancho = %d, want %d: %q", i, w, width, line)
		}
	}
	// El borde horizontal tiene que ser el caracter de relleno, no el vacio.
	if !strings.Contains(lines[0], " hi ") {
		t.Errorf("el titulo no aparece: %q", lines[0])
	}
}

// TestRenderTruncaElTituloMasAnchoQueLaCaja: un titulo de 30 celdas en una caja
// de 10 tiene que truncarse al ancho interior. Sin el truncate, la linea del
// borde empuja las demas y el titulo invade la caja de al lado.
func TestRenderTruncaElTituloMasAnchoQueLaCaja(t *testing.T) {
	const width = 10
	out := RenderWithTitleEx(lipgloss.RoundedBorder(), nil, AlignLeft, "TITULO MUY LARGO", "x", width)

	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w != width {
			t.Errorf("linea %d ancho = %d, want %d: %q", i, w, width, line)
		}
	}
}

// cellIndexOf devuelve la CELDA en la que aparece needle dentro de line,
// contando en ancho de pantalla y no en bytes. Hace falta porque las esquinas
// del borde son multibyte: strings.Index daría la posición en bytes, que en
// una caja con esquinas es un número sin relación con la columna.
func cellIndexOf(line, needle string) int {
	runes := []rune(line)
	w := 0
	for i := 0; i < len(runes); i++ {
		if strings.HasPrefix(string(runes[i:]), needle) {
			return w
		}
		w += ansi.StringWidth(string(runes[i]))
	}
	return -1
}

// TestRenderAlineamientosDelTitulo: los tres alineamientos tienen que colocar
// el titulo en puntos distintos de la linea de borde, y todos tienen que
// respetar el ancho. Es la propiedad que hace que un footer a la derecha y un
// header a la izquierda convivan en la misma caja.
func TestRenderAlineamientosDelTitulo(t *testing.T) {
	const width = 30
	const title = "T"

	tests := []struct {
		name       string
		align      int
		wantPrefix string
		wantSuffix string
		wantCell   int
	}{
		{"izquierda", AlignLeft, "╭" + title, "", 1},
		{"derecha", AlignRight, "", title + "╮", width - 2},
		{"centro", AlignCenter, "╭", "╮", (width - 1) / 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := RenderWithTitleEx(lipgloss.RoundedBorder(), nil, tt.align, title, "cuerpo", width)
			top := strings.Split(out, "\n")[0]

			if w := ansi.StringWidth(top); w != width {
				t.Fatalf("ancho = %d, want %d: %q", w, width, top)
			}
			if tt.wantPrefix != "" && !strings.HasPrefix(top, tt.wantPrefix) {
				t.Errorf("la linea no empieza por %q: %q", tt.wantPrefix, top)
			}
			if tt.wantSuffix != "" && !strings.HasSuffix(top, tt.wantSuffix) {
				t.Errorf("la linea no acaba en %q: %q", tt.wantSuffix, top)
			}
			if got := cellIndexOf(top, title); got != tt.wantCell {
				t.Errorf("titulo en la celda %d, want %d: %q", got, tt.wantCell, top)
			}
		})
	}
}

// TestRenderConBordeDeColor: cuando hay borderFg, los caracteres de borde se
// emiten con el SGR del color. El assert NO es el color exacto sino que el ANSI
// este presente y que el ancho MEDIDO (que ignora el ANSI) siga siendo el
// pedido: ese es el contrato que el resto de la TUI depende, porque las
// columnas se calculan con ansi.StringWidth.
func TestRenderConBordeDeColor(t *testing.T) {
	const width = 24
	out := RenderWithTitlesEx(
		lipgloss.RoundedBorder(),
		color.RGBA{R: 0x88, G: 0x44, B: 0xCC, A: 0xFF},
		" top ",
		AlignLeft,
		" bot ",
		AlignRight,
		"cuerpo",
		width,
	)

	if !strings.Contains(out, "\033[") {
		t.Fatal("con borderFg la caja deberia emitir SGR, y no hay ninguno")
	}
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w != width {
			t.Errorf("linea %d ancho MEDIDO = %d, want %d: %q", i, w, width, line)
		}
	}
}

// TestRenderEsquinasAnchasSaturanElAnchoInteriorAZero: con esquinas de mas de
// una celda (lipgloss las permite) el ancho interior sale NEGATIVO. El clamp a
// cero es lo que evita que strings.Repeat reciba un numero negativo y reviente
// con panic, asi que es codigo de seguridad y por eso se ejercita.
//
// Antes de este test el clamp estaba sin cubrir: ningun border del repo lo
// necesita (todos son de una celda), asi que una regresion aqui pasaria
// inadvertida hasta que alguien usara un border compuesto.
func TestRenderEsquinasAnchasSaturanElAnchoInteriorAZero(t *testing.T) {
	// "┌─┐" mide 3 celdas, asi que dos esquinas ya son 6: con width 2 el
	// interior es 2-6 = -4.
	border := lipgloss.Border{
		TopLeft:     "┌─┐",
		TopRight:    "┌─┐",
		BottomLeft:  "└─┘",
		BottomRight: "└─┘",
		Top:         "─",
		Left:        "│",
		Right:       "│",
		Bottom:      "─",
	}

	out := RenderWithTitleEx(border, nil, AlignLeft, "T", "cuerpo", 2)

	// Lo que importa es que no revienta y que el contenido no se pierde.
	if !strings.Contains(out, "cuerpo") {
		t.Errorf("el contenido se perdio al saturar el ancho interior: %q", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w < 0 {
			t.Errorf("linea %d con ancho negativo %d: %q", i, w, line)
		}
	}
}

// TestRenderEnvuelveElContenidoQueNoCabe: aqui se ejercita el wrapLine desde
// buildContentLines, no en aislamiento. El contenido se parte en varias lineas
// y CADA una tiene que medir el ancho interior exacto — es el requisito sin el
// cual el borde derecho deja de ser una columna recta.
func TestRenderEnvuelveElContenidoQueNoCabe(t *testing.T) {
	const width = 16
	out := RenderWithTitleEx(lipgloss.RoundedBorder(), nil, AlignLeft, " t ", "abcdefghijklmnopqrstuvwxyz", width)

	lines := strings.Split(out, "\n")
	// borde + (contenido partido en trozos de ancho interior) + borde.
	if len(lines) < 4 {
		t.Fatalf("el contenido largo deberia producir varias lineas, hubo %d: %q", len(lines), out)
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != width {
			t.Errorf("linea %d ancho = %d, want %d: %q", i, w, width, line)
		}
	}
	// Solo las lineas de cuerpo llevan borde vertical: la primera y la ultima
	// son las lineas del borde horizontal, con esquinas.
	body := lines[1 : len(lines)-1]
	if len(body) < 2 {
		t.Fatalf("el contenido se partio en %d lineas, want >=2: %q", len(body), body)
	}
	for i, line := range body {
		if !strings.HasPrefix(line, "│") {
			t.Errorf("cuerpo %d no empieza por el borde izquierdo: %q", i, line)
		}
	}
	// El texto tiene que seguir estando entero: el wrap reparte, nunca tira.
	// Se comprueba quitando los bordes y los espacios de relleno, porque los
	// trozos se cortan en el ancho interior, no en multiplos de 10.
	var texto strings.Builder
	for _, line := range body {
		texto.WriteString(ansi.Strip(strings.Trim(line, "│")))
	}
	if got, want := strings.TrimSpace(texto.String()), "abcdefghijklmnopqrstuvwxyz"; got != want {
		t.Errorf("contenido envolvente = %q, want %q", got, want)
	}
}

// TestRenderContenidoVacioDibujaUnaFila: un contenido vacio tiene que dar una
// linea de cuerpo en blanco del ancho interior. Sin esa fila, la caja sale
// aplastada contra su propio borde.
func TestRenderContenidoVacioDibujaUnaFila(t *testing.T) {
	const width = 12
	out := RenderWithTitleEx(lipgloss.RoundedBorder(), nil, AlignLeft, " t ", "", width)

	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("caja vacia = %d lineas, want 3 (borde, cuerpo, borde): %q", len(lines), out)
	}
	if want := "│" + strings.Repeat(" ", width-2) + "│"; lines[1] != want {
		t.Errorf("cuerpo = %q, want %q", lines[1], want)
	}
}

// TestRenderRellenaElContenidoCortoAPadDerecha: el cuerpo se rellena por la
// derecha hasta el borde. Un "│ab" sin relleno saca la columna del borde
// derecho, que es exactamente el defecto que hace la UI difficult de leer.
func TestRenderRellenaElContenidoCortoAPadDerecha(t *testing.T) {
	const width = 12
	out := RenderWithTitleEx(lipgloss.RoundedBorder(), nil, AlignLeft, "", "ab", width)

	lines := strings.Split(out, "\n")
	if want := "│ab" + strings.Repeat(" ", width-4) + "│"; lines[1] != want {
		t.Errorf("cuerpo = %q, want %q", lines[1], want)
	}
}

// TestRenderMultiplesLineasDeContenido: un contenido con saltos de linea
// explicitos produce una fila por linea, rellenada y con sus dos bordes.
func TestRenderMultiplesLineasDeContenido(t *testing.T) {
	const width = 10
	out := RenderWithTitleEx(lipgloss.RoundedBorder(), nil, AlignLeft, "", "a\nb\nc", width)

	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("tres lineas de cuerpo = %d lineas totales, want 5: %q", len(lines), out)
	}
	for i, want := range []string{"│a       │", "│b       │", "│c       │"} {
		if lines[i+1] != want {
			t.Errorf("cuerpo %d = %q, want %q", i, lines[i+1], want)
		}
	}
}
