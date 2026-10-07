package tui

import (
	"strings"
	"testing"
)

// Logs carry \r (Maven progress, spinners) and the renderer reads it as column 0, corrupting the tree, so sanitizeConsole emulates per-line overwrite.
func TestSanitizeConsoleCarriageReturns(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "simple overwrite",
			in:   "aaa\rbbb",
			want: "bbb",
		},
		{
			name: "maven progress: last segment wins",
			in: "Progress (1): 0.5/4.2 kB\rProgress (1): 4.2 kB    \r" +
				"                    \rDownloaded from x: url (4.2 kB at 64 kB/s)",
			want: "Downloaded from x: url (4.2 kB at 64 kB/s)",
		},
		{
			name: "crlf is normalized preserving the text",
			in:   "line\r\nnext",
			want: "line\nnext",
		},
		{
			name: "dangling CR at end does not erase the line",
			in:   "open progress\r",
			want: "open progress",
		},
		{
			name: "segment longer than previous extends the line",
			in:   "short\rvery long segment",
			want: "very long segment",
		},
		{
			name: "multiple lines with CR",
			in:   "p1\rFINAL1\np2\rFINAL2",
			want: "FINAL1\nFINAL2",
		},
		{
			name: "without CR stays intact",
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

// IntelliJ-style palette is deliberate, and ERROR outranks noise when both hit the same line.
func TestHighlightConsole(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "ERROR line goes entirely red",
			in:   "2026-09-07 17:00:00.123 ERROR Connection refused",
			want: styleLineError.Render("2026-09-07 17:00:00.123 ERROR Connection refused"),
		},
		{
			name: "WARN line goes entirely yellow",
			in:   "12:00:00 WARN deprecated config",
			want: styleLineWarn.Render("12:00:00 WARN deprecated config"),
		},
		{
			name: "Maven [WARNING] prefix is also warn",
			in:   "[WARNING] Using platform encoding",
			want: styleLineWarn.Render("[WARNING] Using platform encoding"),
		},
		{
			name: "Maven [ERROR] prefix goes red",
			in:   "[ERROR] Failed to execute goal",
			want: styleLineError.Render("[ERROR] Failed to execute goal"),
		},
		{
			name: "DEBUG goes dimmed",
			in:   "10:00:00 DEBUG HikariPool stats",
			want: styleDim.Render("10:00:00 DEBUG HikariPool stats"),
		},
		{
			name: "TRACE goes dimmed",
			in:   "TRACE resolving bean",
			want: styleDim.Render("TRACE resolving bean"),
		},
		{
			name: "stack trace frame goes dimmed",
			in:   "\tat com.example.orders.OrdersController.handle(OrdersController.java:42)",
			want: styleDim.Render("\tat com.example.orders.OrdersController.handle(OrdersController.java:42)"),
		},
		{
			name: "Caused by: goes dimmed",
			in:   "Caused by: java.net.SocketTimeoutException: Read timed out",
			want: styleDim.Render("Caused by: java.net.SocketTimeoutException: Read timed out"),
		},
		{
			name: "... N more goes dimmed",
			in:   "\t... 42 more",
			want: styleDim.Render("\t... 42 more"),
		},
		{
			name: "Maven [INFO] prefix goes dimmed",
			in:   "[INFO] Building orders-api 1.0.0",
			want: styleDim.Render("[INFO] Building orders-api 1.0.0"),
		},
		{
			name: "normal INFO stays default",
			in:   "INFO Started OrdersApplication in 2.1s",
			want: "INFO Started OrdersApplication in 2.1s",
		},
		{
			name: "line without tokens stays intact",
			in:   "Tomcat started on port 8080",
			want: "Tomcat started on port 8080",
		},
		{
			name: "empty lines without escapes",
			in:   "a\n\nb",
			want: "a\n\nb",
		},
		{
			name: "ERROR wins over DEBUG and stack trace",
			in:   "\tat ERROR DEBUG x",
			want: styleLineError.Render("\tat ERROR DEBUG x"),
		},
		{
			name: "WARN wins over TRACE",
			in:   "TRACE WARN mixed",
			want: styleLineWarn.Render("TRACE WARN mixed"),
		},
		{
			name: "empty buffer passes as-is",
			in:   "",
			want: "",
		},
		{
			name: "mixed multi-line highlights only what applies",
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

func TestSetConsoleContentHighlights(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.setConsoleContent("plain line\nERROR boom\n")

	view := m.consoleView.View()
	if want := styleLineError.Render("ERROR boom"); !strings.Contains(view, want) {
		t.Errorf("viewport = %q, want ERROR line styled with %q", view, want)
	}
	if !strings.Contains(view, "plain line") {
		t.Errorf("viewport = %q, want default line visible", view)
	}
}

func TestSetConsoleContentEmulatesCarriageReturns(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	raw := "Progress (1): 0.5 kB\r                    \r" +
		"Downloaded from jfrog-ctti: https://example.com/x.pom\n"
	m.setConsoleContent(raw)

	view := m.consoleView.View()
	if strings.ContainsRune(view, '\r') {
		t.Errorf("the viewport contains \\r: %q", view)
	}
	if !strings.Contains(view, "Downloaded from jfrog-ctti") {
		t.Errorf("viewport = %q, want final line visible", view)
	}
	if strings.Contains(view, "Progress (1)") {
		t.Errorf("the overwritten segment must not be visible: %q", view)
	}
}
