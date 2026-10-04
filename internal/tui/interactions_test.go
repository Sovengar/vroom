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
		t.Errorf("detailsTop = %d tras subir del todo, want 0: el clamp es lo que evita leer por encima del buffer", m.detailsTop)
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
		t.Errorf("pgup en un nodo ya estaba arriba: detailsTop = %d, want 0 (el clamp)", got.detailsTop)
	}
	if !got.consoleFollow {
		t.Error("pgup sobre un nodo no debe pausar el follow de la consola: el scroll fue al panel")
	}

	m3, _ := press(got, "pgdown")
	down := m3
	if down.detailsTop != detailsHeight {
		t.Errorf("detailsTop = %d tras pgdown en un nodo, want %d", down.detailsTop, detailsHeight)
	}

	m4 := moveCursorTo(t, down, "tienda-api")
	m5, _ := press(m4, "pgup")
	proj := m5
	if proj.detailsTop != down.detailsTop {
		t.Errorf("pgup con un proyecto seleccionado movió el panel: detailsTop %d -> %d", down.detailsTop, proj.detailsTop)
	}
	if proj.consoleFollow {
		t.Error("pgup con un proyecto seleccionado debería pausar el follow de la consola")
	}
}

func TestRefreshBatchPideEstadoGitYLaPestanaActiva(t *testing.T) {
	m, _ := newTestModel(t)
	m.activeTab = tabConsole

	cmd := m.refreshBatch()
	if cmd == nil {
		t.Fatal("refreshBatch devolvió nil: el refresh manual no haría nada")
	}

	msgs := collectBatch(t, cmd)
	var sawRefresh bool
	for _, msg := range msgs {
		if _, ok := msg.(refreshedMsg); ok {
			sawRefresh = true
		}
	}
	if !sawRefresh {
		t.Error("refreshBatch no pidió un refreshedMsg: el estado de los servicios quedaría rancio")
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
		t.Error("sin refreshedMsg el estado de los servicios queda rancio")
	}
	if !sawTail {
		t.Error("con la pestaña de consola no se pidió el tail: la consola se congelaría aunque el servicio escriba")
	}

	none := m
	none.cursor = findPrimary(none, "tienda") // un header no es un proyecto
	if cmd := none.refreshTab(); cmd != nil {
		if msgs := collectBatch(t, cmd); len(msgs) > 0 {
			t.Errorf("sobre un header de grupo se pidieron datos: %d comandos", len(msgs))
		}
	}
}

func TestToggleStackSinEngineLoDice(t *testing.T) {
	m, _ := newTestModel(t)
	if m.engine != nil {
		t.Skip("este modelo tiene engine: el caso sin engine no se puede provocar")
	}

	out, cmd := m.toggleStack(&orchestrate.Stack{Name: "front"})
	got := out.(Model)
	if cmd != nil {
		t.Error("sin engine no debería volver ningún Cmd")
	}
	if !strings.Contains(got.message, "orchestration engine not available") {
		t.Errorf("el aviso no explica por qué no pasa nada: %q", got.message)
	}
}

func TestToggleComposersSinEngineLoDice(t *testing.T) {
	m, _ := newTestModel(t)
	if m.engine != nil {
		t.Skip("este modelo tiene engine: el caso sin engine no se puede provocar")
	}
	out, cmd := m.toggleComposers("tienda")
	got := out.(Model)
	if cmd != nil {
		t.Error("sin engine no debería volver ningún Cmd")
	}
	if !strings.Contains(got.message, "orchestration engine not available") {
		t.Errorf("el aviso no explica por qué: %q", got.message)
	}
}

func TestStacksForPrimaryFiltraPorGrupoYToleraAusenciaDeCompose(t *testing.T) {
	m, _ := newTestModel(t)

	// nil means no compose file, and the TUI reads nil as "do not paint the Composers section".
	if got := m.stacksForPrimary("tienda"); got != nil {
		t.Errorf("sin compose file dio %v, want nil", namesOfStacks(got))
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
		t.Errorf("stacksForPrimary(tienda) = %v, want sólo front", namesOfStacks(got))
	}
	if got := m.stacksForPrimary("no-existe"); len(got) != 0 {
		t.Errorf("un primary sin stacks dio %v, want vacío", namesOfStacks(got))
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
		t.Errorf("el servicio vivo quedó en %q, want stopping", got)
	}
	if got := m.services[api].Status; got == statusStopping {
		t.Errorf("un servicio ya parado quedó en stopping: parecería que se está parando algo parado")
	}

	m.markStackStopping(stack)
	if got := m.services[web].Status; got != statusStopping {
		t.Errorf("la segunda pasada cambió el estado a %q", got)
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
			t.Errorf("%s quedó en stopping sin que ningún servicio vivo lo pidiera", path)
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
		t.Error("markRunning no dejó el servicio vivo: los tests de stopping estarían probando otra cosa")
	}
	if got := m.services[path].Status; got != statusRunning {
		t.Errorf("Status = %q, want running", got)
	}
	var _ process.Manager = &stubManager{}
	var _ scanner.Project
	var _ state.Meta
	var _ tea.Cmd
}
