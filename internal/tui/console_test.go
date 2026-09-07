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

// Resaltado estilo IntelliJ: ERROR→rojo, WARN→amarillo, ruido (debug/
// trace, stack traces, [INFO] Maven)→gris tenue; el resto queda default
// y ERROR gana siempre que haya mezcla.
func TestHighlightConsole(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "línea ERROR va entera en rojo",
			in:   "2026-09-07 17:00:00.123 ERROR Connection refused",
			want: styleLineError.Render("2026-09-07 17:00:00.123 ERROR Connection refused"),
		},
		{
			name: "línea WARN va entera en amarillo",
			in:   "12:00:00 WARN deprecated config",
			want: styleLineWarn.Render("12:00:00 WARN deprecated config"),
		},
		{
			name: "prefijo Maven [WARNING] también es warn",
			in:   "[WARNING] Using platform encoding",
			want: styleLineWarn.Render("[WARNING] Using platform encoding"),
		},
		{
			name: "prefijo Maven [ERROR] va en rojo",
			in:   "[ERROR] Failed to execute goal",
			want: styleLineError.Render("[ERROR] Failed to execute goal"),
		},
		{
			name: "DEBUG va atenuado",
			in:   "10:00:00 DEBUG HikariPool stats",
			want: styleDim.Render("10:00:00 DEBUG HikariPool stats"),
		},
		{
			name: "TRACE va atenuado",
			in:   "TRACE resolving bean",
			want: styleDim.Render("TRACE resolving bean"),
		},
		{
			name: "frame de stack trace va atenuado",
			in:   "\tat com.example.orders.OrdersController.handle(OrdersController.java:42)",
			want: styleDim.Render("\tat com.example.orders.OrdersController.handle(OrdersController.java:42)"),
		},
		{
			name: "Caused by: va atenuado",
			in:   "Caused by: java.net.SocketTimeoutException: Read timed out",
			want: styleDim.Render("Caused by: java.net.SocketTimeoutException: Read timed out"),
		},
		{
			name: "... N more va atenuado",
			in:   "\t... 42 more",
			want: styleDim.Render("\t... 42 more"),
		},
		{
			name: "prefijo Maven [INFO] va atenuado",
			in:   "[INFO] Building orders-api 1.0.0",
			want: styleDim.Render("[INFO] Building orders-api 1.0.0"),
		},
		{
			name: "INFO normal queda default",
			in:   "INFO Started OrdersApplication in 2.1s",
			want: "INFO Started OrdersApplication in 2.1s",
		},
		{
			name: "línea sin tokens queda intacta",
			in:   "Tomcat started on port 8080",
			want: "Tomcat started on port 8080",
		},
		{
			name: "líneas vacías sin escapes",
			in:   "a\n\nb",
			want: "a\n\nb",
		},
		{
			name: "ERROR gana sobre DEBUG y stack trace",
			in:   "\tat ERROR DEBUG x",
			want: styleLineError.Render("\tat ERROR DEBUG x"),
		},
		{
			name: "WARN gana sobre TRACE",
			in:   "TRACE WARN mixed",
			want: styleLineWarn.Render("TRACE WARN mixed"),
		},
		{
			name: "buffer vacío pasa tal cual",
			in:   "",
			want: "",
		},
		{
			name: "multi-línea mezclada resalta solo lo que toca",
			in:   "Booting Spring\nERROR boom\nTomcat started\n[INFO] done",
			want: "Booting Spring\n" +
				styleLineError.Render("ERROR boom") + "\n" +
				"Tomcat started\n" +
				styleDim.Render("[INFO] done"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := highlightConsole(tc.in); got != tc.want {
				t.Errorf("highlightConsole(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// E2E: el buffer llega al viewport resaltado (ANSI en la línea ERROR) y
// sin \r (S19.8 + highlight).
func TestSetConsoleContentHighlights(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.setConsoleContent("plain line\nERROR boom\n")

	view := m.consoleView.View()
	if want := styleLineError.Render("ERROR boom"); !strings.Contains(view, want) {
		t.Errorf("viewport = %q, want línea ERROR estilizada con %q", view, want)
	}
	if !strings.Contains(view, "plain line") {
		t.Errorf("viewport = %q, want línea default visible", view)
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
