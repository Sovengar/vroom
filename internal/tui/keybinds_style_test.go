package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Every hint in the keybinds bar is a "key desc" pair: the key carries the accent and the
// description keeps the muted help colour. Separators belong to the description colour.
func TestRenderHelpAccentsKeysAndKeepsDescriptionsMuted(t *testing.T) {
	line := "s start/stop · R restart"
	got := renderHelp(line)

	want := styleHelpKey.Render("s") + " " + styleHelp.Render("start/stop") +
		styleHelp.Render(" · ") +
		styleHelpKey.Render("R") + " " + styleHelp.Render("restart")
	if got != want {
		t.Errorf("renderHelp(%q) = %q, want %q", line, got, want)
	}
	if gotW, plainW := lipglossWidth(got), lipglossWidth(line); gotW != plainW {
		t.Errorf("styling changed the visible width: %d, want %d", gotW, plainW)
	}
	if stripped := ansi.Strip(got); stripped != line {
		t.Errorf("styling changed the visible text: %q, want %q", stripped, line)
	}
}

// A truncated tail has no description left: it must not steal the key colour.
func TestRenderHelpLeavesTruncatedTailAsDescription(t *testing.T) {
	line := "s start/stop · r…"
	got := renderHelp(line)

	want := styleHelpKey.Render("s") + " " + styleHelp.Render("start/stop") +
		styleHelp.Render(" · ") + styleHelp.Render("r…")
	if got != want {
		t.Errorf("renderHelp(%q) = %q, want %q", line, got, want)
	}
}

// The pane composes the styled lines, so the real surface must show the accent key next to
// the muted description without the width arithmetic noticing the escape sequences.
func TestKeybindsBoxRendersAccentedKeys(t *testing.T) {
	m, _ := newTestModel(t)

	got := m.keybindsBox()
	if !strings.Contains(got, styleHelpKey.Render("s")+" "+styleHelp.Render("start/stop")) {
		t.Errorf("the keybinds box does not accent the start/stop key:\n%q", got)
	}
	if !strings.Contains(got, styleHelpKey.Render("q")+" "+styleHelp.Render("quit")) {
		t.Errorf("the keybinds box does not accent the quit key:\n%q", got)
	}
	if strings.Contains(got, styleHelp.Render("s start/stop")) {
		t.Errorf("the whole start/stop hint still uses the description style:\n%q", got)
	}
	for i, line := range strings.Split(got, "\n") {
		if w := lipglossWidth(line); w > m.width {
			t.Errorf("line %d measures %d cells, want <= %d", i, w, m.width)
		}
	}
}
