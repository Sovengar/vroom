package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/orchestrate"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

func TestScrollDetailsNoBajaDeCero(t *testing.T) {
	m, _ := newTestModel(t)
	m.detailsTop = 0

	m.scrollDetails(-detailsHeight)
	if m.detailsTop != 0 {
		t.Errorf("detailsTop = %d after scrolling all the way up, want 0: the clamp is what prevents reading above the buffer", m.detailsTop)
	}

	m.scrollDetails(3)
	if m.detailsTop != 3 {
		t.Errorf("detailsTop = %d, want 3", m.detailsTop)
	}
	m.scrollDetails(-1)
	if m.detailsTop != 2 {
		t.Errorf("detailsTop = %d, want 2", m.detailsTop)
	}
	m.scrollDetails(-9999)
	if m.detailsTop != 0 {
		t.Errorf("detailsTop = %d, want 0", m.detailsTop)
	}
}

// The cursor's node kind picks the scroll target: a project scrolls the console, a group or stack scrolls the details panel.
func TestPgUpPgDownEnUnNodoDesplazanElPanelDeDetalles(t *testing.T) {
	m, _ := newTestModel(t)
	m.detailsShown = true

	m.cursor = findPrimary(m, "tienda")

	m2, _ := press(m, "pgup")
	got := m2
	if got.detailsTop != 0 {
		t.Errorf("pgup on a node already at the top: detailsTop = %d, want 0 (the clamp)", got.detailsTop)
	}
	if !got.consoleFollow {
		t.Error("pgup on a node should not pause console follow: the scroll went to the panel")
	}

	m3, _ := press(got, "pgdown")
	down := m3
	if down.detailsTop != detailsHeight {
		t.Errorf("detailsTop = %d after pgdown on a node, want %d", down.detailsTop, detailsHeight)
	}

	m4 := moveCursorTo(t, down, "tienda-api")
	m5, _ := press(m4, "pgup")
	proj := m5
	if proj.detailsTop != down.detailsTop {
		t.Errorf("pgup with a selected project moved the panel: detailsTop %d -> %d", down.detailsTop, proj.detailsTop)
	}
	if proj.consoleFollow {
		t.Error("pgup with a selected project should pause console follow")
	}
}

func TestRefreshBatchPideEstadoGitYLaPestanaActiva(t *testing.T) {
	m, _ := newTestModel(t)
	m.activeTab = tabConsole

	cmd := m.refreshBatch()
	if cmd == nil {
		t.Fatal("refreshBatch returned nil: manual refresh would do nothing")
	}

	msgs := collectBatch(t, cmd)
	var sawRefresh bool
	for _, msg := range msgs {
		if _, ok := msg.(refreshedMsg); ok {
			sawRefresh = true
		}
	}
	if !sawRefresh {
		t.Error("refreshBatch did not request a refreshedMsg: service state would become stale")
	}

	// Tail is requested regardless of liveness: a just-dead service still has bytes, and filtering by liveness would blank the last output the user came for.
	live := moveCursorTo(t, m, "tienda-api")
	liveMsgs := collectBatch(t, live.refreshBatch())
	var sawLiveRefresh, sawTail bool
	for _, msg := range liveMsgs {
		switch msg.(type) {
		case refreshedMsg:
			sawLiveRefresh = true
		case consoleDeltaMsg:
			sawTail = true
		}
	}
	if !sawLiveRefresh {
		t.Error("without refreshedMsg service state becomes stale")
	}
	if !sawTail {
		t.Error("with the console tab the tail was not requested: the console would freeze even if the service writes")
	}

	none := m
	none.cursor = findPrimary(none, "tienda") // a header is not a project
	if cmd := none.refreshTab(); cmd != nil {
		if msgs := collectBatch(t, cmd); len(msgs) > 0 {
			t.Errorf("on a group header data was requested: %d commands", len(msgs))
		}
	}
}

func TestToggleStackSinEngineLoDice(t *testing.T) {
	m, _ := newTestModel(t)
	if m.engine != nil {
		t.Skip("this model has an engine: the no-engine case cannot be triggered")
	}

	out, cmd := m.toggleStack(&orchestrate.Stack{Name: "front"})
	got := out.(Model)
	if cmd != nil {
		t.Error("without engine no Cmd should be returned")
	}
	if !strings.Contains(got.message, "orchestration engine not available") {
		t.Errorf("the message does not explain why nothing happens: %q", got.message)
	}
}

func TestToggleComposersSinEngineLoDice(t *testing.T) {
	m, _ := newTestModel(t)
	if m.engine != nil {
		t.Skip("this model has an engine: the no-engine case cannot be triggered")
	}
	out, cmd := m.toggleComposers("tienda")
	got := out.(Model)
	if cmd != nil {
		t.Error("without engine no Cmd should be returned")
	}
	if !strings.Contains(got.message, "orchestration engine not available") {
		t.Errorf("the message does not explain why: %q", got.message)
	}
}

func TestStacksForPrimaryFiltraPorGrupoYToleraAusenciaDeCompose(t *testing.T) {
	m, _ := newTestModel(t)

	// nil means no compose file, and the TUI reads nil as "do not paint the Composers section".
	if got := m.stacksForPrimary("tienda"); got != nil {
		t.Errorf("without compose file gave %v, want nil", namesOfStacks(got))
	}

	// Each stack carries its own PrimaryGroup, which overrides the compose file's global group.
	m.composeFile = &orchestrate.ComposeFile{
		PrimaryGroup: "tienda",
		Stacks: []orchestrate.Stack{
			{Name: "front", PrimaryGroup: "tienda"},
			{Name: "otro", PrimaryGroup: "otra-cosa"},
		},
	}
	got := m.stacksForPrimary("tienda")
	if len(got) != 1 || got[0].Name != "front" {
		t.Errorf("stacksForPrimary(tienda) = %v, want only front", namesOfStacks(got))
	}
	if got := m.stacksForPrimary("no-existe"); len(got) != 0 {
		t.Errorf("a primary without stacks gave %v, want empty", namesOfStacks(got))
	}
}

func TestMarkStackStoppingSoloMarcaLoQueEstaVivo(t *testing.T) {
	m, _ := newTestModel(t)
	web := projectPath(t, m, "tienda-web")
	api := projectPath(t, m, "tienda-api")

	markRunning(&m, web, 4242)
	// api is left stopped on purpose and "fantasma" is absent from the workspace: neither may end up stopping.

	// Stack services are named by manifest name, the key LookupService resolves; directory names would make every stack look empty and fail silently.
	stack := &orchestrate.Stack{
		Name:         "front",
		PrimaryGroup: "tienda",
		Stages: []orchestrate.Stage{{Name: "front", Services: []string{
			"tienda-web", "tienda-api", "fantasma",
		}}},
	}
	m.markStackStopping(stack)

	if got := m.services[web].Status; got != statusStopping {
		t.Errorf("the live service was left in %q, want stopping", got)
	}
	if got := m.services[api].Status; got == statusStopping {
		t.Errorf("an already stopped service was left in stopping: it would look like something stopped is being stopped")
	}

	m.markStackStopping(stack)
	if got := m.services[web].Status; got != statusStopping {
		t.Errorf("the second pass changed the state to %q", got)
	}
}

func TestMarkStackStoppingConServicioInexistenteNoRompe(t *testing.T) {
	m, _ := newTestModel(t)
	stack := &orchestrate.Stack{
		Name:   "front",
		Stages: []orchestrate.Stage{{Name: "front", Services: []string{"no-existe-nunca"}}},
	}
	m.markStackStopping(stack)
	for path, sv := range m.services {
		if sv.Status == statusStopping {
			t.Errorf("%s was left in stopping without any live service requesting it", path)
		}
	}
}

func namesOfStacks(stacks []orchestrate.Stack) []string {
	out := make([]string, 0, len(stacks))
	for _, s := range stacks {
		out = append(out, s.Name)
	}
	return out
}

// Guards the fixture: if markRunning did not satisfy isRunning, every stopping test above would silently exercise the stopped path.
func TestHarnessMarkRunningDejaElServicioVivo(t *testing.T) {
	m, _ := newTestModel(t)
	path := projectPath(t, m, "tienda-web")
	markRunning(&m, path, 4242)

	if !m.isRunning(path) {
		t.Error("markRunning did not leave the service alive: the stopping tests would be testing something else")
	}
	if got := m.services[path].Status; got != statusRunning {
		t.Errorf("Status = %q, want running", got)
	}
	var _ process.Manager = &stubManager{}
	var _ scanner.Project
	var _ state.Meta
	var _ tea.Cmd
}
