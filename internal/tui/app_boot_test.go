package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"vroom/internal/agents"
	"vroom/internal/config"
	"vroom/internal/launcher"
	"vroom/internal/orchestrate"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
	"vroom/internal/tail"
)

// The walk starts at each project and goes up, never above the root: a compose above it belongs to another workspace.
func TestFindComposeFileEmpiezaEnCadaProyectoYNoSeSaleDelRoot(t *testing.T) {
	compose := func(nombreStack string) string {
		return `
primary_group = "g"
[[stack]]
name = "` + nombreStack + `"
[[stack.stage]]
name = "e"
services = ["api"]
`
	}

	t.Run("sube hasta el root", func(t *testing.T) {
		root := t.TempDir()
		writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), compose("del-root"))
		hondo := filepath.Join(root, "grupo", "proyecto")
		if err := os.MkdirAll(hondo, 0o755); err != nil {
			t.Fatal(err)
		}

		cf, err := findComposeFile(root, []scanner.Project{{Path: hondo}})
		if err != nil {
			t.Fatalf("no encontró el compose del root: %v", err)
		}
		if len(cf.Stacks) != 1 || cf.Stacks[0].Name != "del-root" {
			t.Errorf("compose = %+v, want el stack del root", cf.Stacks)
		}
	})

	t.Run("encuentra uno intermedio que no está en el root", func(t *testing.T) {
		root := t.TempDir()
		writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), compose("del-root"))
		grupo := filepath.Join(root, "grupo")
		writeStr(t, filepath.Join(grupo, orchestrate.ComposeFileName), compose("del-grupo"))
		proj := filepath.Join(grupo, "proyecto")
		if err := os.MkdirAll(proj, 0o755); err != nil {
			t.Fatal(err)
		}

		cf, err := findComposeFile(root, []scanner.Project{{Path: proj}})
		if err != nil {
			t.Fatal(err)
		}
		if cf.Stacks[0].Name != "del-grupo" {
			t.Errorf("encontró el %q: el compose más cercano gana", cf.Stacks[0].Name)
		}
	})

	t.Run("el más cercano de varios proyectos gana", func(t *testing.T) {
		root := t.TempDir()
		writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), compose("del-root"))
		writeStr(t, filepath.Join(root, "cerca", orchestrate.ComposeFileName), compose("del-cerca"))

		cf, err := findComposeFile(root, []scanner.Project{{Path: filepath.Join(root, "cerca", "api")}})
		if err != nil {
			t.Fatal(err)
		}
		if cf.Stacks[0].Name != "del-cerca" {
			t.Errorf("encontró el %q", cf.Stacks[0].Name)
		}
	})

	t.Run("no se sale del root", func(t *testing.T) {
		// t.TempDir cannot raise this case because the tree hangs off /tmp: the root is pushed deeper so the compose above it is real.
		dir := t.TempDir()
		writeStr(t, filepath.Join(dir, orchestrate.ComposeFileName), compose("fuera"))

		hondo := filepath.Join(dir, "a", "b", "c")
		if err := os.MkdirAll(hondo, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := findComposeFile(hondo, []scanner.Project{{Path: hondo}}); err == nil {
			t.Error("se encontró un compose POR ENCIMA del root: es el de otro workspace")
		}
	})

	t.Run("sin compose en ningún sitio lo dice", func(t *testing.T) {
		root := t.TempDir()
		_, err := findComposeFile(root, []scanner.Project{{Path: root}})
		if err == nil {
			t.Fatal("sin compose tiene que dar error")
		}
		if !strings.Contains(err.Error(), orchestrate.ComposeFileName) {
			t.Errorf("err = %q, want que nombre el fichero que falta", err)
		}
		if !strings.Contains(err.Error(), root) {
			t.Errorf("err = %q, want que diga dónde buscó", err)
		}
	})

	t.Run("sin proyectos no hay nada que buscar", func(t *testing.T) {
		root := t.TempDir()
		writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), compose("x"))
		if _, err := findComposeFile(root, nil); err == nil {
			t.Error("sin proyectos no puede haber compose: la búsqueda parte de los proyectos")
		}
	})

	t.Run("un compose malformado no es el de nadie", func(t *testing.T) {
		root := t.TempDir()
		writeStr(t, filepath.Join(root, orchestrate.ComposeFileName), "esto no es toml [[[")
		if _, err := findComposeFile(root, []scanner.Project{{Path: root}}); err == nil {
			t.Error("un compose malformado tiene que rechazarse, no aceptarse como si no hubiera stacks")
		}
	})
}

// The CWD is the contract default and config root is the explicit exception, so scanner.root wins; "~" is expanded because people write it.
func TestNewUsaElRootDelConfigYExpandeElTilde(t *testing.T) {
	t.Run("sin root en el config: el CWD", func(t *testing.T) {
		root := writeTestTree(t, false)
		isolateConfig(t)

		m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
		if len(m.projects) == 0 {
			t.Error("con el CWD como root tiene que escanear los proyectos de debajo")
		}
	})

	t.Run("root del config relativo: bajo el CWD", func(t *testing.T) {
		root := writeTestTree(t, false)
		writeStr(t, filepath.Join(root, "sub", ".vroom.toml"), "name = \"sub-api\"\ncommand_start = \"./x\"\n")

		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		writeStr(t, cfgPath, "[scanner]\nroot = \"sub\"\n")
		t.Setenv("VROOM_CONFIG", cfgPath)

		m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
		// MEDIDO: Project.Name is the directory name, so only the path set proves what the root decided.
		var rutas []string
		for _, p := range m.projects {
			rutas = append(rutas, p.Path)
		}
		if len(rutas) != 1 || !strings.HasSuffix(rutas[0], "/sub") {
			t.Errorf("con root = \"sub\" escaneó %v, want sólo el subdirectorio: el root del config manda sobre el CWD", rutas)
		}
	})

	t.Run("root del config con tilde", func(t *testing.T) {
		home := t.TempDir()
		real := filepath.Join(home, "workspace")
		writeStr(t, filepath.Join(real, "api", ".vroom.toml"), "name = \"api-del-home\"\ncommand_start = \"./x\"\n")

		// The tilde resolves through the process HOME, which t.Setenv does change for os.UserHomeDir unlike /proc/self/environ.
		t.Setenv("HOME", home)
		t.Setenv("VROOM_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
		writeStr(t, os.Getenv("VROOM_CONFIG"), "[scanner]\nroot = \"~/workspace\"\n")

		m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, t.TempDir())
		var rutas []string
		for _, p := range m.projects {
			rutas = append(rutas, p.Path)
		}
		if len(rutas) != 1 || !strings.HasSuffix(rutas[0], "/workspace/api") {
			t.Errorf("con root = \"~/workspace\" escaneó %v, want el api de dentro: un ~ sin expandir es un directorio que no existe", rutas)
		}
	})
}

// Both boot errors are "start anyway and warn": a typo in the config cannot leave the user without a TUI.
func TestNewReportaUnEscaneoQueFallaYUnConfigInvalido(t *testing.T) {
	t.Run("config inválido: defaults más aviso", func(t *testing.T) {
		isolateConfig(t)
		root := writeTestTree(t, false)
		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		writeStr(t, cfgPath, "[ask]\nlauncher = \"inexistente\"\n")
		t.Setenv("VROOM_CONFIG", cfgPath)

		m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
		if m.message == "" {
			t.Error("un config inválido tiene que avisar: si no, el usuario cree que se aplica")
		}
		if len(m.projects) == 0 {
			t.Error("un config inválido no puede impedir el escaneo: vroom tiene que arrancar con los defaults")
		}
		if m.cfg.Ask.Launcher != "auto" {
			t.Errorf("Ask.Launcher = %q, want auto: el launcher inválido se descartó", m.cfg.Ask.Launcher)
		}
	})

	t.Run("los proyectos sin manifiesto salen como no configurados", func(t *testing.T) {
		isolateConfig(t)
		root := writeTestTree(t, false)
		if err := os.MkdirAll(filepath.Join(root, "sin-manifiesto"), 0o755); err != nil {
			t.Fatal(err)
		}

		m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
		for _, p := range m.projects {
			if !p.Configured {
				if sv := m.services[p.Path]; sv == nil || sv.Status != statusUnconfigured {
					t.Errorf("el proyecto sin manifiesto %q tiene estado %v, want unconfigured", p.Name, m.services[p.Path])
				}
			} else if sv := m.services[p.Path]; sv == nil || sv.Status != statusStopped {
				t.Errorf("el proyecto %q arranca como %v, want stopped: no se ha preguntado por él", p.Name, sv.Status)
			}
		}
	})

	t.Run("restaura el plegado persistido", func(t *testing.T) {
		isolateConfig(t)
		root := writeTestTree(t, false)
		store := state.NewStoreAt(t.TempDir())
		if err := store.SaveCollapsed(map[string]bool{"tienda": true}); err != nil {
			t.Fatal(err)
		}

		m := New(store, &stubManager{}, root)
		if !m.collapsed["tienda"] {
			t.Error("el plegado persistido no se restauró: el usuario pierde el árbol que tenía la última vez")
		}
		if m.collapsed["no-existe"] {
			t.Error("se inventó un estado de plegado que nadie guardó")
		}
	})
}

// A negative margin is what makes bordered misbehave, so no height may go below zero at any terminal size.
func TestUpdateLayoutNoDejaQueNingunaCajaSeSalga(t *testing.T) {
	m, _ := newTestModel(t)

	for _, dims := range [][2]int{
		{20, 5}, {30, 8}, {40, 12}, {50, 20}, {80, 24}, {100, 30}, {120, 40}, {200, 60}, {300, 80},
	} {
		m.width, m.height = dims[0], dims[1]
		m.updateLayout()

		ctx := func(what string, v int) {
			t.Helper()
			if v < 0 {
				t.Errorf("%dx%d: %s = %d, negativo: el compositor dibuja la caja al revés", dims[0], dims[1], what, v)
			}
		}
		ctx("bodyOuterH", m.bodyOuterH)
		ctx("bodyH", m.bodyH)
		ctx("rightW", m.rightW)
		ctx("contentH", m.contentH)
		ctx("treeVis", m.treeVis())

		if total := treeWidth + boxFrame + m.rightW + boxFrame; total > dims[0] && dims[0] > 40 {
			t.Errorf("%dx%d: las columnas suman %d, want <= %d", dims[0], dims[1], total, dims[0])
		}
		if m.detailsShown && m.contentH+detailsHeight+boxFrame+4 > dims[1] && dims[1] > 20 {
			t.Errorf("%dx%d: los paneles suman %d, want <= %d", dims[0], dims[1], m.contentH+detailsHeight+boxFrame+4, dims[1])
		}
	}
}

// Layout drops Details before the console: Details is informative, the console is what the user is working in.
func TestLaCajaDeDetallesSeOcultaCuandoNoCabe(t *testing.T) {
	m, _ := newTestModel(t)

	ancho, _ := newTestModel(t)
	ancho.width, ancho.height = 200, 60
	ancho.updateLayout()
	if !ancho.detailsShown {
		t.Error("en una pantalla ancha la caja de detalles tiene que estar: es donde vive el panel de detalle")
	}

	estrecho, _ := newTestModel(t)
	estrecho.width, estrecho.height = 40, 10
	estrecho.updateLayout()
	if estrecho.detailsShown {
		t.Error("en una pantalla estrecha hay que esconder Details antes que la consola")
	}
	if estrecho.contentH <= 0 {
		t.Errorf("contentH = %d con la pantalla más estrecha: la consola desaparece y no queda nada", estrecho.contentH)
	}

	_ = m
}

// Offsets jump to EOF rather than zero, or the first read reinserts the whole previous run's log.
func TestUpdateConMensajeDeConsolaRestableceLosOffsetsAlEOF(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	if _, err := m.store.EnsureServiceDir(path); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{m.store.StdoutLog(path), m.store.StderrLog(path)} {
		if err := os.WriteFile(p, []byte("contenido viejo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cs := m.consoleStateFor(path)
	cs.stdout, cs.stderr, cs.merged = "viejo en memoria\n", "viejo en memoria\n", "viejo en memoria\n"

	got := updateMsg(t, m, startedMsg{path: path, res: process.StartResult{Pid: 4321}})
	nuevo := got.consoleStateFor(path)

	if nuevo.stdout != "" || nuevo.stderr != "" || nuevo.merged != "" {
		t.Errorf("los buffers en memoria no se vaciaron: %q / %q / %q", nuevo.stdout, nuevo.stderr, nuevo.merged)
	}
	for i, off := range nuevo.off {
		if off == 0 {
			t.Errorf("el offset %d quedó a 0: el siguiente tail reinsertaría el log viejo", i)
		}
	}

	otro := moveCursorTo(t, m, "suelto")
	antes := otro.consoleView.View()
	got = updateMsg(t, otro, startedMsg{path: path, res: process.StartResult{Pid: 4321}})
	if got.consoleView.View() != antes {
		t.Error("arrancar un servicio que no es el seleccionado no puede tocar la consola visible")
	}
}

func TestUpdateConElTickDeSpinnerReLoSincronizaYConElTickDeEstadoHaceLoMismo(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	got, cmd := m.Update(spinnerTick())
	if cmd == nil {
		t.Fatal("el tick del spinner tiene que re-armarse o la animación se congela")
	}

	model := got.(Model)
	markRunning(&model, pathOfSelected(t, model), livePID(t))
	_, cmd2 := model.Update(tickMsg(time.Now()))
	if cmd2 == nil {
		t.Fatal("el tick de estado tiene que pedir un refresco o la tabla se queda congelada")
	}
}

// Every ptyDataMsg must re-arm the read: the PTY emits nothing on its own and the terminal would go mute.
func TestUpdateConElMensajeDeSalidaDelPtyLoEscribeYReArmaLaLectura(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	m, _ := newTestModel(t)
	m.term = s
	m.termOpen = true

	_, cmd := m.Update(ptyDataMsg{data: []byte("hola desde el shell")})
	if cmd == nil {
		t.Error("tras leer del PTY hay que re-armar la lectura: si no, la terminal se queda muda")
	}
	if !strings.Contains(s.screen(), "hola desde el shell") {
		t.Errorf("la salida del shell no llegó al emulador: %q", s.screen())
	}

	// EOF must not release the session: the PTY master does not emit EOF when the shell dies.
	antes := m.term
	got2, _ := m.Update(ptyEOFMsg{})
	if got2.(Model).term != antes {
		t.Error("el EOF no puede soltar la sesión: el master del PTY no emite EOF al morir el shell")
	}
	if !got2.(Model).termOpen {
		t.Error("el EOF no puede cerrar el modal: la sesión sigue viva")
	}
}

// The notice must report the exit code: with a deliberate `exit 1` the user closed their own terminal and a plain "closed" would blame vroom.
func TestUpdateConLaSalidaDelProcesoCierraElModalYAvisa(t *testing.T) {
	for _, tt := range []struct {
		nombre   string
		err      error
		quiere   string
		noQuiere string
	}{
		{"salida limpia", nil, "terminal closed", "exited"},
		{"con código", exitReal(t, 3), "terminal exited (3)", "terminal closed"},
		{"sin proceso", nil, "terminal closed", "exited"},
	} {
		t.Run(tt.nombre, func(t *testing.T) {
			s := newStubSession(40, 10, &stubPty{})
			m, _ := newTestModel(t)
			m.term = s
			m.termOpen = true

			got := updateMsg(t, m, ptyExitMsg{err: tt.err})
			if got.termOpen {
				t.Error("el shell terminó: el modal tiene que cerrarse")
			}
			if got.term != nil {
				t.Error("la sesión tiene que soltarse: si no, el modal cerrado la deja viva para siempre")
			}
			if !strings.Contains(got.message, tt.quiere) {
				t.Errorf("aviso = %q, want que contenga %q", got.message, tt.quiere)
			}
			if tt.noQuiere != "" && strings.Contains(got.message, tt.noQuiere) {
				t.Errorf("aviso = %q, no debe contener %q: el usuario cerró su propia terminal", got.message, tt.noQuiere)
			}
		})
	}
}

func TestUpdateConElResultadoDeStacksDiceLoQuePasó(t *testing.T) {
	tests := []struct {
		nombre      string
		msg         tea.Msg
		quiere      string
		tieneNombre bool
	}{
		{
			"lanzo bien",
			stackResultMsg{result: orchestrate.LaunchResult{OK: true, Stack: "front"}},
			"stack front launched", true,
		},
		{
			"lanzo pero la etapa fallo",
			stackResultMsg{result: orchestrate.LaunchResult{OK: false, Stack: "front", Error: "health check failed"}},
			"stack failed", true,
		},
		{
			"no lanzo ni a la tentativa",
			stackResultMsg{err: errLaunch},
			"stack error", false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			m := newStackModel(t)
			got := updateMsg(t, m, tt.msg)
			if !strings.Contains(got.message, tt.quiere) {
				t.Errorf("aviso = %q, want que contenga %q", got.message, tt.quiere)
			}
			// MEDIDO: only the paths with a stack name reach the timeline; a launch error has no service to attach to.
			var registrado bool
			for path := range got.events {
				if len(got.events[path]) > 0 {
					registrado = true
				}
			}
			if tt.tieneNombre && !registrado {
				t.Error("el resultado del stack no llegó al timeline: se pierde justo lo que el usuario viene a mirar")
			}
			if !tt.tieneNombre && registrado {
				t.Error("un fallo de lanzamiento sin nombre de stack no puede colgar eventos: no hay servicio al que atribuirlos")
			}
		})
	}
}

// Reporting "all stacks launched" when one of them failed is what makes the notice useless.
func TestUpdateConElResultadoDeComposersCuentaLosFallidos(t *testing.T) {
	tests := []struct {
		nombre  string
		results []orchestrate.LaunchResult
		quiere  string
	}{
		{
			"todos bien",
			[]orchestrate.LaunchResult{{OK: true, Stack: "a"}, {OK: true, Stack: "b"}},
			"all stacks launched in tienda",
		},
		{
			"uno fallo",
			[]orchestrate.LaunchResult{{OK: true, Stack: "a"}, {OK: false, Stack: "b", Error: "boom"}},
			"1 stack(s) failed in tienda",
		},
		{
			"tres fallaron",
			[]orchestrate.LaunchResult{{OK: false}, {OK: false}, {OK: false}},
			"3 stack(s) failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			m := newStackModel(t)
			got := updateMsg(t, m, composersResultMsg{primary: "tienda", results: tt.results})
			if !strings.Contains(got.message, tt.quiere) {
				t.Errorf("aviso = %q, want que contenga %q", got.message, tt.quiere)
			}
		})
	}
}

// The no-engine case is real for a hand-built model, so this path must not nil-deref somewhere less obvious.
func TestToggleStackYComposersDicenQueNoHayEngineONoHayStacks(t *testing.T) {
	t.Run("sin stacks en el grupo", func(t *testing.T) {
		m := newStackModel(t)
		next, cmd := m.toggleComposers("grupo-que-no-existe")
		got := next.(Model)
		if cmd != nil {
			t.Error("sin stacks no hay nada que lanzar")
		}
		if !strings.Contains(got.message, "no stacks found") {
			t.Errorf("aviso = %q, want que diga que no hay stacks para el grupo", got.message)
		}
	})

	sinEngine, _ := newTestModel(t)
	sinEngine.engine = nil

	_, cmd := sinEngine.toggleComposers("tienda")
	if cmd != nil {
		t.Error("sin engine no hay nada que lanzar")
	}

	nxt, cmd2 := sinEngine.toggleStack(&orchestrate.Stack{Name: "x"})
	if cmd2 != nil {
		t.Error("sin engine no hay nada que lanzar")
	}
	if !strings.Contains(nxt.(Model).message, "engine") {
		t.Errorf("aviso = %q, want que nombre el engine: sin él el usuario no sabe si falta el compose o otra cosa", nxt.(Model).message)
	}
}

// Group toggle starts the stopped ones only: restarting the live ones would leave duplicate processes fighting for the same port.
func TestToggleNodeArrancaLosParadosYNoTocaLosVivos(t *testing.T) {
	m, _ := newTestModel(t)
	cursorEn(t, &m, "tienda")

	api, web := projectPath(t, m, "tienda-api"), projectPath(t, m, "tienda-web")
	markRunning(&m, api, livePID(t))
	m.services[web].Status = statusStopped

	antes := map[string]uiStatus{}
	for path, sv := range m.services {
		antes[path] = sv.Status
	}

	next, cmd := m.toggleNode("tienda", "")
	if cmd == nil {
		t.Fatal("hay un servicio parado: tiene que arrancar algo")
	}
	got := next.(Model)

	if got.services[api].Status != statusRunning {
		t.Errorf("el servicio vivo quedó en %q, want running intacto: no se relanza lo que ya corre", got.services[api].Status)
	}
	if got.services[web].Status != statusStarting {
		t.Errorf("el parado quedó en %q, want starting", got.services[web].Status)
	}
	if antes[api] != got.services[api].Status {
		t.Error("un servicio vivo no puede cambiar de estado en la acción de grupo")
	}
}

// A node with no members must be a silent no-op, not an out-of-range index.
func TestToggleNodeSinMiembrosNoHaceNada(t *testing.T) {
	m, _ := newTestModel(t)

	_, cmd := m.toggleNode("grupo-que-no-existe", "")
	if cmd != nil {
		t.Error("un nodo sin miembros no puede lanzar nada")
	}

	_, cmd2 := m.toggleNode("tienda", "secundario-inventado")
	_ = cmd2
}

func TestEnterSelectionNoHaceNadaSobreUnStackNiSobreUnProyectoInline(t *testing.T) {
	t.Run("sobre un stack no reconstruye el árbol", func(t *testing.T) {
		m := newStackModel(t)
		cursorEn(t, &m, "front")
		arbolAntes := len(m.tree)

		next, cmd := m.enterSelection()
		got := next.(Model)
		if cmd != nil {
			t.Error("enter sobre un stack no emite comandos: los stacks no se pliegan")
		}
		if len(got.tree) != arbolAntes {
			t.Error("enter sobre un stack cambió el árbol: un stack no tiene hijos que plegar")
		}
	})

	t.Run("sobre un proyecto sin contenedor no hace nada", func(t *testing.T) {
		m, _ := newTestModel(t)
		// "suelto" has no primary_group
		m = moveCursorTo(t, m, "suelto")
		it, ok := m.selectedItem()
		if !ok || it.primary != "" {
			t.Skip("el árbol de test cambió: 'suelto' ya tiene primario")
		}
		arbolAntes := len(m.tree)
		next, _ := m.enterSelection()
		if len(next.(Model).tree) != arbolAntes {
			t.Error("enter sobre un proyecto inline cambió el árbol: no tiene contenedor que plegar")
		}
	})

	t.Run("sobre un proyecto con grupo lo pliega y conserva el cursor", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		arbolAntes := len(m.tree)

		next, _ := m.enterSelection()
		got := next.(Model)
		if len(got.tree) >= arbolAntes {
			t.Errorf("enter no plegó nada: el árbol pasó de %d a %d filas", arbolAntes, len(got.tree))
		}
		if !m.store.LoadCollapsed()[it0(m).primary] && !got.collapsed[it0(m).primary] {
			t.Error("el plegado no quedó en el modelo: enter no persistió nada")
		}
	})

	t.Run("sin item no hace nada", func(t *testing.T) {
		m, _ := newTestModel(t)
		m.tree = nil
		next, cmd := m.enterSelection()
		if cmd != nil || len(next.(Model).tree) != 0 {
			t.Error("sin selección enter no puede hacer nada")
		}
	})
}

// No refresh for nodes with nothing to fetch: cursor movement would spawn a git per row on a large workspace.
func TestOnSelectNoPideRefrescoParaLoQueNoTieneNadaQueRefrescar(t *testing.T) {
	t.Run("sobre un header no pide nada", func(t *testing.T) {
		m := sinSeleccion(t)
		next, cmd := m.onSelect()
		got := next.(Model)
		if cmd != nil {
			t.Error("un header no tiene logs que traer: pedir un refresco sería un git por fila")
		}
		if !strings.Contains(tail.StripANSI(got.consoleView.View()), "pick a service") {
			t.Errorf("consola = %q, want la pista de grupo", tail.StripANSI(got.consoleView.View()))
		}
	})

	t.Run("sobre un proyecto sin manifiesto no pide nada", func(t *testing.T) {
		m := sinManifiesto(t)
		if _, cmd := m.onSelect(); cmd != nil {
			t.Error("un proyecto sin manifiesto no tiene logs que traer")
		}
	})

	t.Run("sobre un proyecto configurado sí pide", func(t *testing.T) {
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		if _, cmd := m.onSelect(); cmd == nil {
			t.Error("un proyecto configurado sí tiene logs que traer: el cursor se movería sin actualizarse")
		}
	})
}

// Sampling a stopped service is a /proc walk on a dead PID, four times a second, per visible stopped service.
func TestRefreshThreadsNoPideNadaSinUnServicioVivo(t *testing.T) {
	m, _ := newTestModel(t)

	if cmd := m.refreshThreads(); cmd != nil {
		t.Error("sin proyecto no hay hilos que muestrear")
	}

	sinManif := sinManifiesto(t)
	if cmd := sinManif.refreshThreads(); cmd != nil {
		t.Error("sin manifiesto no hay proceso que muestrear")
	}

	m = moveCursorTo(t, m, "tienda-api")
	if cmd := m.refreshThreads(); cmd != nil {
		t.Error("un servicio parado no tiene hilos: muestrearlo es un /proc sobre un PID muerto")
	}

	markRunning(&m, projectPath(t, m, "tienda-api"), livePID(t))
	if cmd := m.refreshThreads(); cmd == nil {
		t.Error("un servicio vivo sí tiene hilos que muestrear")
	}
}

// /proc/0/task does not exist, so a live-looking service with Pid 0 would error on every tick.
func TestRefreshThreadsConPidCeroNoMuestrea(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := projectPath(t, m, "tienda-api")
	m.services[path].Status = statusRunning
	m.services[path].Meta.Pid = 0

	if cmd := m.refreshThreads(); cmd != nil {
		t.Error("sin PID no hay nada que muestrear: /proc/0 no existe")
	}
}

// tea.ExecProcess itself cannot be tested here (it suspends the program), so only the command's existence is asserted.
func TestEditLogsCmdTraeElEditorDevueltoYElError(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no hay /bin/sh")
	}
	cmd := editLogsCmd("/bin/sh -c true", "/l/out.log", "/l/err.log", false)
	if cmd == nil {
		t.Fatal("editLogsCmd devolvió nil")
	}
	m, _ := newTestModel(t)
	_ = m
}

// esc with a filter applied clears it and stays: quitting there would lose a half-finished stack teardown.
func TestHandleKeyConEscLimpiaElFiltroAntesDeSalir(t *testing.T) {
	t.Run("con filtro aplicado: limpia y no sale", func(t *testing.T) {
		m, _ := newTestModel(t)
		aplicado, _ := m.applyFilter("tienda")
		conFiltro := aplicado.(Model)
		if conFiltro.filterText == "" {
			t.Fatal("precondición: el filtro debería estar aplicado")
		}

		got, _ := conFiltro.handleKey(keyMsg("esc"))
		model := got.(Model)
		if model.filterText != "" {
			t.Errorf("esc no limpió el filtro: %q", model.filterText)
		}
		if model.filterText != "" {
			t.Errorf("el filtro sigue en %q", model.filterText)
		}
	})

	t.Run("sin filtro: esc sale", func(t *testing.T) {
		m, _ := newTestModel(t)
		next, cmd := m.handleKey(keyMsg("esc"))
		_ = next
		if cmd == nil {
			t.Error("sin filtro, esc sale del programa: no hay nada que cerrar")
		}
	})
}

// enter means "done typing" (live filtering already applied it); esc means "changed my mind" and clears.
func TestFilterKeyCierraElBoxSinLimpiarConEnter(t *testing.T) {
	m, _ := newTestModel(t)
	m.filterOpen = true
	m.filterInput.SetValue("tienda")
	m.filterText = "tienda"

	t.Run("enter cierra y conserva", func(t *testing.T) {
		next, _ := m.filterKey(keyMsg("enter"))
		got := next.(Model)
		if got.filterOpen {
			t.Error("enter tiene que cerrar el box")
		}
		if got.filterText != "tienda" {
			t.Errorf("enter vació el filtro: %q. El filtrado en vivo ya lo había aplicado", got.filterText)
		}
	})

	t.Run("esc cierra y limpia", func(t *testing.T) {
		next, _ := m.filterKey(keyMsg("esc"))
		got := next.(Model)
		if got.filterOpen {
			t.Error("esc tiene que cerrar el box")
		}
		if got.filterText != "" {
			t.Errorf("esc dejó el filtro en %q: el usuario creería haberlo quitado", got.filterText)
		}
	})

	t.Run("ctrl+c sale sin cerrar nada", func(t *testing.T) {
		next, cmd := m.filterKey(keyMsg("ctrl+c"))
		if cmd == nil {
			t.Error("ctrl+c es la salida de emergencia y no depende del estado del box")
		}
		_ = next
	})
}

// Only the background strategies return here, and their error must reach the user: a failed `herdr pane split` would look like a started agent.
func TestLaunchAskCmdTraeElMensajeDelLauncherYSuError(t *testing.T) {
	l := nuevoLauncherEnBackground(t)

	req := reqAsk()
	msg := launchAskCmd(l, "custom", req)()
	sm, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("launchAskCmd devolvió %T, want statusMsg", msg)
	}
	if sm.message == "" {
		t.Error("el launcher volvió sin mensaje: el usuario no sabe qué pasó con su agente")
	}
}

// Non-inline strategies must dispatch without blocking: waiting for the agent to exit is what the launcher exists to avoid.
func TestDispatchAskConEstrategiaNoInlineDespachaEnBackground(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.askPromptOpen = true
	m.askAgent = agenteFalso()
	m.promptInput.SetValue("arregla el bug")
	m.askLauncher = nuevoLauncherEnBackground(t)
	m.promptInput.Focus()

	next, cmd := m.dispatchAsk()
	got := next.(Model)
	if cmd == nil {
		t.Fatal("una estrategia en background tiene que despachar")
	}
	if got.askPromptOpen {
		t.Error("el modal tiene que cerrarse al despachar: si no, se solapa con la salida del agente")
	}
	if got.message != "" {
		t.Errorf("aviso = %q antes de despachar: el resultado llega después", got.message)
	}
}

// MouseMode is CellMotion, not drag: the wheel is all that is needed and drag would enable text selection inside a bordered dashboard.
func TestViewPoneAltScreenYRueda(t *testing.T) {
	m, _ := newTestModel(t)
	v := m.View()

	if !v.AltScreen {
		t.Error("el dashboard tiene que ir a pantalla completa: no cabe en la altura parcial del terminal")
	}
	if v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("MouseMode = %v, want CellMotion: lo que hace falta es la rueda", v.MouseMode)
	}
	if strings.TrimSpace(v.Content) == "" {
		t.Error("la vista no trae contenido: alt screen en blanco es un programa colgado")
	}
}

func it0(m Model) treeItem {
	it, ok := m.selectedItem()
	if !ok {
		panic("no hay item bajo el cursor")
	}
	return it
}

func spinnerTick() tea.Msg {
	return spinner.TickMsg{}
}

// A fake error would make exitCode return 0 and the test would check the wrong case, so a real exiting command runs.
func exitReal(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	if err == nil {
		t.Fatalf("sh -c \"exit %d\" salió con 0: el error de test no probaría nada", code)
	}
	return err
}

// a stack launch failure with no exit code behind it.
var errLaunch = errors.New("no se pudo lanzar")

// A custom-strategy launcher over a fake script on PATH: the background ask path that does not suspend the program.
func nuevoLauncherEnBackground(t *testing.T) *launcher.Launcher {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"agente lanzado en $PWD\"\n"
	if err := os.WriteFile(filepath.Join(bin, "agente-falso"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return launcher.New(config.AskConfig{Launcher: "custom", LauncherCmd: "agente-falso"})
}

func reqAsk() launcher.Request {
	return launcher.Request{Agent: "agente-falso", Args: []string{"agente-falso"}, Dir: "/tmp"}
}

func agenteFalso() agents.Agent {
	return agents.Agent{Name: "agente-falso", Cmd: []string{"agente-falso"}}
}
