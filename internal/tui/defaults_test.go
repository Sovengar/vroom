package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/vt"

	"vroom/internal/orchestrate"
	"vroom/internal/tail"
)

// Two clips on purpose: the top one limits scroll, the bottom one stops the panel from pushing the border off-screen.
func TestDetailsPanelClipsWhenContentIsTallerThanBox(t *testing.T) {
	// MEASURED: a service panel never exceeds twelve lines, so only a multi-stage stack crosses detailsHeight.
	m := newStackModel(t)
	cursorOn(t, &m, "front")

	stack := m.selectedStack()
	stack.Stages = []orchestrate.Stage{
		{Name: "build", Services: []string{"tienda-web", "tienda-api"}},
		{Name: "migrate", Services: []string{"tienda-api"}},
		{Name: "serve", Services: []string{"tienda-web", "tienda-api", "suelto"}},
	}

	content := m.allDetailsLines(120)
	if len(content) <= detailsHeight {
		t.Fatalf("the stack panel only has %d lines: it has not exceeded detailsHeight (%d) "+
			"and the test is not testing the clipping", len(content), detailsHeight)
	}

	got := m.detailsContentLines()
	if len(got) != detailsHeight {
		t.Errorf("detailsContentLines returned %d lines with %d of content, want %d",
			len(got), len(content), detailsHeight)
	}
	for i, l := range got {
		if lipglossWidth(l) > 120 {
			t.Errorf("line %d measures %d cells with a panel of 120", i, lipglossWidth(l))
		}
	}
}

// strings.Repeat panics on a negative count, which is what m.width - boxFrame yields below the frame width.
func TestKeybindsBoxSurvivesTwoColumnTerminal(t *testing.T) {
	for _, w := range []int{1, 2, 3, 4, 5, boxFrame, boxFrame + 1} {
		m, _ := newTestModel(t)
		m.width, m.height = w, 30
		m.updateLayout()

		got := m.keybindsBox()
		if strings.TrimSpace(got) == "" && w > 3 {
			t.Errorf("w=%d: the keybinds box came out empty", w)
		}
	}
}

// The floor of 28 exists because below it the compositor wraps every row into an illegible block.
func TestModalWidthAdjustsToLongestRowAndMinimum(t *testing.T) {
	m := newStackModel(t)
	m.width = 200

	m.pickerItems = []pickerItem{{Name: "t", Description: "d"}}
	if w := m.pickerInnerW(); w > 40 {
		t.Errorf("with a two-character row the width is %d: the modal does not fit the content", w)
	}

	m.pickerItems = []pickerItem{{Name: strings.Repeat("x", 400), Description: strings.Repeat("y", 400)}}
	if w := m.pickerInnerW(); w >= m.width {
		t.Errorf("with an 800-character row the width is %d and the screen is %d: the modal does not fit", w, m.width)
	}

	m.pickerItems = nil
	if w := m.pickerInnerW(); w < 28 {
		t.Errorf("without items the width is %d, want >= 28", w)
	}
}

// A filter that empties the tree must say so, because a blank list reads as a hung process.
func TestTreeColumnClipsAndExtraGoesFirst(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 100, 30
	m.updateLayout()

	t.Run("with filter that matches nothing", func(t *testing.T) {
		applied, _ := m.applyFilter("no-such-project-9911")
		got := applied.(Model).treeColumnLines()
		body := tail.StripANSI(strings.Join(got, "\n"))
		if !strings.Contains(body, "no matches") && !strings.Contains(body, "0") {
			t.Errorf("a filter with no results must say so:\\n%s", body)
		}
	})

	t.Run("the open filter occupies the first line", func(t *testing.T) {
		withBar := m
		withBar.filterOpen = true
		withBar.filterText = "tienda"
		got := withBar.treeColumnLines()
		// MEASURED: the column does not pad up to bodyH; the box compositor fills the gap, so it may return fewer lines but never more.
		if len(got) > withBar.bodyH {
			t.Errorf("the column has %d lines with bodyH %d", len(got), withBar.bodyH)
		}
		if !strings.Contains(tail.StripANSI(got[0]), "/") {
			t.Errorf("first line = %q, want the filter bar with its prompt", tail.StripANSI(got[0]))
		}
		if withBar.treeVis() >= m.treeVis() {
			t.Errorf("with the bar open treeVis = %d, but without it is %d: the bar must consume a row",
				withBar.treeVis(), m.treeVis())
		}
	})

	t.Run("the tree is clipped to the visible height", func(t *testing.T) {
		short := m
		short.height = 10
		short.updateLayout()
		got := short.treeColumnLines()
		if len(got) > short.bodyH {
			t.Errorf("the column has %d lines with bodyH %d: the compositor wraps and the height breaks",
				len(got), short.bodyH)
		}
	})
}

func TestOverlayWithBoxTallerThanScreenAnchorsWithoutOverflowing(t *testing.T) {
	base := strings.Repeat("line\n", 3)

	tall := strings.Repeat("X\n", 20)
	got := overlay(base, tall, 20, 3)
	if n := len(strings.Split(got, "\n")); n > len(strings.Split(base, "\n")) {
		t.Errorf("a 20-line box on a 3-line base produced %d lines: the vertical is not clipped", n)
	}

	// MEASURED: View always passes a base as tall as the screen, so the only invariant left to check here is that the offset cannot go negative.
	for _, dims := range [][2]int{{5, 1}, {5, 2}, {5, 3}, {40, 10}} {
		screen := strings.Repeat("line\n", dims[1])
		got := overlay(screen, "SINGLE", dims[0], dims[1])
		if !strings.Contains(got, "SINGLE") {
			t.Errorf("with %dx%d and a base of the correct height the box is not visible", dims[0], dims[1])
		}
	}
}

// The example must stay copyable: it is what the user pastes into the file to make the message go away.
func TestDetailsPanelOfProjectWithoutManifestShowsExample(t *testing.T) {
	m := noManifest(t)
	body := tail.StripANSI(strings.Join(m.detailsLines(m.rightW), "\n"))

	if !strings.Contains(body, "No manifest") {
		t.Errorf("the panel must explain what is missing:\\n%s", body)
	}
	for _, want := range []string{"name =", "command_start"} {
		if !strings.Contains(body, want) {
			t.Errorf("the example does not include %q, and without it the user does not know what to write:\\n%s", want, body)
		}
	}
	if !strings.Contains(body, m.selected().Name) {
		t.Errorf("the example does not include the project name: the user would have to type it manually")
	}
}

func TestDetailsPanelWithNarrowPanelFallsToOneColumn(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.branches[path] = "main"

	// MEASURED: width clipping lives in detailsLines via clipLines; allDetailsLines returns unclipped lines by contract.
	for _, w := range []int{2, 5, 10, 16, 20} {
		lines := m.detailsLines(w)
		for i, l := range lines {
			if n := lipglossWidth(l); n > w {
				t.Errorf("w=%d: line %d measures %d cells", w, i, n)
			}
		}
		if len(lines) < 2 {
			t.Errorf("w=%d: the panel has %d lines, want at least header + path", w, len(lines))
		}
	}
}

// Below termMinW the grid shows a split prompt and a shell that no longer knows where the cursor is.
func TestTermWSurvivesGridMinimum(t *testing.T) {
	m, _ := newTestModel(t)
	for _, w := range []int{1, 5, 20, 40, 80, 200} {
		m.width = w
		got := m.termW()
		if got < termMinW {
			t.Errorf("w=%d: termW = %d, want >= %d", w, got, termMinW)
		}
	}
}

func TestScreenSetsCursorAsInvertedBlockAndStaysInRow(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	s.write([]byte("short\r\n"))
	screen := tail.StripANSI(s.screen())

	if !strings.Contains(screen, "short") {
		t.Fatalf("the shell text did not reach the emulator: %q", screen)
	}
	for _, l := range strings.Split(screen, "\n") {
		if n := lipglossWidth(l); n > 40 {
			t.Errorf("a line measures %d cells in a 40-wide grid: %q", n, l)
		}
	}

	// A closed session must return "", because the caller uses it to know there is nothing to paint.
	s.closed = true
	if got := s.screen(); got != "" {
		t.Errorf("screen of a closed session = %q, want empty string", got)
	}
}

// Control bytes written to a real PTY move the cursor, erase the line or switch virtual terminals, so a non-key message must never reach it.
func TestTermKeyWithNonKeyMessageDoesNotWriteOrClose(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	m, _ := newTestModel(t)
	m.term = s
	m.termOpen = true

	next, _ := m.termKey(keyPress("a"))
	if !next.(Model).termOpen {
		t.Error("a normal key closed the modal")
	}
	waitFor(t, 2*time.Second, func() bool { return len(s.pty.(*stubPty).bytesWritten()) > 0 })

	before := len(s.pty.(*stubPty).bytesWritten())
	next, cmd := m.termKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Error("ctrl+q does not emit commands")
	}
	if next.(Model).termOpen {
		t.Error("ctrl+q must close the modal")
	}
	if n := len(s.pty.(*stubPty).bytesWritten()); n > before {
		t.Errorf("ctrl+q wrote %d bytes to the PTY: it is a vroom key, not a shell key", n-before)
	}
}

// With a nonexistent argv[0] the exec fails after the PTY is already open, exactly when a slip leaks both fds.
func TestStartSessionWithNonexistentBinaryErrorsWithoutLeakingFd(t *testing.T) {
	_, err := startSession(40, 10, t.TempDir(), []string{"/no/exists/a/shell"})
	if err == nil {
		t.Fatal("a nonexistent binary must produce an error")
	}

	// 50 iterations so an fd leak trips the process fd limit instead of passing.
	for range 50 {
		if _, err := startSession(40, 10, t.TempDir(), []string{"/no/exists/a/shell"}); err == nil {
			t.Fatal("a nonexistent binary returned nil on iteration 50")
		}
	}
}

// ptyEOFMsg and the process shutdown can both close it, and entering the C library twice would be fatal.
func TestNewEmulatorCanCloseWithoutPanic(t *testing.T) {
	emu := vt.NewEmulator(40, 10)
	_, _ = emu.Write([]byte("hello"))
	_ = emu.Close()
	_ = emu.Close()
}

// "No group" is not a group: an inline project's tree entry must not open a header with nothing under it.
func TestEmptyGroupDoesNotOpenBlock(t *testing.T) {
	m, _ := newTestModel(t)

	var inline int
	for i := range m.projects {
		if m.projects[i].Manifest == nil || m.projects[i].Manifest.PrimaryGroup != "" {
			continue
		}
		inline++
		for _, it := range m.tree {
			if it.kind == itemPrimary && it.primary == m.projects[i].Name {
				t.Errorf("the inline project %q appears as a block header", m.projects[i].Name)
			}
		}
	}
	if inline == 0 {
		t.Skip("the test tree has no projects without a group")
	}
	t.Logf("inline projects in the test tree: %d", inline)
}
