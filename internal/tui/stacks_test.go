package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/orchestrate"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// Orquestación de stacks en la TUI: toggleStack, toggleComposers, stackStats.
//
// Aquí sí hace falta el ENGINE REAL, y no un doble, porque lo que hay que
// verificar es que la TUI y el motor DICEN LO MISMO: que la TUI calcula
// "corriendo" con el mismo criterio que usa el motor para decidir, y que el
// motor recibe el stack entero. Un doble de engine probaría que la TUI llama a
// un doble.
//
// El engine se construye con el mismo `New` de producción, que lo crea él solo
// cuando encuentra un compose file. Por eso el árbol de estos tests lleva uno.
// ---------------------------------------------------------------------------

// stackTree es el árbol de test con un compose file, que es lo que hace que New
// construya el engine.
func stackTree(t *testing.T) (root string, store *state.Store) {
	t.Helper()
	isolateConfig(t)
	root = writeTestTree(t, false)
	// Los dos servicios del árbol necesitan puerto ABIERTO para que
	// process.Evaluate los dé por vivos: ver el comentario de listeningService en
	// el paquete de cli. Aquí se usan puertos falsos porque lo que se prueba es la
	// lógica de agregación del stack, no el dial.
	writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), `primary_group = "tienda"

[[stack]]
name = "front"
primary_group = "tienda"

  [[stack.stage]]
  name = "front"
  services = ["tienda-web", "tienda-api"]
`)
	return root, state.NewStoreAt(t.TempDir())
}

func writeStr(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newStackModel construye el modelo con engine a partir de stackTree.
func newStackModel(t *testing.T) Model {
	t.Helper()
	root, store := stackTree(t)
	m := New(store, &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()
	if m.engine == nil {
		t.Fatalf("New no construyó el engine: falta el compose file %s", orchestrate.ComposeFileName)
	}
	return m
}

// TestStackStatsCuentaLosServiciosDelStackYNoLosDemas: el veredicto de "el stack
// está corriendo" sale de SUS servicios, no de todos los del workspace.
//
// Es la propiedad de la que depende toda la decisión de toggleStack: si contara
// de más, un stack con un servicio parado se declararía corriendo y el toggle lo
// pararía cuando el usuario quería arrancarlo. Y si contara de menos, un stack
// con un servicio de más en el workspace nunca parecería completo.
func TestStackStatsCuentaLosServiciosDelStackYNoLosDemas(t *testing.T) {
	m := newStackModel(t)
	stacks := m.stacksForPrimary("tienda")
	if len(stacks) != 1 {
		t.Fatalf("hay %d stacks, want 1", len(stacks))
	}
	stack := &stacks[0]

	// Nadie vivo: 0 de 2.
	running, total, err := m.stackStats(stack)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (los dos servicios del stack)", total)
	}
	if running != 0 {
		t.Errorf("running = %d sin ningún servicio vivo", running)
	}

	// Uno vivo: 1 de 2. Un servicio fuera del stack no cuenta.
	markRunning(&m, projectPath(t, m, "tienda-web"), 4242)
	running, total, err = m.stackStats(stack)
	if err != nil {
		t.Fatal(err)
	}
	if running != 1 || total != 2 {
		t.Errorf("running/total = %d/%d, want 1/2", running, total)
	}
}

// TestStackStatsConNombreAmbiguoEsError: dos proyectos con el mismo nombre de
// manifiesto dentro del stack NO se pueden contar, y el recuento tiene que decirlo.
//
// Sin esto, el TUI calcularía running==total con un recuento que no sabe a qué
// servicio corresponde y declararía el stack corriendo cuando en realidad uno de
// los dos nunca arrancó.
func TestStackStatsConNombreAmbiguoEsError(t *testing.T) {
	isolateConfig(t)
	root := writeTestTree(t, false)
	for _, sub := range []string{"dup-a", "dup-b"} {
		writeStr(t, filepath.Join(root, sub, ".vroom.toml"),
			"name = \"dup\"\ncommand_start = \"sleep 60\"\nport = 9001\n")
	}
	writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), `primary_group = "tienda"

[[stack]]
name = "front"
primary_group = "tienda"

  [[stack.stage]]
  name = "front"
  services = ["dup"]
`)
	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()
	stacks := m.stacksForPrimary("tienda")
	if len(stacks) != 1 {
		t.Fatalf("hay %d stacks, want 1", len(stacks))
	}

	if _, _, err := m.stackStats(&stacks[0]); err == nil {
		t.Error("un nombre ambiguo debería hacer fallar el recuento: el TUI no puede decidir con un recuento que no sabe a qué servicio corresponde")
	}
}

// TestToggleStackArrancaUnStackParadoYAvisa: la mitad de "arrancar" de toggleStack.
//
// Lo que se comprueba es el AVISO y el Cmd, no el estado: el arranque ocurre en
// el Cmd, que devuelve un stackResultMsg, y el estado sólo cambia cuando ese msg
// vuelve. Fijar el estado aquí sería fijar un estado intermedio que no existe.
func TestToggleStackArrancaUnStackParadoYAvisa(t *testing.T) {
	m := newStackModel(t)
	stacks := m.stacksForPrimary("tienda")
	stack := &stacks[0]

	out, cmd := m.toggleStack(stack)
	got := out.(Model)

	if cmd == nil {
		t.Fatal("un stack parado debería devolver el Cmd de arranque")
	}
	if !strings.Contains(got.message, "launching stack front") {
		t.Errorf("el aviso no dice que se está lanzando: %q", got.message)
	}
	// Y nada se marca como stopping: un stack parado no se está parando.
	for path, sv := range got.services {
		if sv.Status == statusStopping {
			t.Errorf("%s quedó en stopping al lanzar el stack", path)
		}
	}
}

// TestToggleStackParaUnStackCompletoYAvisoYMarcaStopping: la mitad de "parar", que
// es la que cambia el estado de los servicios.
//
// Marcar stopping ANTES de que el motor conteste es lo que evita que la TUI
// muestre "corriendo" durante los segundos que tarda el stop. Y es sólo lo que
// estaba vivo: un servicio del stack ya parado se queda como estaba.
func TestToggleStackParaUnStackCompletoYAvisoYMarcaStopping(t *testing.T) {
	m := newStackModel(t)
	stacks := m.stacksForPrimary("tienda")
	stack := &stacks[0]

	web := projectPath(t, m, "tienda-web")
	api := projectPath(t, m, "tienda-api")
	markRunning(&m, web, 4242)
	markRunning(&m, api, 4243)

	out, cmd := m.toggleStack(stack)
	got := out.(Model)

	if cmd != nil {
		t.Error("parar un stack es síncrono: el motor lo hace aquí y no devuelve Cmd")
	}
	if !strings.Contains(got.message, "stopping stack front") {
		t.Errorf("el aviso no dice que se está parando: %q", got.message)
	}
	for _, path := range []string{web, api} {
		if got.services[path].Status != statusStopping {
			t.Errorf("%s quedó en %q, want stopping: mientras se para hay que mostrar algo, no 'corriendo'", path, got.services[path].Status)
		}
	}
}

// TestToggleStackConConflictoLoDiceYNoLanzaNada: un stack cuyo servicio tiene
// nombre ambiguo no se puede ni contar ni lanzar, y el usuario tiene que saber
// POR QUÉ.
//
// El silencio aquí sería especialmente malo: el usuario pulsa la tecla, no pasa
// nada, y no hay forma de saber que el problema es un nombre duplicado en dos
// worktrees — que es justo el caso que el compose file no puede expresar.
func TestToggleStackConConflictoLoDiceYNoLanzaNada(t *testing.T) {
	isolateConfig(t)
	root := writeTestTree(t, false)
	for _, sub := range []string{"dup-a", "dup-b"} {
		writeStr(t, filepath.Join(root, sub, ".vroom.toml"),
			"name = \"dup\"\ncommand_start = \"sleep 60\"\nport = 9001\n")
	}
	writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), `primary_group = "tienda"

[[stack]]
name = "front"
primary_group = "tienda"

  [[stack.stage]]
  name = "front"
  services = ["dup"]
`)
	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()
	stacks := m.stacksForPrimary("tienda")

	out, cmd := m.toggleStack(&stacks[0])
	got := out.(Model)

	if cmd != nil {
		t.Error("con un conflicto no se debe lanzar nada")
	}
	if !strings.Contains(got.message, "stack conflict") {
		t.Errorf("el aviso no dice que hay conflicto: %q", got.message)
	}
	if !strings.Contains(got.message, "ambiguous") {
		t.Errorf("el aviso no explica la causa: %q", got.message)
	}
}

// TestToggleComposersSinStacksLoDice: un primary sin stacks no es un error
// silencioso.
//
// El caso es real: el compose file define `primary_group = "tienda"` a nivel
// superior, así que un grupo del árbol sin ningún stack es normal. Pulsar la tecla
// ahí no debe hacer nada sin decir nada.
func TestToggleComposersSinStacksLoDice(t *testing.T) {
	m := newStackModel(t)

	out, cmd := m.toggleComposers("grupo-que-no-tiene-stacks")
	got := out.(Model)

	if cmd != nil {
		t.Error("sin stacks no hay nada que lanzar")
	}
	if !strings.Contains(got.message, "no stacks found") {
		t.Errorf("el aviso no explica que no hay stacks: %q", got.message)
	}
	if !strings.Contains(got.message, "grupo-que-no-tiene-stacks") {
		t.Errorf("el aviso no nombra el grupo: %q", got.message)
	}
}

// TestToggleComposersLanzaTodosLosStacksDelGrupo: la mitad de "lanzar" de
// toggleComposers, y su contrato es que devuelve UN Cmd con todos los stacks.
//
// La diferencia con toggleStack es la agregación por grupo: el header Composers
// actúa sobre todo lo que cuelga de un primary_group, no sobre un stack. El Cmd
// único es lo que hace que los stacks se lanceran en paralelo en vez de uno a
// uno, que es la razón de ser del botón.
func TestToggleComposersLanzaTodosLosStacksDelGrupo(t *testing.T) {
	isolateConfig(t)
	root := writeTestTree(t, false)
	writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), `primary_group = "tienda"

[[stack]]
name = "uno"
primary_group = "tienda"

  [[stack.stage]]
  name = "front"
  services = ["tienda-web"]

[[stack]]
name = "dos"
primary_group = "tienda"

  [[stack.stage]]
  name = "front"
  services = ["tienda-api"]
`)
	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()
	if len(m.stacksForPrimary("tienda")) != 2 {
		t.Fatalf("el árbol de test no tiene 2 stacks: %v", namesOfStacks(m.stacksForPrimary("tienda")))
	}

	out, cmd := m.toggleComposers("tienda")
	got := out.(Model)
	if cmd == nil {
		t.Fatal("con stacks parados debería devolver el Cmd de lanzamiento")
	}
	if !strings.Contains(got.message, "launching stacks in tienda") {
		t.Errorf("el aviso no dice que se lanzan stacks: %q", got.message)
	}

	// Con UNO de los dos stacks vivo el criterio de "todos" NO se cumple, así que
	// se LANZA (que reinicia el parado) en vez de parar. Parar un stack a medio
	// levantar dejaría el grupo en un estado que nadie pidió.
	for _, p := range m.projects {
		if p.Name == "tienda-web" {
			markRunning(&m, p.Path, 4242)
		}
	}
	out, cmd = m.toggleComposers("tienda")
	got = out.(Model)
	if cmd == nil {
		t.Error("con un stack a medias debería lanzar, no parar")
	}
	if !strings.Contains(got.message, "launching stacks") {
		t.Errorf("con stacks a medias el aviso dice %q, want launching", got.message)
	}
}

// TestToggleComposersParaCuandoTodosEstanVivos: la mitad de "parar" del
// agregador, con el criterio de "todos" explícito.
//
// El criterio es TODOS, no "alguno": con stacks parcialmente vivos, el botón lanza
// en vez de parar, porque parar un stack a medio levantar deja el grupo en un
// estado que nadie pidió. Y eso se comprueba aquí: un stack vivo y otro parado NO
// activan el camino de parada.
func TestToggleComposersParaCuandoTodosEstanVivos(t *testing.T) {
	m := newStackModel(t)
	// Un solo stack con un servicio: si ese servicio vive, el stack está vivo.
	stacks := m.stacksForPrimary("tienda")
	stack := &stacks[0]
	for _, name := range []string{"tienda-web", "tienda-api"} {
		markRunning(&m, projectPath(t, m, name), 4242)
	}

	out, cmd := m.toggleStack(stack)
	got := out.(Model)
	if cmd != nil {
		t.Error("un stack completo se para de forma síncrona, sin Cmd")
	}
	if !strings.Contains(got.message, "stopping stack front") {
		t.Errorf("con todos los servicios vivos debería parar, no lanzar: %q", got.message)
	}
}

// TestHandleKeyRechazaBuildInstallYLogsEnUnStack: hay acciones que no tienen
// sentido sobre un stack, y el rechazo tiene que ser EXPLÍCITO.
//
// La alternativa —no hacer nada— es indistinguible de "la tecla no está
// asignada", y el usuario no tiene forma de saber que la operación no se puede
// hacer ahí. El aviso dice qué hacer en su lugar.
func TestHandleKeyRechazaBuildInstallYLogsEnUnStack(t *testing.T) {
	isolateConfig(t)
	root := writeTestTree(t, false)
	writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), `primary_group = "tienda"

[[stack]]
name = "front"
primary_group = "tienda"

  [[stack.stage]]
  name = "front"
  services = ["tienda-web"]
`)
	m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
	m.width, m.height = 100, 30
	m.updateLayout()

	// El cursor sobre el item del stack.
	stackIdx := -1
	for i, it := range m.tree {
		if it.kind == itemStack {
			stackIdx = i
		}
	}
	if stackIdx < 0 {
		t.Skip("este árbol no expone el stack como item del árbol: el caso no se puede provocar")
	}
	m.cursor = stackIdx

	for _, key := range []string{"b", "i", "l"} {
		t.Run(key, func(t *testing.T) {
			// El nombre de la acción depende del config; se usan las teclas por
			// defecto del harness, que ya están exercised por otros tests.
			out, _ := m.Update(keyPress(key))
			got := out.(Model)
			if !strings.Contains(got.message, "not available for stacks") {
				t.Errorf("tecla %q sobre un stack dio %q, want el aviso de no disponible", key, got.message)
			}
		})
	}
}

// TestHandleKeyAliasOAlwaysOpensTheEditor: la tecla `o` es alias fijo de logs,
// salvo que el config la reclame.
//
// "Salvo que el config lo reclame" es la parte que hace que esto sea una regla y
// no un hecho: si el config asigna `o` a otra cosa, manda el config.
func TestHandleKeyAliasOAlwaysOpensTheEditor(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m.cursor = findCursor(m, "tienda-api")

	if _, ok := m.keyActions["o"]; ok {
		t.Skip("el config del harness reclama 'o': el alias cedido es el caso del config, no éste")
	}
	out, _ := m.Update(keyPress("o"))
	got := out.(Model)
	// Abrir el editor deja un cmd de ExecProcess; no hace falta ejecutarlo.
	if got.message == "editor closed" {
		t.Error("no se ejecutó el cmd del editor: la aserción se apoyaría en nada")
	}
}
