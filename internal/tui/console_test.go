package tui

import (
	"strings"
	"testing"
)

// S19.8: los logs traen \r (progreso de Maven, spinners) y el renderer
// los interpreta con semántica de terminal (x = col 0), invadiendo el
// árbol. El saneo emula la sobrescritura por línea antes del viewport.
func TestSanitizeConsoleCarriageReturns(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "sobrescritura simple",
			in:   "aaa\rbbb",
			want: "bbb",
		},
		{
			name: "progreso maven: último segmento gana",
			in: "Progress (1): 0.5/4.2 kB\rProgress (1): 4.2 kB    \r" +
				"                    \rDownloaded from x: url (4.2 kB at 64 kB/s)",
			want: "Downloaded from x: url (4.2 kB at 64 kB/s)",
		},
		{
			name: "crlf se normaliza conservando el texto",
			in:   "line\r\nnext",
			want: "line\nnext",
		},
		{
			name: "CR colgante al final no borra la línea",
			in:   "open progress\r",
			want: "open progress",
		},
		{
			name: "segmento más largo que el previo extiende la línea",
			in:   "short\rvery long segment",
			want: "very long segment",
		},
		{
			name: "varias líneas con CR",
			in:   "p1\rFINAL1\np2\rFINAL2",
			want: "FINAL1\nFINAL2",
		},
		{
			name: "sin CR queda intacto",
			in:   "clean\nlines\n",
			want: "clean\nlines\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeConsole(tc.in); got != tc.want {
				t.Errorf("sanitizeConsole(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// E2E: el contenido con \r llega al viewport sin \r y solo con lo que un
// terminal mostraría (S19.8).
func TestSetConsoleContentEmulatesCarriageReturns(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	raw := "Progress (1): 0.5 kB\r                    \r" +
		"Downloaded from jfrog-ctti: https://example.com/x.pom\n"
	m.setConsoleContent(raw)

	view := m.consoleView.View()
	if strings.ContainsRune(view, '\r') {
		t.Errorf("el viewport contiene \\r: %q", view)
	}
	if !strings.Contains(view, "Downloaded from jfrog-ctti") {
		t.Errorf("viewport = %q, want línea final visible", view)
	}
	if strings.Contains(view, "Progress (1)") {
		t.Errorf("el segmento sobrescrito no debe verse: %q", view)
	}
}
