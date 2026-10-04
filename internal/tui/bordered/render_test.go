package bordered

import (
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Below 2 cells no box is possible, so the width clamps up rather than subtracting a negative and handing strings.Repeat a panic.
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

// lipgloss permits empty border runes for partial borders; unfilled they measure zero width, so the line comes up a cell short of the others.
func TestRenderConCaracteresDeBordeVaciosLosSustituyePorEspacio(t *testing.T) {
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
	if !strings.Contains(lines[0], " hi ") {
		t.Errorf("el titulo no aparece: %q", lines[0])
	}
}

// A title wider than the box must be truncated, or the border row overflows and the title invades the neighbouring box.
func TestRenderTruncaElTituloMasAnchoQueLaCaja(t *testing.T) {
	const width = 10
	out := RenderWithTitleEx(lipgloss.RoundedBorder(), nil, AlignLeft, "TITULO MUY LARGO", "x", width)

	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w != width {
			t.Errorf("linea %d ancho = %d, want %d: %q", i, w, width, line)
		}
	}
}

// Counts screen cells, not bytes: border corners are multibyte, so strings.Index would return an offset unrelated to the column.
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

// The contract is the ANSI-ignoring measured width, not the exact color, because the rest of the TUI computes columns with ansi.StringWidth.
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

// No border in the repo needs this clamp (all corners are one cell), so the branch would regress unnoticed until someone used a compound border.
func TestRenderEsquinasAnchasSaturanElAnchoInteriorAZero(t *testing.T) {
	// "┌─┐" is 3 cells wide, so two corners already take 6 and width 2 leaves an interior of -4.
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

	if !strings.Contains(out, "cuerpo") {
		t.Errorf("el contenido se perdio al saturar el ancho interior: %q", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(line); w < 0 {
			t.Errorf("linea %d con ancho negativo %d: %q", i, w, line)
		}
	}
}

// Exercises wrapLine through buildContentLines rather than in isolation.
func TestRenderEnvuelveElContenidoQueNoCabe(t *testing.T) {
	const width = 16
	out := RenderWithTitleEx(lipgloss.RoundedBorder(), nil, AlignLeft, " t ", "abcdefghijklmnopqrstuvwxyz", width)

	lines := strings.Split(out, "\n")
	if len(lines) < 4 {
		t.Fatalf("el contenido largo deberia producir varias lineas, hubo %d: %q", len(lines), out)
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != width {
			t.Errorf("linea %d ancho = %d, want %d: %q", i, w, width, line)
		}
	}
	body := lines[1 : len(lines)-1]
	if len(body) < 2 {
		t.Fatalf("el contenido se partio en %d lineas, want >=2: %q", len(body), body)
	}
	for i, line := range body {
		if !strings.HasPrefix(line, "│") {
			t.Errorf("cuerpo %d no empieza por el borde izquierdo: %q", i, line)
		}
	}
	// The wrap redistributes and never drops text; checking it means stripping borders and padding because the cuts land on the inner width, not on multiples of 10.
	var texto strings.Builder
	for _, line := range body {
		texto.WriteString(ansi.Strip(strings.Trim(line, "│")))
	}
	if got, want := strings.TrimSpace(texto.String()), "abcdefghijklmnopqrstuvwxyz"; got != want {
		t.Errorf("contenido envolvente = %q, want %q", got, want)
	}
}

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

func TestRenderRellenaElContenidoCortoAPadDerecha(t *testing.T) {
	const width = 12
	out := RenderWithTitleEx(lipgloss.RoundedBorder(), nil, AlignLeft, "", "ab", width)

	lines := strings.Split(out, "\n")
	if want := "│ab" + strings.Repeat(" ", width-4) + "│"; lines[1] != want {
		t.Errorf("cuerpo = %q, want %q", lines[1], want)
	}
}

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
