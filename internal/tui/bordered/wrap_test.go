package bordered

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// wrapLine y parseAnsiSegments son el corazon del renderer y los dos tenian
// CERO cobertura: solo se llegaban a ellos cuando el contenido era mas ancho
// que la caja, y ningun test del repo lo era.
//
// Un fallo aqui no se ve como un fallo de test, se ve como una caja con el
// texto desbordado, partido a mitad de palabra, o con el color corrido hacia
// la linea siguiente. Es la clase de bug mas cara de esta TUI porque solo es
// visible en pantalla, donde nadie la mira hasta que ya no se lee.
// ---------------------------------------------------------------------------

// TestWrapLineSinANSIPartePorAnchoDePantalla: el caso base. Sin escapes, el
// wrap tiene que respectar maxDisplayWidth medido en celdas, no en bytes ni en
// runes: por eso el ancho es 5 y la palabra mide 7.
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

// TestWrapLineRespetaAnchoEnCeldasNoEnRunes es el test que distingue una
// implementacion correcta de una que cuenta caracteres. "áé" son 2 runes pero 2
// celdas; los emojis de casa son 4 bytes, 1 rune y 2 celdas. Un wrap que
// cuenta runes desborda la caja.
func TestWrapLineRespetaAnchoEnCeldasNoEnRunes(t *testing.T) {
	// 4 emojis de 2 celdas cada uno = 8 celdas de ancho real.
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

// TestWrapLineReparteElSobranteEnLaPrimeraLinea: cuando el ancho no divide la
// cadena, el resto va a la primera linea y NO a la ultima. Es la convencion
// que espera el resto de la UI, y al invertirla el detalle de un servicio
// aparece descuadrado un caracter respecto a su borde.
func TestWrapLineReparteElSobranteEnLaPrimeraLinea(t *testing.T) {
	got := wrapLine("abcdefg", 3)

	want := []string{"abc", "def", "g"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("wrapLine = %q, want %q", got, want)
	}
}

// TestWrapLineConAnchoNoPositivoNoPierdeTexto: con maxDisplayWidth <= 0 no hay
// por donde partir, asi que se devuelve la linea intacta. Perder texto aqui
// seria peor que no respectar el ancho: el usuario veria contenido que ya no
// esta.
func TestWrapLineConAnchoNoPositivoNoPierdeTexto(t *testing.T) {
	for _, width := range []int{0, -1, -100} {
		got := wrapLine("texto que no cabe", width)
		if len(got) != 1 || got[0] != "texto que no cabe" {
			t.Errorf("wrapLine(w=%d) = %q, want la linea intacta", width, got)
		}
	}
}

// TestWrapLineExactoEnElAnchoNoParte: una linea que ya cabe exactamente no se
// parte. Si se partiera, apareceria un chunk de ancho cero y la caja ganaria
// una linea en blanco espuria.
func TestWrapLineExactoEnElAnchoNoParte(t *testing.T) {
	got := wrapLine("abcde", 5)

	if len(got) != 1 || got[0] != "abcde" {
		t.Errorf("wrapLine = %q, want un unico chunk de 5", got)
	}
}

// TestWrapLineCierraElEstiloEnCadaChunk: al partir una linea con color hay que
// cerrar el SGR al final de cada trozo. Si no, el color se fuga a la linea
// siguiente y a todo lo que se dibuje despues — el sintoma clasico de "el
// borde de la caja sale del color del texto".
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

// TestWrapLineTrasUnResetNoArrastraElEstilo: "rojo resets, luego azul" no puede
// salir con el segundo tramo en rojo. El reset tiene que cerrar el estilo
// activo aunque todavia queden caracteres por delante.
func TestWrapLineTrasUnResetNoArrastraElEstilo(t *testing.T) {
	// "ab" en rojo, reset, "cd" en azul, y "cd" no cabe en 2 -> se parte.
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

// TestWrapLineSinEstiloNoInyectaReset: una linea sin ANSI no puede ganar un
// "\033[0m" del nada, o el terminal recibiria resets espurios.
func TestWrapLineSinEstiloNoInyectaReset(t *testing.T) {
	got := wrapLine("abcdef", 2)

	for i, chunk := range got {
		if strings.Contains(chunk, "\033") {
			t.Errorf("chunk %d injecto ANSI en una linea sin estilo: %q", i, chunk)
		}
	}
}

// TestWrapLineDeVaciaDevuelveUnChunkVacio: la cadena vacia debe dar UN chunk
// vacio, no cero chunks. El consumidor (buildContentLines) cuenta los chunks
// para saber cuantas lineas dibujo; cero chunks haria desaparecer la fila.
func TestWrapLineDeVaciaDevuelveUnChunkVacio(t *testing.T) {
	got := wrapLine("", 5)

	if len(got) != 1 || got[0] != "" {
		t.Errorf("wrapLine(\"\") = %q, want un unico chunk vacio", got)
	}
}

// TestIsResetStyle: el reset se escribe de dos formas y ambas son legitimas
// ("\x1b[0m" explicito y "\x1b[m" corto, que es lo que emite lipgloss). Si
// isResetStyle no reconociera la forma corta, el estilo activo nunca se
// limpiaria y un texto sin color heredaria el color del anterior.
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

// TestParseAnsiSegments: el segmentador tiene que separar estilo y texto de
// forma que wrapLine pueda decidir cuando abrir y cerrar el estilo activo.
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
			// Un ESC al final sin '[' no es una secuencia: es texto. Si se
			// tratara como secuencia malformada, se perderia el caracter.
			name: "ESC suelto al final es texto",
			in:   "ab\033",
			want: []ansiSegment{{style: "", text: "ab\033"}},
		},
		{
			// CSI sin byte final: la secuencia no se cierra y el resto se
			// consume como parte de ella. Es lo que hace el codigo, y el test
			// lo fija para que un cambio futuro sea deliberado.
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

// TestParseAnsiSegmentsReconstruyeLaEntrada: property test de ida y vuelta. Lo
// que el segmentador saca tiene que volver a dar la cadena original. Es la
// invariante que sostiene todo wrapLine: si un caracter se pierde aqui, el
// texto de un log desaparece de la caja sin que nada falle.
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
