package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"vroom/internal/gitinfo"
	"vroom/internal/state"
	"vroom/internal/tail"
)

// Both halves are needed: clipping protects against content taller than the box, padding against content shorter, and a negative n must yield no lines at all.
func TestPadLinesPadsAndClipsToExactHeight(t *testing.T) {
	tests := []struct {
		name  string
		in    []string
		n     int
		want  int
		check func([]string)
	}{
		{"pads", []string{"a"}, 4, 4, func(g []string) {
			if g[1] != "" || g[3] != "" {
				t.Errorf("the padding lines must be empty: %q", g)
			}
		}},
		{"clips", []string{"a", "b", "c", "d"}, 2, 2, func(g []string) {
			if g[1] != "b" {
				t.Errorf("the clip must preserve the beginning: %q", g)
			}
		}},
		{"exact", []string{"a", "b"}, 2, 2, nil},
		{"empty to zero", []string{"a", "b"}, 0, 0, nil},
		{"negative", []string{"a", "b"}, -5, 0, nil},
		{"empty", nil, 3, 3, nil},
		{"more than the budget", nil, 0, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := padLines(tt.in, tt.n)
			if len(got) != tt.want {
				t.Fatalf("len = %d, want %d: %q", len(got), tt.want, got)
			}
			if tt.check != nil {
				tt.check(got)
			}
		})
	}
}

// Width is measured in visible cells: len() would count ANSI bytes and make the compositor wrap the line, changing the box height.
func TestFitLinesClipsEachLineToVisibleWidth(t *testing.T) {
	lines := []string{
		strings.Repeat("x", 50),
		"\x1b[31m" + strings.Repeat("y", 50) + "\x1b[0m",
		strings.Repeat("€", 20),
	}
	got := fitLines(lines, 10)

	for i, l := range got {
		if n := lipglossWidth(l); n > 10 {
			t.Errorf("line %d measures %d visible cells, want <= 10: %q", i, n, l)
		}
	}
	// Escaped content survives: fitLines truncates, it never strips escapes.
	if !strings.Contains(got[1], "\x1b[31m") {
		t.Errorf("the style was lost when clipping: %q", got[1])
	}
	if n := lipglossWidth(got[2]); n > 10 {
		t.Errorf("with multibyte measures %d cells, want <= 10", n)
	}
}

// The height is fixed because the Details box has a border and a title: fewer lines leave the bottom border floating, more lines push it off screen.
func TestDetailsContentLinesAlwaysReturnsBoxHeight(t *testing.T) {
	withProject, _ := newTestModel(t)
	withProject = moveCursorTo(t, withProject, "tienda-api")
	// Branch, pattern and pid are added so the panel gets the maximum number of rows.
	path := pathOfSelected(t, withProject)
	withProject.branches[path] = "feature/a-very-long-branch-that-fits"
	if p := withProject.projectByPath(path); p != nil && p.Manifest != nil {
		p.Manifest.ProcessPattern = "a.long.process.pattern"
	}
	withProject.services[path].Meta.Pid = 4321
	withProject.services[path].Meta.StartedAt = "2026-10-03 12:00:00"

	for _, w := range []int{30, 60, 140} {
		for _, m := range []Model{withProject} {
			m.width, m.height = w+40, 30
			m.updateLayout()
			got := m.detailsContentLines()
			if len(got) != detailsHeight {
				t.Errorf("w=%d with a full project panel: %d lines, want %d", w, len(got), detailsHeight)
			}
			for i, l := range got {
				if lipglossWidth(l) > m.rightW {
					t.Errorf("rightW=%d: line %d measures %d cells: the compositor would wrap it and the box height would change",
						m.rightW, i, lipglossWidth(l))
				}
			}
		}
	}
}

// Both clamps matter: a top past the content indexes out of range after a refresh shrinks it, a negative one panics.
func TestDetailsContentLinesAppliesScrollAndDoesNotOverflow(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	base := tail.StripANSI(strings.Join(m.detailsContentLines(), "\n"))

	t.Run("scroll 0 is the natural position", func(t *testing.T) {
		m.detailsTop = 0
		if got := tail.StripANSI(strings.Join(m.detailsContentLines(), "\n")); got != base {
			t.Error("detailsTop=0 must give the content without scrolling")
		}
	})

	t.Run("negative scroll stays at zero", func(t *testing.T) {
		m.detailsTop = -10
		if got := tail.StripANSI(strings.Join(m.detailsContentLines(), "\n")); got != base {
			t.Error("a negative scroll must clamp to 0, not show garbage from the end")
		}
	})

	t.Run("huge scroll stays within", func(t *testing.T) {
		m.detailsTop = 10000
		got := m.detailsContentLines()
		if len(got) != detailsHeight {
			t.Fatalf("an absurd scroll returned %d lines, want %d", len(got), detailsHeight)
		}
	})
}

// The seven tabs are independent switch branches: an unexercised panel is indistinguishable from a panel one column short.
func TestConsoleContentLinesCoversAllSevenTabs(t *testing.T) {
	// A live service with metrics, env, git, events and health: the state in which all seven tabs have something to paint.
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	pid := livePID(t)
	markRunning(&m, path, pid)
	setManifestPort(&m, "tienda-api", 4321)

	m.metrics[path] = &metricsView{CPU: 3.5, RSSKB: 2048, FDs: 12, Threads: 4, At: time.Now()}
	m.envVars[path] = []string{"HOME=/root", "PATH=/usr/bin"}
	m.gitStatus[path] = gitinfo.Status{
		Branch:  "feature/login",
		Changed: []string{" M app.go"},
		Commits: []string{"abc1234 first commit"},
	}
	m.events[path] = []timelineEvent{{At: time.Now(), Kind: "start", Detail: "started", OK: true}}
	m.healthRes[path] = &healthResult{StatusCode: 200, Latency: 12 * time.Millisecond, ContentType: "text/html"}

	for k := tabKind(0); k < tabCount; k++ {
		m.activeTab = k
		got := m.consoleContentLines()
		if len(got) != m.contentH+1 {
			t.Errorf("tab %d: %d lines, want %d (the bar + the content)", k, len(got), m.contentH+1)
		}
		if !strings.Contains(tail.StripANSI(got[0]), tabLabelText(k)) {
			t.Errorf("tab %d: the bar does not name it: %q", k, got[0])
		}
	}
}

// The return is padded and clipped to an exact height on purpose: one line too many pushes the Output box border off screen.
func TestConsoleContentLinesClipsWhenPanelHasMoreLines(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))

	vars := make([]string, 300)
	for i := range vars {
		vars[i] = "VERY_LONG_ENVIRONMENT_VARIABLE_NUMBER_" + strings.Repeat("x", 30)
	}
	m.envVars[path] = vars
	m.activeTab = tabEnv

	got := m.consoleContentLines()
	if len(got) != m.contentH+1 {
		t.Errorf("with 300 variables returned %d lines, want %d", len(got), m.contentH+1)
	}
	for i, l := range got {
		if n := lipglossWidth(l); n > m.rightW {
			t.Fatalf("line %d measures %d cells, want <= %d", i, n, m.rightW)
		}
	}
}

// "follow" and "paused" are opposite states the user must read at a glance, and a stopped service still says "pid -" because omitting it unbalances the bar and says nothing.
func TestTabsBarDistinguishesFollowAndPausedAndPIDPerTab(t *testing.T) {
	m, _ := newTestModel(t)
	m.width, m.height = 160, 30
	m.updateLayout()
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, 4321)

	t.Run("console: follow and paused", func(t *testing.T) {
		m.activeTab = tabConsole
		m.consoleFollow = true
		if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, "follow") || strings.Contains(got, "paused") {
			t.Errorf("with follow = %q", got)
		}
		m.consoleFollow = false
		if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, "paused") || strings.Contains(got, "follow") {
			t.Errorf("with paused = %q", got)
		}
	})

	t.Run("sampling tabs: pid or pid —", func(t *testing.T) {
		for _, tab := range []tabKind{tabThreads, tabMetrics, tabEnv} {
			m.activeTab = tab
			if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, "pid 4321") {
				t.Errorf("tab %d with live service = %q, want the pid", tab, got)
			}
			m.services[path].Meta.Pid = 0
			if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, "pid —") {
				t.Errorf("tab %d with stopped service = %q, want the text pid —", tab, got)
			}
			markRunning(&m, path, 4321)
		}
	})

	t.Run("health: the port", func(t *testing.T) {
		setManifestPort(&m, "tienda-api", 4321)
		m.activeTab = tabHealth
		if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, ":4321") {
			t.Errorf("with declared port = %q, want the port", got)
		}
	})

	t.Run("timeline: the number of events", func(t *testing.T) {
		m.activeTab = tabTimeline
		m.events[path] = []timelineEvent{{At: time.Now()}, {At: time.Now()}, {At: time.Now()}}
		if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, "3 events") {
			t.Errorf("with 3 events = %q", got)
		}
		m.events[path] = nil
		if got := tail.StripANSI(m.tabsBar(160)); !strings.Contains(got, "0 events") {
			t.Errorf("with 0 events = %q: the counter is also information", got)
		}
	})

	t.Run("narrow bar: the label is dropped but the bar is drawn", func(t *testing.T) {
		// Below bar+label width the label is dropped whole: half of it would render "pid 43", a pid that does not exist.
		m.activeTab = tabConsole
		got := m.tabsBar(20)
		if lipglossWidth(got) > 20 {
			t.Errorf("the bar measures %d with a width of 20", lipglossWidth(got))
		}
		if strings.Contains(tail.StripANSI(got), "pid") {
			t.Errorf("a whole pid doesn't fit but one entered: %q", got)
		}
	})
}

// No selected project means no PID to show.
func TestTabsBarWithCursorOnHeaderDoesNotInventLabel(t *testing.T) {
	m, _ := newTestModel(t)
	cursorOn(t, &m, "tienda")

	for _, tab := range []tabKind{tabThreads, tabMetrics, tabEnv, tabHealth, tabTimeline} {
		m.activeTab = tab
		if got := tail.StripANSI(m.tabsBar(160)); strings.Contains(got, "pid ") {
			t.Errorf("tab %d without service = %q: it cannot invent a pid", tab, got)
		}
	}
}

// The message lives at the bottom right because it is the only spot that does not push the two fixed help lines, and it must be clipped or the compositor would wrap it.
func TestKeybindsBoxHasStatusMessageAtBottom(t *testing.T) {
	m, _ := newTestModel(t)

	withoutMessage := tail.StripANSI(m.keybindsBox())
	if len(withoutMessage) == 0 {
		t.Fatal("the keybinds box cannot be empty")
	}

	m.message = "service-started"
	withMessage := tail.StripANSI(m.keybindsBox())
	if !strings.Contains(withMessage, "service-started") {
		t.Errorf("the status message does not appear: %q", withMessage)
	}

	m.message = strings.Repeat("a-very-long-message-", 50)
	clipped := tail.StripANSI(m.keybindsBox())
	for i, l := range strings.Split(clipped, "\n") {
		if lipglossWidth(l) > m.width {
			t.Errorf("line %d of the box measures %d cells with a huge message, want <= %d", i, lipglossWidth(l), m.width)
		}
	}

	m.message = ""
	if a, b := len(strings.Split(withoutMessage, "\n")), len(strings.Split(tail.StripANSI(m.keybindsBox()), "\n")); a != b {
		t.Errorf("the keybinds box changes height depending on the message: %d vs %d", a, b)
	}
}

// Without the label the user cannot tell "vroom is slow because it walks the tree" from "fd is missing": the 2s tick looks the same.
func TestKeybindsBoxDistinguishesWalkFromFD(t *testing.T) {
	m, _ := newTestModel(t)

	m.usedFD = false
	if got := tail.StripANSI(m.keybindsBox()); !strings.Contains(got, "walk") {
		t.Errorf("without fd = %q, want the word walk", got)
	}
	m.usedFD = true
	if got := tail.StripANSI(m.keybindsBox()); !strings.Contains(got, "fd") {
		t.Errorf("with fd = %q, want the word fd", got)
	}
}

// The item under the cursor must always be visible: a list that navigates without showing the highlighted row is the most confusing way for a modal to look broken.
func TestPickerRowsSlidesWindowAndCountsHidden(t *testing.T) {
	m := newStackModel(t)
	items := make([]pickerItem, 30)
	for i := range items {
		items[i] = pickerItem{Name: "task-" + string(rune('a'+i%26)) + "-" + strconv.Itoa(i)}
	}
	m.pickerItems = items

	const maxRows = 8
	for cur := range items {
		m.pickerCursor = cur
		rows, more := m.pickerRows(maxRows, 60)

		if len(rows) != maxRows {
			t.Fatalf("cursor %d: %d visible rows, want %d", cur, len(rows), maxRows)
		}
		if got, want := maxRows+more, len(items); got != want {
			t.Fatalf("cursor %d: %d visible + %d hidden = %d, want %d", cur, len(rows), more, got, want)
		}
		if !strings.Contains(tail.StripANSI(rows[cur%maxRows]), items[cur].Name) &&
			!strings.Contains(tail.StripANSI(strings.Join(rows, "\n")), "▶ "+items[cur].Name) {
			t.Errorf("cursor %d: the highlighted element is not in the window: %q", cur, rows)
		}
		body := tail.StripANSI(strings.Join(rows, "\n"))
		if n := strings.Count(body, "▶"); n != 1 {
			t.Errorf("cursor %d: %d arrows in the window, want 1", cur, n)
		}
	}

	m.pickerCursor = 0
	rows, more := m.pickerRows(40, 60)
	if more != 0 || len(rows) != len(items) {
		t.Errorf("with everything inside: %d rows and %d hidden, want %d and 0", len(rows), more, len(items))
	}

	if rows, more := m.pickerRows(0, 60); len(rows) != 0 || more != len(items) {
		t.Errorf("with maxRows 0: %d rows and %d hidden, want 0 and %d", len(rows), more, len(items))
	}
}

// The 28 floor matters: below it the modal box is narrower than its own text and the compositor wraps every row into an unreadable block.
func TestPickerTitleAndWidth(t *testing.T) {
	m := newStackModel(t)

	cursorOn(t, &m, "tienda-api")
	if got := m.pickerTitle(); got != "tienda-api" {
		t.Errorf("with a project = %q, want tienda-api", got)
	}

	cursorOn(t, &m, "front") // a stack, not a project
	if got := m.pickerTitle(); got != "?" {
		t.Errorf("without project = %q, want a question mark", got)
	}

	m.width = 200
	m.pickerItems = []pickerItem{{Name: "short"}, {Name: "a-task-with-the-longest-name-in-batch"}}
	if w := m.pickerInnerW(); w < 28 {
		t.Errorf("the modal width is %d, want >= 28: below that the compositor wraps", w)
	}
	m.pickerItems = []pickerItem{{Name: "x", Description: strings.Repeat("d", 100)}}
	if w := m.pickerInnerW(); w > m.width-14 {
		t.Errorf("the modal width is %d with a screen of %d: it cannot exceed the screen", w, m.width)
	}
	m.pickerItems = nil
	if w := m.pickerInnerW(); w < 28 {
		t.Errorf("with an empty list the width is %d, want >= 28", w)
	}
}

// The help line is the contract, not decoration: this is the only modal in the program and a lost user has no other way out.
func TestPickerBoxDrawsCompleteModal(t *testing.T) {
	m := newStackModel(t)
	m.pickerItems = []pickerItem{
		{Name: "build", Description: "builds the project"},
		{Name: "test", Description: "runs the tests"},
	}
	m.pickerCursor = 0

	body := tail.StripANSI(m.pickerBox())
	for _, want := range []string{"tasks", "build", "test", "j/k select", "esc close"} {
		if !strings.Contains(body, want) {
			t.Errorf("the modal does not have %q:\n%s", want, body)
		}
	}

	m.pickerKind = pickerAgents
	m.pickerItems = []pickerItem{{Name: "opencode"}, {Name: "claude"}}
	if body := tail.StripANSI(m.pickerBox()); !strings.Contains(body, "choose an agent") {
		t.Errorf("the agents modal has its own title:\n%s", body)
	}

	m.pickerItems = make([]pickerItem, 200)
	for i := range m.pickerItems {
		m.pickerItems[i] = pickerItem{Name: "t" + strconv.Itoa(i)}
	}
	if body := tail.StripANSI(m.pickerBox()); !strings.Contains(body, "more") {
		t.Errorf("with 200 items it must say how many are not visible:\n%s", body)
	}
}

// Two ceilings exist and only the screen one is enforced in production.
func TestPickerMaxRowsNeverExceedsScreenOrList(t *testing.T) {
	m := newStackModel(t)

	m.pickerItems = make([]pickerItem, 3)
	if got := m.pickerMaxRows(); got != 3 {
		t.Errorf("with 3 items and plenty of room = %d, want 3", got)
	}
	m.pickerItems = make([]pickerItem, 500)
	if got, want := m.pickerMaxRows(), m.bodyH-7; got != want {
		t.Errorf("with 500 items = %d, want %d (the screen height rules)", got, want)
	}

	m.bodyH = 1
	m.pickerItems = make([]pickerItem, 500)
	if got := m.pickerMaxRows(); got < 3 {
		t.Errorf("with a one-line screen = %d, want >= 3", got)
	}

	m.pickerItems = nil
	m.bodyH = 30
	if got := m.pickerMaxRows(); got != 0 {
		t.Errorf("without items = %d, want 0", got)
	}
}

// The overlay clips the base instead of erasing it, which is what makes a modal a modal and not a screen change.
func TestOverlayCentersBoxAndPreservesSurroundings(t *testing.T) {
	base := strings.Repeat("base-line\n", 10)
	box := strings.Repeat("X", 3)

	got := overlay(base, box, 20, 10)

	if !strings.Contains(got, "XXX") {
		t.Error("the box was not drawn")
	}
	if n := strings.Count(got, "base-line"); n < 6 {
		t.Errorf("only %d base lines remain out of 10: the overlay cannot erase content", n)
	}

	tall := strings.Repeat("X", 20)
	if got := overlay(base, tall, 20, 3); len(strings.Split(got, "\n")) != len(strings.Split(base, "\n")) {
		t.Errorf("a box taller than the base changed the height: %d vs %d", len(strings.Split(got, "\n")), len(strings.Split(base, "\n")))
	}

	// MEASURED: overlay does NOT clip the box to the screen; it clips the surrounding base content, so whoever sizes the box must make it fit (askInnerW is where that broke).
	if got = overlay("short", strings.Repeat("W", 100), 20, 1); lipglossWidth(got) != 100 {
		t.Errorf("the box measures %d, want 100: overlay does not clip it, the caller must make it fit", lipglossWidth(got))
	}
}

// The modal width decides whether the tree and the boxes still fit underneath: wider than the terminal every line wraps and the layout comes apart, so the 28 floor is kept even if it overflows.
func TestAskInnerWNeverOverflowsScreen(t *testing.T) {
	for _, width := range []int{40, 60, 80, 86, 100, 124, 200, 300} {
		inner := askInnerW(width)
		if total := inner + boxFrame; total > width {
			t.Errorf("width=%d: the modal measures %d inner width, want <= %d", width, inner, width-boxFrame)
		}
		if inner < 28 {
			t.Errorf("width=%d: inner width %d, want >= 28: below that the textarea is not usable", width, inner)
		}
	}

	if got := askInnerW(100); got != 86 {
		t.Errorf("askInnerW(100) = %d, want 86: on a normal screen the remaining space rules", got)
	}
	if got := askInnerW(300); got != 110 {
		t.Errorf("askInnerW(300) = %d, want the cap of 110", got)
	}
	if got := askInnerW(20); got != 28 {
		t.Errorf("askInnerW(20) = %d, want 28: the floor survives the narrow screen", got)
	}
}

// An empty path would store under key "" and that event would then show up on every service reading m.events[""].
func TestAddEventIgnoresEmptyPathAndTrimsList(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-api")

	m.addEvent("", "start", "no service", time.Second, true)
	if len(m.events) != 0 {
		t.Errorf("an event without a service was stored: %v", m.events)
	}

	for i := range maxTimeline + 50 {
		m.addEvent(path, "start", strconv.Itoa(i), time.Second, true)
	}
	if got := len(m.events[path]); got != maxTimeline {
		t.Errorf("the list has %d events, want %d", got, maxTimeline)
	}
	last := m.events[path][maxTimeline-1]
	if last.Detail != strconv.Itoa(maxTimeline+49) {
		t.Errorf("the last event is %q, want the most recent (%d)", last.Detail, maxTimeline+49)
	}
}

// With an unresolved port, showing and probing the declared one would point at a port that may belong to another worktree.
func TestHealthLinesDistinguishesNoPortFromUnresolvedPort(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	t.Run("no port declared", func(t *testing.T) {
		m.services[path].Meta = state.Meta{State: state.StateStopped}
		if p := m.projectByPath(path); p != nil {
			p.Manifest.Port = 0
		}
		if got := tail.StripANSI(strings.Join(m.healthLines(80), "\n")); !strings.Contains(got, "no port configured") {
			t.Errorf("= %q, want it to ask to declare a port", got)
		}
	})

	t.Run("unresolved port", func(t *testing.T) {
		setManifestPort(&m, "tienda-api", 4321)
		m.services[path].Meta = state.Meta{State: state.StatePortUnresolved, Port: 4321}
		if got := tail.StripANSI(strings.Join(m.healthLines(80), "\n")); !strings.Contains(got, "unresolved") {
			t.Errorf("= %q, want it to say the port was not resolved", got)
		}
	})

	t.Run("configured and live without probe yet", func(t *testing.T) {
		setManifestPort(&m, "tienda-api", 4321)
		markRunning(&m, path, livePID(t))
		m.healthRes[path] = nil
		if got := tail.StripANSI(strings.Join(m.healthLines(80), "\n")); !strings.Contains(got, "probing") {
			t.Errorf("= %q, want it to say the probe is in progress", got)
		}
	})

	t.Run("failed probe shows the reason and the URL", func(t *testing.T) {
		markRunning(&m, path, livePID(t))
		m.healthRes[path] = &healthResult{Err: "connection refused"}
		body := tail.StripANSI(strings.Join(m.healthLines(80), "\n"))
		if !strings.Contains(body, "connection refused") {
			t.Errorf("a failed probe must say why: %q", body)
		}
		if !strings.Contains(body, "url:") {
			t.Errorf("a failed probe must show the URL to test it manually: %q", body)
		}
	})
}

// The three tiers are visibly different: 900 MB shown as "943718.4 KB" is unreadable, and so is 2 GB shown as "2048.0 KB".
func TestHumanKBUsesCorrectUnitAndDoesNotExceedKB(t *testing.T) {
	tests := []struct {
		kb   int64
		want string
	}{
		{0, "0 KB"},
		{1, "1 KB"},
		{1023, "1023 KB"},
		{1024, "1.0 MB"},
		{1536, "1.5 MB"},
		{1024*1024 - 1, "1024.0 MB"},
		// The input is in KiB, so a GB is 1024*1024 KiB.
		{1024 * 1024, "1.0 GB"},
		{3 * 1024 * 1024, "3.0 GB"},
		{1024*1024*1024 - 1, "1024.0 GB"},
	}
	for _, tt := range tests {
		if got := humanKB(tt.kb); got != tt.want {
			t.Errorf("humanKB(%d) = %q, want %q", tt.kb, got, tt.want)
		}
	}
}
