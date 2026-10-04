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

// ---------------------------------------------------------------------------
// Las interacciones de la TUI que estaban al 0%: toggleComposers, toggleStack,
// markStackStopping, scrollDetails, refreshBatch, stacksForPrimary.
//
// Todas comparten una forma: reciben el estado del modelo y DEVUELVEN un modelo
// nuevo más un Cmd. Eso las hace ejecutables sin Bubbletea —se aplica el modelo
// devuelto y se ejecuta el Cmd— y es lo que permite comprobar dos cosas que un
// test de render no vería: qué ESTADO queda, y qué se le pide al runtime.
//
// El patrón de todos estos tests es el mismo: dejar el modelo en el estado que
// dispara la rama, ejecutar la acción, y comprobar el efectoobservable (un
// servicio que pasa a stopping, un aviso concreto, un Cmd no nil). Comprobar el
// aviso en vez del estado interno es lo que hace que el test siga valiendo si
// mañana la implementación cambia pero el contrato no.
// ---------------------------------------------------------------------------

// TestScrollDetailsNoBajaDeCero: el scroll del panel de detalles tiene suelo, y
// es un suelo que se nota.
//
// Abajo del todo, `detailsTop` es negativo y la vistaintentaría leer por encima
// del principio del buffer. Con un delta grande de golpe (pgup) es fácil llegar
// ahí, y el clamp es lo que evita que el panel muestre basura.
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
	// Y bajar de 0 no hace nada: no hay un suelo simétrico arriba del buffer.
	m.scrollDetails(-9999)
	if m.detailsTop != 0 {
		t.Errorf("detailsTop = %d, want 0", m.detailsTop)
	}
}

// TestPgUpPgDownEnUnNodoDesplazanElPanelDeDetalles: cuando el cursor está en un
// grupo o un stack, pgup/pgdown mueven el PANEL y no la consola.
//
// Es la regla que evita que la rueda de un panel mueva el otro: con el cursor en
// un proyecto, pgup hace scroll de consola aunque el panel esté visible; con el
// cursor en un grupo, hace scroll del panel. Confundir las dos enruta el scroll
// al sitio que el usuario no está mirando.
func TestPgUpPgDownEnUnNodoDesplazanElPanelDeDetalles(t *testing.T) {
	m, _ := newTestModel(t)
	m.detailsShown = true

	// Cursor sobre el header primario (kind != itemProject).
	m.cursor = findPrimary(m, "tienda")

	m2, _ := press(m, "pgup")
	got := m2
	if got.detailsTop != 0 {
		t.Errorf("pgup en un nodo ya estaba arriba: detailsTop = %d, want 0 (el clamp)", got.detailsTop)
	}
	if !got.consoleFollow {
		t.Error("pgup sobre un nodo no debe pausar el follow de la consola: el scroll fue al panel")
	}

	// Bajar sí mueve el panel.
	m3, _ := press(got, "pgdown")
	down := m3
	if down.detailsTop != detailsHeight {
		t.Errorf("detailsTop = %d tras pgdown en un nodo, want %d", down.detailsTop, detailsHeight)
	}

	// Y con el cursor sobre un proyecto, pgup va a la consola y pausa el follow.
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

// TestRefreshBatchPideEstadoGitYLaPestanaActiva: el refresh manual no es sólo un
// refresh de estado: también re-lee la rama git y la pestaña activa.
//
// La rama es lo que hace que un `git branch -m` cambie el nombre de la ruta sin
// reiniciar la TUI, y la pestaña es lo que evita que el refresh deje en blanco lo
// que el usuario está mirando.
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

	// Y con la pestaña de consola y un proyecto configurado seleccionado se pide
	// además el TAIL, con independencia de que el proceso esté vivo.
	//
	// La independencia es deliberada y es lo correcto: el log de un servicio que
	// acaba de morir sigue teniendo bytes que enseñar, y filtrar el tail por
	// liveness dejaría en blanco justo la última salida de un servicio parado,
	// que es la que el usuario va a buscar.
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

	// Y una pestaña que no necesita datos no inventa trabajo: sin proyecto
	// seleccionado no hay log que leer.
	none := m
	none.cursor = findPrimary(none, "tienda") // un header no es un proyecto
	if cmd := none.refreshTab(); cmd != nil {
		if msgs := collectBatch(t, cmd); len(msgs) > 0 {
			t.Errorf("sobre un header de grupo se pidieron datos: %d comandos", len(msgs))
		}
	}
}

// TestToggleStackSinEngineLoDice: sin engine no hay orquestación, y el usuario
// tiene que enterarse en vez de que la tecla no haga nada.
//
// El silencio es el peor resultado: el usuario pulsa espacio y la TUI no cambia,
// y no hay forma de saber si es un bug o que la tecla no está asignada.
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

// TestStacksForPrimaryFiltraPorGrupoYToleraAusenciaDeCompose: los stacks son
// del primary_group que dice el compose file, no del directorio.
//
// Y sin compose file la lista es vacía, no un panic: el compose file es opcional
// y su ausencia tiene que ser el caso normal de un workspace sin orquestación.
func TestStacksForPrimaryFiltraPorGrupoYToleraAusenciaDeCompose(t *testing.T) {
	m, _ := newTestModel(t)

	// Sin compose: nil. Es lo que la TUI usa para no pintar la sección Composers.
	if got := m.stacksForPrimary("tienda"); got != nil {
		t.Errorf("sin compose file dio %v, want nil", namesOfStacks(got))
	}

	// Con compose, cada stack va a su primary_group. Se usa el campo del stack
	// porque un stack puede sobreescribir el grupo del compose.
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

// TestMarkStackStoppingSoloMarcaLoQueEstaVivo: marcar un stack como stopping
// cambia el estado de SUS servicios, y sólo de los que estaban vivos.
//
// Las dos mitades importan. Marcar uno parado como stopping lo haría parecer que
// se está parando algo que ya estaba parado, y el usuario vería una animación
// perpetuo. Y los servicios que el stack menciona pero no existen (renombrados,
// borrados) se saltan sin panico, porque LookupService falla y eso es routine.
func TestMarkStackStoppingSoloMarcaLoQueEstaVivo(t *testing.T) {
	m, _ := newTestModel(t)
	web := projectPath(t, m, "tienda-web")
	api := projectPath(t, m, "tienda-api")

	markRunning(&m, web, 4242) // vivo
	// api queda parado a propósito.
	// un tercer servicio que el stack menciona pero no existe en el workspace.

	// Los servicios del stack se nombran por el NOMBRE DEL MANIFIESTO, que es lo
	// que resuelve LookupService. Usar el nombre del directorio haría que todo
	// stack pareciera vacío, y eso es un error silencioso: markStackStopping no
	// avisaría, simplemente no marcaría nada.
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

	// Y repetir la llamada no degrada el estado: el mismo servicio mencionado dos
	// veces en el stack se marca una vez y no se toca dos veces.
	m.markStackStopping(stack)
	if got := m.services[web].Status; got != statusStopping {
		t.Errorf("la segunda pasada cambió el estado a %q", got)
	}
}

// TestMarkStackStoppingConServicioInexistenteNoRompe: un stack que menciona un
// servicio que no está en el workspace se salta.
//
// Es el caso del stack editado a mano o del servicio borrado del disco: el stack
// sigue referencing, el workspace ya no. Entrar en pánico por eso dejaría la TUI
// muerta por un fichero de texto.
func TestMarkStackStoppingConServicioInexistenteNoRompe(t *testing.T) {
	m, _ := newTestModel(t)
	stack := &orchestrate.Stack{
		Name:   "front",
		Stages: []orchestrate.Stage{{Name: "front", Services: []string{"no-existe-nunca"}}},
	}
	m.markStackStopping(stack)
	// Nada que comprobar más allá de no haber muerto: los estados intactos.
	for path, sv := range m.services {
		if sv.Status == statusStopping {
			t.Errorf("%s quedó en stopping sin que ningún servicio vivo lo pidiera", path)
		}
	}
}

// ---- helpers ----

func namesOfStacks(stacks []orchestrate.Stack) []string {
	out := make([]string, 0, len(stacks))
	for _, s := range stacks {
		out = append(out, s.Name)
	}
	return out
}

// Sanity del harness: los estados vivos que markRunning deja deben ser los que
// isRunning reconoce, o todos estos tests estarían probando el camino del
// servicio parado por accident.
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
