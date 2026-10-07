package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/orchestrate"
	"vroom/internal/portless"
	"vroom/internal/state"
	"vroom/internal/tail"
)

func cursorOn(t *testing.T, m *Model, want string) {
	t.Helper()
	rows, _ := m.treeLines()
	for i, r := range rows {
		if strings.Contains(r, want) {
			m.cursor = i
			return
		}
	}
	t.Fatalf("no tree row contains %q: %v", want, rows)
}

// The three reasons for having no rows are distinct: group node, no manifest, stopped; conflating them would read as broken sampling.
func TestThreadsLinesOnHeaderInvitesToPick(t *testing.T) {
	m, _ := newTestModel(t)
	cursorOn(t, &m, "tienda")

	lines := m.threadsLines(m.rightW, m.contentH)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want a single message", lines)
	}
	if !strings.Contains(lines[0], "pick a service") {
		t.Errorf("on a group node = %q, want an invitation to pick a service", lines[0])
	}
}

// nil, not an empty line: the panel must tell "nothing to show" apart from "a message exists".
func TestThreadsLinesWithoutProjectAndHeaderReturnsNothing(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = -1 // out of range: selectedItem fails and there is no header

	if lines := m.threadsLines(m.rightW, m.contentH); lines != nil {
		t.Errorf("lines = %v with cursor outside the tree, want nil: a []string{\"\"} would draw a blank line", lines)
	}
}

// The flag is flipped on the ALREADY BUILT tree on purpose: a hand-injected project could be dropped by buildTree's grouping rules and the test would prove nothing.
func TestThreadsLinesDistinguishesNoManifestFromStopped(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "suelto")
	path := pathOfSelected(t, m)

	m.services[path] = &ServiceState{Status: statusStopped}
	if p := m.projectByPath(path); p != nil {
		p.Configured = false
	}
	it, ok := m.selectedItem()
	if !ok {
		t.Fatal("cursor is no longer on the project")
	}
	it.project.Configured = false
	m.tree[m.cursor] = it

	lines := m.threadsLines(m.rightW, m.contentH)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want a single message", lines)
	}
	if !strings.Contains(lines[0], "No manifest") {
		t.Errorf("line = %q, want it to ask to create a .vroom.toml", lines[0])
	}
	if strings.Contains(lines[0], "not running") {
		t.Errorf("line = %q: confusing \"no manifest\" with \"stopped\" sends the user to press start on something that won't start", lines[0])
	}
}

func TestThreadsLinesSaysItIsTakingFirstSample(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, 4242)

	lines := m.threadsLines(m.rightW, m.contentH)
	if len(lines) != 1 || !strings.Contains(lines[0], "sampling") {
		t.Errorf("lines = %v, want a sampling-in-progress notice", lines)
	}
}

func TestThreadsLinesCountsThreadsThatDoNotFit(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, 4242)

	rows := make([]threadRow, 40)
	for i := range rows {
		rows[i] = threadRow{TID: i + 1, Name: "thread", State: "S", CPU: float64(i)}
	}
	m.threads[path] = rows

	lines := m.threadsLines(80, 6)
	body := strings.Join(lines, "\n")

	if !strings.Contains(body, "NAME") {
		t.Errorf("the table must include its header: %q", body)
	}
	if !strings.Contains(body, "more threads") {
		t.Errorf("with 40 threads and 6 rows, must say how many are left: %q", body)
	}
	if len(lines) > 6 {
		t.Errorf("the table returned %d lines for a height of 6", len(lines))
	}
	if tall := m.threadsLines(200, 60); strings.Contains(strings.Join(tall, "\n"), "more threads") {
		t.Error("with the whole table inside, saying \"more threads\" is noise")
	}
}

// The name column keeps a minimum width even when the panel cannot afford it, or the header degrades into a wall of format verbs.
func TestThreadsLinesSurvivesVeryNarrowPanel(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, 4242)
	m.threads[path] = []threadRow{
		{TID: 1, Name: "a-very-long-thread-name", State: "R", CPU: 12.5},
	}

	for _, w := range []int{0, 1, 5, 12, 21, 40} {
		lines := m.threadsLines(w, 10)
		if len(lines) < 2 {
			t.Fatalf("w=%d: lines = %v, want header + at least one row", w, lines)
		}
		if !strings.Contains(lines[0], "NAME") {
			t.Errorf("w=%d: header = %q", w, lines[0])
		}
		// MEASURED: at height 1 the panel emits TWO lines (header plus "+N more"): overflowing a 2-row panel beats showing the label alone.
		if got := len(m.threadsLines(w, 1)); got != 2 {
			t.Errorf("w=%d, h=1: %d lines, want 2 (header + \"more\"): with one row, even the label alone doesn't fit", w, got)
		}
		if got := len(m.threadsLines(w, 5)); got > 5 {
			t.Errorf("w=%d, h=5: %d lines, want <= 5", w, got)
		}
	}
}

// This panel answers "why did stop kill that?": a stack name alone leaves the scope of the pressed key to guesswork.
func TestStackDetailsLinesExplainsStack(t *testing.T) {
	m := newStackModel(t)
	cursorOn(t, &m, "front")

	stack := m.selectedStack()
	if stack == nil {
		t.Fatalf("cursor did not land on a stack: %d", m.selectedItemKind())
	}
	body := tail.StripANSI(strings.Join(m.stackDetailsLines(stack, m.rightW), "\n"))

	for _, want := range []string{
		"front",
		"orchestration stack",
		"stages:",
		"services:",
		"group:",
		"tienda-web",
		"tienda-api",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the stack panel does not mention %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "services: 2 (0 running)") {
		t.Errorf("service count = %q, want both from the stack and zero running:\n%s", body, body)
	}
}

// The header verdict is what makes the user press stop or start, so showing both would be worse than showing neither.
func TestStackDetailsLinesDistinguishesRunningFromStopped(t *testing.T) {
	t.Run("stopped", func(t *testing.T) {
		m := newStackModel(t)
		cursorOn(t, &m, "front")
		stack := m.selectedStack()
		body := tail.StripANSI(strings.Join(m.stackDetailsLines(stack, m.rightW), "\n"))
		if !strings.Contains(body, "stopped") {
			t.Errorf("a stack with no live services must say stopped:\n%s", body)
		}
		if strings.Contains(body, "running ●") {
			t.Errorf("cannot say running with nothing alive:\n%s", body)
		}
	})

	t.Run("running", func(t *testing.T) {
		m := newStackModel(t)
		cursorOn(t, &m, "front")
		stack := m.selectedStack()
		markRunning(&m, projectPath(t, m, "tienda-api"), livePID(t))
		body := tail.StripANSI(strings.Join(m.stackDetailsLines(stack, m.rightW), "\n"))
		if !strings.Contains(body, "running") {
			t.Errorf("with a live service the header must say running:\n%s", body)
		}
		if !strings.Contains(body, "services: 2 (1 running)") {
			t.Errorf("count = %q, want 2 with 1 running:\n%s", body, body)
		}
	})
}

// Same rule as the engine and the CLI: an ambiguous name is never resolved to the first match.
func TestStackDetailsLinesWarnsOfAmbiguousNameWithoutBreakingPanel(t *testing.T) {
	m := newStackModel(t)

	dup := m.projectByPath(projectPath(t, m, "tienda-api"))
	clone := *dup
	clone.Path = filepath.Join(t.TempDir(), "other-api")
	clone.Name = "tienda-api"
	m.projects = append(m.projects, clone)
	m.tree = m.buildTree()

	stack := &orchestrate.Stack{
		Name:         "ambiguous",
		PrimaryGroup: "tienda",
		Stages: []orchestrate.Stage{
			{Name: "e1", Services: []string{"tienda-api", "does-not-exist"}},
		},
	}
	body := tail.StripANSI(strings.Join(m.stackDetailsLines(stack, m.rightW), "\n"))

	if !strings.Contains(body, "does-not-exist") {
		t.Errorf("the unresolvable service must still appear:\n%s", body)
	}
	if !strings.Contains(body, "⚠") {
		t.Errorf("a service that doesn't resolve must bring a warning:\n%s", body)
	}
	if !strings.Contains(body, "ambiguous") || !strings.Contains(body, "stages:") {
		t.Errorf("a warning cannot empty the panel:\n%s", body)
	}
}

// Showing the port on the service's own line is what lets the panel be compared with the tree without memorising the port table.
func TestStackDetailsLinesShowsPortOfEachService(t *testing.T) {
	m := newStackModel(t)
	// tienda-api's manifest declares 8081
	path := projectPath(t, m, "tienda-api")
	markRunning(&m, path, livePID(t))

	stack := &orchestrate.Stack{
		Name:         "front",
		PrimaryGroup: "tienda",
		Stages:       []orchestrate.Stage{{Name: "e", Services: []string{"tienda-api", "tienda-web"}}},
	}
	body := tail.StripANSI(strings.Join(m.stackDetailsLines(stack, m.rightW), "\n"))

	if !strings.Contains(body, "8081") {
		t.Errorf("the service's declared port must be on its line:\n%s", body)
	}
	if strings.Contains(body, "999999") {
		t.Errorf("a non-existent port cannot appear:\n%s", body)
	}
}

// allDetailsLines must pick the stack by the cursor being on it, not by name: picking by name would show "front: 2 services" for another service.
func TestStackDetailsLinesSelectedByCursorOnStackNotByName(t *testing.T) {
	m := newStackModel(t)

	m = moveCursorTo(t, m, "tienda-api")
	body := tail.StripANSI(strings.Join(m.detailsLines(m.rightW), "\n"))
	if strings.Contains(body, "[stack]") {
		t.Errorf("with a project selected, the panel cannot be the stack's:\n%s", body)
	}
	if !strings.Contains(body, "path:") {
		t.Errorf("with a project selected it must be its detail panel:\n%s", body)
	}

	cursorOn(t, &m, "front")
	body = tail.StripANSI(strings.Join(m.detailsLines(m.rightW), "\n"))
	if !strings.Contains(body, "[stack]") {
		t.Errorf("with the cursor on the stack it must be its panel:\n%s", body)
	}
}

// A blank line would leave the user unable to tell a failure from a plain lack of selection.
func TestDetailsLinesWithNothingSelectedSaysSo(t *testing.T) {
	m, _ := newTestModel(t)
	m.tree = nil

	body := tail.StripANSI(strings.Join(m.detailsLines(m.rightW), "\n"))
	if !strings.Contains(body, "No project selected") {
		t.Errorf("with no selection the panel must say so:\n%s", body)
	}
}

// Rows are clipped first: clipping columns of a line that is never drawn is wasted, and in the other order an ANSI line could count escape bytes as width and split a rune.
func TestClipLinesClipsRowsAndColumns(t *testing.T) {
	lines := []string{"one", "two", "three", "four"}

	got := clipLines(lines, 2, 40)
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("with h=2 = %v, want the first two", got)
	}
	// clipLines clips in place on purpose; the result is what matters.
	if h, w := 10, 3; len(clipLines([]string{"abcdefghij"}, h, w)) != 1 {
		t.Error("a single line with h=10 must stay as one")
	}
	wide := clipLines([]string{"abcdefghij"}, 10, 4)
	if len(tail.StripANSI(wide[0])) > 4 {
		t.Errorf("clipLines did not clip columns: %q (%d)", wide[0], len(tail.StripANSI(wide[0])))
	}
}

// A long label is left overflowing on purpose: truncating it would break the alignment of the columns behind it.
func TestPadPadsToRunesAndDoesNotTouchWhatDoesNotFit(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"port:", 10, "port:     "},
		{"", 4, "    "},
		// MEASURED: "exacta10" is 8 characters, so it is padded anyway; the case name was misleading.
		{"exacta10", 10, "exacta10  "},
		{"too-long", 4, "too-long"},
	}
	for _, tt := range tests {
		if got := pad(tt.in, tt.n); got != tt.want {
			t.Errorf("pad(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}

	for _, s := range []string{"url:", "path:", "port:"} {
		if n := len(pad(s, 10)); n != 10 {
			t.Errorf("pad(%q, 10) measures %d bytes, want 10: padding must count runes", s, n)
		}
	}
	got := pad("add", 10)
	if len([]rune(got)) != 10 {
		t.Errorf("pad with multibyte measures %d runes, want 10: %q", len([]rune(got)), got)
	}
}

// RouteOwned=false means the handle already belonged to someone else, and the meta keeps the name so reconciliation still knows where to look.
func TestReleaseRouteDoesNotRemoveWhatWasNotYours(t *testing.T) {
	meta := &state.Meta{RouteName: "vroom-test-not-mine", RouteOwned: false}
	releaseRoute(meta)

	if meta.RouteOwned {
		t.Error("RouteOwned = true after removing a route that was not ours")
	}
	if meta.RouteName != "vroom-test-not-mine" {
		t.Errorf("RouteName = %q: another's route cannot disappear from meta", meta.RouteName)
	}
}

// Both halves must hold at once: removing without revoking leaves ownership on a route that is gone, revoking without removing leaves a zombie route the next service cannot take.
func TestReleaseRouteRevokesOwnershipWhenRemovalTookEffect(t *testing.T) {
	rec := &recordingReleaser{}
	installRouteStub(t, rec)

	meta := &state.Meta{RouteName: "vroom-test-mine", RouteOwned: true}
	releaseRoute(meta)

	if len(rec.removed) != 1 || rec.removed[0] != "vroom-test-mine" {
		t.Errorf("removed = %v, want exactly [vroom-test-mine]", rec.removed)
	}
	if meta.RouteOwned {
		t.Error("the removal took effect but ownership is still granted: the route becomes a zombie")
	}
}

// Failing closed: if the route is already gone (someone else removed it, or it was never taken) revoking would lie about the state, while keeping it lets reconciliation retry.
func TestReleaseRouteDoesNotRevokeIfRemovalHadNoEffect(t *testing.T) {
	// A releaser that reports "was not there", which is what Remove returns on error.
	t.Cleanup(func() {
		tuiReleaseStub = nil
		routeStubInstalled = false
	})
	tuiReleaseStub = func(name string) error { return os.ErrNotExist }
	routeStubInstalled = true

	meta := &state.Meta{RouteName: "vroom-test-already-gone", RouteOwned: true}
	releaseRoute(meta)

	if !meta.RouteOwned {
		t.Error("the removal had no effect but ownership was revoked: reconciliation loses the trail")
	}
}

// The flag-plus-nil combination is the check order: returning a Releaser wrapped around a nil func would SIGSEGV on the first Remove.
func TestTuiRouteReleaserChoosesStubOrInertFallback(t *testing.T) {
	t.Run("stub installed and with pointer: it's the stub", func(t *testing.T) {
		rec := &recordingReleaser{}
		installRouteStub(t, rec)

		got := tuiRouteReleaser()
		if got == nil {
			t.Fatal("with the stub installed, the stub must be returned, not nil: nil is the REAL client")
		}
		if err := got.RemoveAbsent("vroom-test-x"); err != nil {
			t.Errorf("the returned releaser is not the test's: %v", err)
		}
		if len(rec.removed) != 1 {
			t.Errorf("the call did not reach the test's stub: %v", rec.removed)
		}
	})

	t.Run("flag set but pointer nil: falls back to inert and doesn't crash", func(t *testing.T) {
		// This is what a test that sets the flag and forgets the stub does; a nil-func Remove would crash in production under a badly written test.
		t.Cleanup(func() {
			tuiReleaseStub = nil
			routeStubInstalled = false
		})
		routeStubInstalled = true
		tuiReleaseStub = nil

		got := tuiRouteReleaser()
		if got == nil {
			t.Fatal("a half-set flag must fall back to inert, not nil: nil means real client")
		}
		if err := got.RemoveAbsent("vroom-anyone"); err != nil {
			t.Errorf("the inert returned error %v: its job is to do NOTHING", err)
		}
	})

	t.Run("no flag in a test binary: the inert", func(t *testing.T) {
		// No test can reach the real-client branch because IsTestBinary() is always true inside a .test, and returning nil there would make a test connect to the real proxy.
		if !portless.IsTestBinary() {
			t.Skip("this binary is not a test binary: the case does not apply")
		}
		if got := tuiRouteReleaser(); got == nil {
			t.Error("without a stub inside a test binary, the inert must be returned, not nil")
		}
	})
}
