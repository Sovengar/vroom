package bordered

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestWrapLineSinANSIPartePorAnchoDePantalla(t *testing.T) {
	got := wrapLine("abcdefghij", 5)

	want := []string{"abcde", "fghij"}
	if len(got) != len(want) {
		t.Fatalf("wrapLine = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A rune-counting implementation would also pass here, because the house emoji is 4 bytes, 1 rune and 2 cells.
func TestWrapLineRespetaAnchoEnCeldasNoEnRunes(t *testing.T) {
	got := wrapLine("🏠🏠🏠🏠", 4)

	for i, chunk := range got {
		if w := ansi.StringWidth(chunk); w != 4 {
			t.Errorf("chunk %d = %q con ancho %d, want 4", i, chunk, w)
		}
	}
	if len(got) != 2 {
		t.Errorf("wrapLine produjo %d chunks (%q), want 2 de 4 celdas", len(got), got)
	}
}

// The remainder goes to the first line, not the last: inverting that shifts a service detail one cell off its border.
func TestWrapLineReparteElSobranteEnLaPrimeraLinea(t *testing.T) {
	got := wrapLine("abcdefg", 3)

	want := []string{"abc", "def", "g"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrapLine = %q, want %q", got, want)
	}
}

// With no positive width there is nowhere to split, so the line comes back intact: losing text is worse than overflowing.
func TestWrapLineConAnchoNoPositivoNoPierdeTexto(t *testing.T) {
	for _, width := range []int{0, -1, -100} {
		got := wrapLine("texto que no cabe", width)
		if len(got) != 1 || got[0] != "texto que no cabe" {
			t.Errorf("wrapLine(w=%d) = %q, want la linea intacta", width, got)
		}
	}
}

// An exact fit must not split, or a zero-width chunk appears and the box gains a spurious blank line.
func TestWrapLineExactoEnElAnchoNoParte(t *testing.T) {
	got := wrapLine("abcde", 5)

	if len(got) != 1 || got[0] != "abcde" {
		t.Errorf("wrapLine = %q, want un unico chunk de 5", got)
	}
}

// Each chunk must close its SGR, otherwise the colour leaks into the next line and everything drawn after it.
func TestWrapLineCierraElEstiloEnCadaChunk(t *testing.T) {
	const sgr = "\033[31m"
	got := wrapLine(sgr+"abcdef", 3)

	if len(got) != 2 {
		t.Fatalf("wrapLine produjo %d chunks (%q), want 2", len(got), got)
	}
	for i, chunk := range got {
		if !strings.HasPrefix(chunk, sgr) {
			t.Errorf("chunk %d no reabre el estilo: %q", i, chunk)
		}
		if !strings.HasSuffix(chunk, "\033[0m") {
			t.Errorf("chunk %d no cierra el estilo: %q", i, chunk)
		}
		if w := ansi.StringWidth(chunk); w != 3 {
			t.Errorf("chunk %d ancho = %d, want 3", i, w)
		}
	}
}

// "ab" in red, reset, "cd" in blue, and "cd" does not fit in 2, so it splits.
func TestWrapLineTrasUnResetNoArrastraElEstilo(t *testing.T) {
	got := wrapLine("\033[31mab\033[0m\033[34mcd", 2)

	if len(got) != 2 {
		t.Fatalf("wrapLine produjo %d chunks (%q), want 2", len(got), got)
	}
	if strings.Contains(got[1], "\033[31m") {
		t.Errorf("el segundo chunk arrastra el rojo del tramo anterior: %q", got[1])
	}
	if !strings.Contains(got[1], "\033[34m") {
		t.Errorf("el segundo chunk perdio el azul: %q", got[1])
	}
}

func TestWrapLineSinEstiloNoInyectaReset(t *testing.T) {
	got := wrapLine("abcdef", 2)

	for i, chunk := range got {
		if strings.Contains(chunk, "\033") {
			t.Errorf("chunk %d injecto ANSI en una linea sin estilo: %q", i, chunk)
		}
	}
}

// One empty chunk, not zero: buildContentLines counts chunks to decide how many rows to draw, so zero would erase the row.
func TestWrapLineDeVaciaDevuelveUnChunkVacio(t *testing.T) {
	got := wrapLine("", 5)

	if len(got) != 1 || got[0] != "" {
		t.Errorf("wrapLine(\"\") = %q, want un unico chunk vacio", got)
	}
}

// lipgloss emits the short "\x1b[m" form, so isResetStyle must recognise both or a style-less text inherits the previous colour.
func TestIsResetStyle(t *testing.T) {
	tests := []struct {
		name string
		sgr  string
		want bool
	}{
		{"reset explicito", "\033[0m", true},
		{"reset corto", "\033[m", true},
		{"color foreground", "\033[31m", false},
		{"color 256", "\033[38;5;196m", false},
		{"negrita", "\033[1m", false},
		{"sin ESC", "[0m", false},
		{"ESC sin CSI", "\0330m", false},
		{"CSI sin final", "\033[", false},
		{"vacio", "", false},
		{"ESC solo", "\033", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isResetStyle(tt.sgr); got != tt.want {
				t.Errorf("isResetStyle(%q) = %v, want %v", tt.sgr, got, tt.want)
			}
		})
	}
}

func TestParseAnsiSegments(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []ansiSegment
	}{
		{
			name: "solo texto",
			in:   "abc",
			want: []ansiSegment{{style: "", text: "abc"}},
		},
		{
			name: "texto con estilo en medio",
			in:   "ab\033[31mcd",
			want: []ansiSegment{{style: "", text: "ab"}, {style: "\033[31m", text: ""}, {style: "", text: "cd"}},
		},
		{
			name: "vacio",
			in:   "",
			want: nil,
		},
		{
			name: "solo escape",
			in:   "\033[0m",
			want: []ansiSegment{{style: "\033[0m", text: ""}},
		},
		{
			// A trailing ESC with no '[' is text, not a malformed sequence, so treating it as one would drop the character.
			name: "ESC suelto al final es texto",
			in:   "ab\033",
			want: []ansiSegment{{style: "", text: "ab\033"}},
		},
		{
			// A CSI with no final byte is consumed whole; the test pins today's behaviour so any change has to be deliberate.
			name: "CSI sin terminador se consume entero",
			in:   "\033[38;5",
			want: []ansiSegment{{style: "\033[38;5", text: ""}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAnsiSegments(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("parseAnsiSegments(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("segmento %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// Round-trip: losing a character here silently drops log text from the box, which is the invariant that holds all of wrapLine up.
func TestParseAnsiSegmentsReconstruyeLaEntrada(t *testing.T) {
	inputs := []string{
		"",
		"texto plano",
		"\033[31mrojo\033[0m",
		"antes\033[1mdurante\033[0mdespues",
		"\033[38;2;255;0;0mtruecolor\033[m",
		"emoji 🏠 con estilo\033[32m y mas\033[0m",
		"tabulador\ty salto",
	}

	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			var b strings.Builder
			for _, seg := range parseAnsiSegments(in) {
				b.WriteString(seg.style)
				b.WriteString(seg.text)
			}
			if got := b.String(); got != in {
				t.Errorf("ida y vuelta = %q, want %q", got, in)
			}
		})
	}
}
