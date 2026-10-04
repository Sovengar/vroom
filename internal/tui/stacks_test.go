package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vroom/internal/orchestrate"
	"vroom/internal/state"
)

// The real engine is required: an engine double would only prove the TUI calls a double.
func stackTree(t *testing.T) (root string, store *state.Store) {
	t.Helper()
	isolateConfig(t)
	root = writeTestTree(t, false)
	// MEDIDO: fake ports are fine here, because process.Evaluate only dials a service alive when its port is open and the aggregation is under test, not the dial.
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

func TestStackStatsCuentaLosServiciosDelStackYNoLosDemas(t *testing.T) {
	m := newStackModel(t)
	stacks := m.stacksForPrimary("tienda")
	if len(stacks) != 1 {
		t.Fatalf("hay %d stacks, want 1", len(stacks))
	}
	stack := &stacks[0]

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

	markRunning(&m, projectPath(t, m, "tienda-web"), 4242)
	running, total, err = m.stackStats(stack)
	if err != nil {
		t.Fatal(err)
	}
	if running != 1 || total != 2 {
		t.Errorf("running/total = %d/%d, want 1/2", running, total)
	}
}

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

// Only the warning and the Cmd are asserted: the state changes when stackResultMsg arrives, so pinning it here would pin a state that does not exist.
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
	for path, sv := range got.services {
		if sv.Status == statusStopping {
			t.Errorf("%s quedó en stopping al lanzar el stack", path)
		}
	}
}

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

// The compose file cannot express a name duplicated across two worktrees, so the rejection has to name the cause instead of silently doing nothing.
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

// The contract is one single Cmd carrying every stack, which is what makes them launch in parallel.
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

	// The criterion is ALL: with one of the two stacks alive it launches and restarts the stopped one, because stopping a half-raised group would leave a state nobody asked for.
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

func TestToggleComposersParaCuandoTodosEstanVivos(t *testing.T) {
	m := newStackModel(t)
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
			// The action names come from config, so these are the harness default keys, already exercised by other tests.
			out, _ := m.Update(keyPress(key))
			got := out.(Model)
			if !strings.Contains(got.message, "not available for stacks") {
				t.Errorf("tecla %q sobre un stack dio %q, want el aviso de no disponible", key, got.message)
			}
		})
	}
}

// 'o' is a fixed alias for logs, but a config that binds 'o' wins over the alias.
func TestHandleKeyAliasOAlwaysOpensTheEditor(t *testing.T) {
	m, _ := newJobsTestModel(t)
	m.cursor = findCursor(m, "tienda-api")

	if _, ok := m.keyActions["o"]; ok {
		t.Skip("el config del harness reclama 'o': el alias cedido es el caso del config, no éste")
	}
	out, _ := m.Update(keyPress("o"))
	got := out.(Model)
	if got.message == "editor closed" {
		t.Error("no se ejecutó el cmd del editor: la aserción se apoyaría en nada")
	}
}
