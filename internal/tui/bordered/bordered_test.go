package bordered

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestRenderWithTitlesBottomRight verifica que el footer se incruste en la
// línea del borde inferior, alineado a la derecha.
func TestRenderWithTitlesBottomRight(t *testing.T) {
	const width = 40
	out := RenderWithTitlesEx(
		lipgloss.RoundedBorder(),
		nil,
		" Tasklist ",
		AlignLeft,
		" 1-10 of 306 · Page 1/31 ",
		AlignRight,
		"body",
		width,
	)
	lines := strings.Split(out, "\n")

	top := lines[0]
	if !strings.HasPrefix(top, "╭ Tasklist ") {
		t.Errorf("top line = %q", top)
	}

	bottom := lines[len(lines)-1]
	if !strings.HasPrefix(bottom, "╰") {
		t.Errorf("bottom should start with corner: %q", bottom)
	}
	if !strings.HasSuffix(bottom, "╯") {
		t.Errorf("bottom should end with corner: %q", bottom)
	}
	if !strings.Contains(bottom, "1-10 of 306 · Page 1/31") {
		t.Errorf("bottom legend missing: %q", bottom)
	}
	if w := ansi.StringWidth(bottom); w != width {
		t.Errorf("bottom width = %d, want %d", w, width)
	}
}

// TestRenderWithTitleExKeepsEmptyBottom asegura que la API previa no dibuje
// ningún texto en el borde inferior.
func TestRenderWithTitleExKeepsEmptyBottom(t *testing.T) {
	const width = 20
	out := RenderWithTitleEx(lipgloss.RoundedBorder(), nil, AlignLeft, " X ", "body", width)
	lines := strings.Split(out, "\n")
	bottom := lines[len(lines)-1]

	want := "╰" + strings.Repeat("─", width-2) + "╯"
	if bottom != want {
		t.Errorf("bottom = %q, want %q", bottom, want)
	}
}
