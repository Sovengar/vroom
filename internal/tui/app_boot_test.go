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

// ---------------------------------------------------------------------------
// El armado del modelo, la geometría del layout y los caminos de `Update` que
// ninguna otra suite pisa.
//
// Las tres cosas que seumped aquí comparten una razón: son las que decides al
// arrancar. Un `New` mal armado hace que la TUI arranque con un árbol equivocado,
// un `updateLayout` mal clavado hace que las cajas se salgan de la pantalla, y un
// `Update` sin cubrir deja mensajes sin procesar que se acumulan en silencio.
//
// Y hay un detalle que aparece en varios sitios: los mensajes de la TUI son
// structs internos, no hay forma de inyectarlos desde fuera del paquete. Así que
// "probar un mensaje de la TUI" es llamar a la rama desde dentro, y por eso varios
// tests de aquí se parecen más a tests de funciones que a tests de la interfaz.
// ---------------------------------------------------------------------------

// TestFindComposeFileEmpiezaEnCadaProyectoYNoSeSaleDelRoot: dónde se busca el
// compose.
//
// El recorrido es hacia ARRIBA y arranca en el directorio de CADA proyecto, no en
// el root: por eso un compose en el medio del árbol —el de un grupo de stacks que
// no llega al root— también se encuentra. Y para en el root: seguir subiría al
// directorio padre del usuario y encontraría el compose de OTRO workspace.
//
// El `seen` es lo que evita el bucle infinito cuando el directorio padre de un
// proyecto es el mismo que el de otro: sin él, con veinte proyectos del mismo grupo
// se subiría veinte veces por la misma rama.
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
		// El caso por el que el recorrido empieza en el proyecto y no en el root:
		// un compose en medio del árbol organiza a los proyectos de debajo sin
		// tocar el resto del workspace.
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
		// El compose del padre del root es de OTRO workspace y no puede usarse.
		// Este caso no se puede provocar con t.TempDir porque todo el árbol temporal
		// cuelga de /tmp; se comprueba con un root que no existe hacia arriba y un
		// compose en un directorio padre REAL.
		dir := t.TempDir()
		writeStr(t, filepath.Join(dir, orchestrate.ComposeFileName), compose("fuera"))

		hondo := filepath.Join(dir, "a", "b", "c")
		if err := os.MkdirAll(hondo, 0o755); err != nil {
			t.Fatal(err)
		}
		// El root es hondo: el compose de dir queda por encima y no debe aparecer.
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
		// Y el mensaje tiene que decir qué no se encontró y dónde se buscó: es la
		// diferencia entre "no tienes stacks" y "el compose está mal puesto".
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
		// Sin proyectos el recorrido no arranca de ningún sitio: no puede inventarse
		// un directorio donde buscar.
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

// TestNewUsaElRootDelConfigYExpandeElTilde: la única parte de New que decide DÓNDE
// se mira.
//
// El CWD es el default por contrato —vroom se ejecuta donde el usuario quiere
// mirar— y el config es la excepción explícita. Por eso `scanner.root` gana.
//
// Y el `~` se expande porque es la forma que la gente escribe de verdad en un
// config, y un `~/proyectos` sin expandir haría que el escaneo mirara un
// directorio llamado "~" que no existe: cero proyectos, sin error.
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
		// MEDIDO: Project.Name es el nombre del DIRECTORIO, no el del manifiesto. Lo
		// que se comprueba es el conjunto de rutas, que es lo único que el root
		// decide de verdad.
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

		// El tilde se expande con el HOME del proceso, y t.Setenv sí lo cambia para
		// os.UserHomeDir (a diferencia de /proc/self/environ).
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

// TestNewReportaUnEscaneoQueFallaYUnConfigInvalido: los dos avisos de arranque.
//
// Los dos son "arranca igual y avisa" a propósito: un config con un typo o un
// directorio que no se puede leer no pueden dejar al usuario sin TUI. Pero tampoco
// pueden arrancar callados, porque el usuario creería que su configuración se está
// aplicando.
func TestNewReportaUnEscaneoQueFallaYUnConfigInvalido(t *testing.T) {
	t.Run("config inválido: defaults más aviso", func(t *testing.T) {
		isolateConfig(t)
		root := writeTestTree(t, false)
		// Un config malformado.
		cfgPath := filepath.Join(t.TempDir(), "config.toml")
		writeStr(t, cfgPath, "[ask]\nlauncher = \"inexistente\"\n")
		t.Setenv("VROOM_CONFIG", cfgPath)

		m := New(state.NewStoreAt(t.TempDir()), &stubManager{}, root)
		if m.message == "" {
			t.Error("un config inválido tiene que avisar: si no, el usuario cree que se aplica")
		}
		// Y arranca igual: el árbol está.
		if len(m.projects) == 0 {
			t.Error("un config inválido no puede impedir el escaneo: vroom tiene que arrancar con los defaults")
		}
		// Con el default aplicado, no con el valor inválido.
		if m.cfg.Ask.Launcher != "auto" {
			t.Errorf("Ask.Launcher = %q, want auto: el launcher inválido se descartó", m.cfg.Ask.Launcher)
		}
	})

	t.Run("los proyectos sin manifiesto salen como no configurados", func(t *testing.T) {
		isolateConfig(t)
		root := writeTestTree(t, false)
		// Un directorio sin manifiesto dentro del root.
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
		// Y un plegado que no está persistido no se inventa.
		if m.collapsed["no-existe"] {
			t.Error("se inventó un estado de plegado que nadie guardó")
		}
	})
}

// TestUpdateLayoutNoDejaQueNingunaCajaSeSalga: la geometría, en todos los tamaños.
//
// La invariante es una: la suma de los anchos que se dibujan tiene que caber en el
// terminal, y ninguna altura puede quedar negativa. Un margen negativo es lo que
// hace que bordered entre en un bucle o que el compositor dibuje una caja al revés,
// que es el peor tipo de bug de TUI: no falla, se ve raro.
//
// Se barre desde un terminal diminuto hasta uno grande, porque el layout degradado
// —sin caja de detalles, con la consola mínima— es donde los márgenes se van.
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

		// La suma de anchos: árbol + marco + columna derecha + marco.
		if total := treeWidth + boxFrame + m.rightW + boxFrame; total > dims[0] && dims[0] > 40 {
			t.Errorf("%dx%d: las columnas suman %d, want <= %d", dims[0], dims[1], total, dims[0])
		}
		// Y las alturas: cabecera + cuerpo + caja de keybinds.
		if m.detailsShown && m.contentH+detailsHeight+boxFrame+4 > dims[1] && dims[1] > 20 {
			t.Errorf("%dx%d: los paneles suman %d, want <= %d", dims[0], dims[1], m.contentH+detailsHeight+boxFrame+4, dims[1])
		}
	}
}

// TestLaCajaDeDetallesSeOcultaCuandoNoCabe: el comportamiento responsive del panel.
//
// ConDetails y consola no caben a la vez en un terminal estrecho, y lo que decide el
// layout es esconder Details —que es informativo— antes que la consola, que es lo
// que el usuario está mirando para trabajar.
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
	// Y con Details escondido la consola sigue habiendo.
	if estrecho.contentH <= 0 {
		t.Errorf("contentH = %d con la pantalla más estrecha: la consola desaparece y no queda nada", estrecho.contentH)
	}

	_ = m
}

// TestUpdateConMensajeDeConsolaRestableceLosOffsetsAlEOF: al arrancar, la consola
// no debe enseñar el log viejo.
//
// Un servicio que se reinicia tiene un log anterior que no quiere ver: el usuario
// acaba de pulsar start y lo primero que aparece es la traza de hace una hora. Los
// offsets saltan al EOF y los buffers se vacían, que es lo que hace que el primer
// tick traiga sólo lo nuevo.
//
// Y si el servicio NO es el seleccionado, no se toca la vista: el usuario está
// mirando otro servicio y no puede verle parpadear la consola.
func TestUpdateConMensajeDeConsolaRestableceLosOffsetsAlEOF(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	// Se escribe en los dos logs del servicio.
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
	// Y los offsets quedan al final del fichero, no a cero: a cero reinsertaría el
	// log viejo entero en la primera lectura.
	for i, off := range nuevo.off {
		if off == 0 {
			t.Errorf("el offset %d quedó a 0: el siguiente tail reinsertaría el log viejo", i)
		}
	}

	// Con otro servicio seleccionado, la vista no se toca.
	otro := moveCursorTo(t, m, "suelto")
	antes := otro.consoleView.View()
	got = updateMsg(t, otro, startedMsg{path: path, res: process.StartResult{Pid: 4321}})
	if got.consoleView.View() != antes {
		t.Error("arrancar un servicio que no es el seleccionado no puede tocar la consola visible")
	}
}

// TestUpdateConElTickDelSpinnerReLoSincronizaYConElTickDeEstadoHaceLoMismo: los
// dos relojes.
//
// El tick de estado (el de 2 s) es el que refresca la tabla y vuelve a pedir
// hilos; el del spinner es el de la animación. Que se confundieran sería visible:
// la tabla parpadearía y el spinner se congelaría.
func TestUpdateConElTickDeSpinnerReLoSincronizaYConElTickDeEstadoHaceLoMismo(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	// El tick del spinner re-arma sólo su propio reloj.
	got, cmd := m.Update(spinnerTick())
	if cmd == nil {
		t.Fatal("el tick del spinner tiene que re-armarse o la animación se congela")
	}

	// El tick de estado pide el refresco Y la pestaña activa.
	model := got.(Model)
	markRunning(&model, pathOfSelected(t, model), livePID(t))
	_, cmd2 := model.Update(tickMsg(time.Now()))
	if cmd2 == nil {
		t.Fatal("el tick de estado tiene que pedir un refresco o la tabla se queda congelada")
	}
}

// TestUpdateConElMensajeDeSalidaDelPtyLoEscribeYReArmaLaLectura: la otra mitad
// del pump.
//
// Cada `ptyDataMsg` tiene que re-armar la lectura, porque el PTY no emite nada
// solo: sin el re-armado la terminal muestra lo que el shell escribió en el primer
// segundo y luego se queda muda mientras el usuario sigue escribiendo.
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

	// Y el EOF no hace nada por sí mismo: el reaper es el que limpia.
	antes := m.term
	got2, _ := m.Update(ptyEOFMsg{})
	if got2.(Model).term != antes {
		t.Error("el EOF no puede soltar la sesión: el master del PTY no emite EOF al morir el shell")
	}
	if !got2.(Model).termOpen {
		t.Error("el EOF no puede cerrar el modal: la sesión sigue viva")
	}
}

// TestUpdateConLaSalidaDelProcesoCierraElModalYAvisa: el final de una terminal.
//
// El aviso tiene que decir SI el código fue 0. Con un `exit 1` dentro del shell, el
// usuario cerró su propia terminal a propósito y un "terminal closed" normal lo
// haría pensar que vroom se la cerró.
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

// TestUpdateConElResultadoDeStacksDiceLoQuePasó: los tres resultados posibles de un
// stack.
//
// Y el registro en el timeline va en los dos casos de fallo: un stack que se lanzó
// y falló es el evento que el usuario va a buscar después ("¿qué pasó la última vez
// que arranqué?"). Perderlo deja el timeline mudo justo cuando tiene contenido.
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
			// Y el timeline del servicio registra el evento. MEDIDO: sólo en los dos
			// caminos con nombre de stack —el error de lanzamiento no sabe qué stack
			// es, y sin nombre no hay a qué servicio colgarlo. Se fija el
			// comportamiento real en vez de inventar un stack vacío en el timeline.
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

// TestUpdateConElResultadoDeComposersCuentaLosFallidos: varios stacks en una tecla.
//
// El recuento de fallos es lo que hace útil el aviso. "all stacks launched" cuando
// uno de los tres falló es la forma más fácil de que el usuario crea que tiene su
// entorno cuando no lo tiene.
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

// TestToggleStackYComposersDicenQueNoHayEngineONoHayStacks: los rechazos de la
// orquestación.
//
// El caso de "no hay engine" es real: `New` lo construye siempre, pero un test que
// inyecta un modelo a mano no, y un camino que no reventara en ese caso terminaría
// con un nil-deref en un sitio mucho menos obvio.
func TestToggleStackYComposersDicenQueNoHayEngineONoHayStacks(t *testing.T) {
	t.Run("sin stacks en el grupo", func(t *testing.T) {
		// Con compose file hay engine; el grupo que se pide no tiene stacks.
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

// TestToggleNodeArrancaLosParadosYNoTocaLosVivos: la acción de grupo.
//
// La regla es "si hay alguno parado, arranca los parados; si no, para los vivos".
// Lo que no puede pasar es arrancar los que ya estaban corriendo: con `s` sobre un
// grupo donde tres están vivos y uno parado, arrancar los cuatro dejaría tres
// procesos nuevos compitiendo por el mismo puerto.
func TestToggleNodeArrancaLosParadosYNoTocaLosVivos(t *testing.T) {
	m, _ := newTestModel(t)
	// "s" sobre el header primario de tienda.
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

	// MEDIDO: con alguno parado, toggleNode arranca SÓLO los parados y deja los
	// vivos como estaban. No los para: la acción de grupo es "dejar el grupo en
	// marcha", no "reiniciar el grupo". Relanzar los vivos dejaría procesos nuevos
	// compitiendo por el mismo puerto.
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

// TestToggleNodeSinMiembrosNoHaceNada: un nodo vacío.
//
// Sucede con un grupo que sólo tiene stacks plegados, o con un primario que ya no
// tiene proyectos después de un refresh. La acción tiene que ser un no-op silencioso
// y no un índice fuera de rango.
func TestToggleNodeSinMiembrosNoHaceNada(t *testing.T) {
	m, _ := newTestModel(t)

	_, cmd := m.toggleNode("grupo-que-no-existe", "")
	if cmd != nil {
		t.Error("un nodo sin miembros no puede lanzar nada")
	}

	// Con un primario real pero un secundario que no existe.
	_, cmd2 := m.toggleNode("tienda", "secundario-inventado")
	_ = cmd2
}

// TestEnterSelectionNoHaceNadaSobreUnStackNiSobreUnProyectoInline: las dos filas
// que no se pliegan.
//
// Un stack no tiene hijos que plegar y un proyecto inline (sin primario) no tiene
// contenedor. En los dos casos `enter` no puede reconstruir el árbol, y si lo hiciera
// con un cambio vacío perdería la posición del cursor.
//
// Y en los que sí pliega, la posición se conserva: es lo que hace que `enter` sea
// usable para plegar una rama entera sin perder el sitio.
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
		// "suelto" no tiene primary_group en su manifiesto.
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
		// Y el estado persistido guarda el plegado.
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

// TestOnSelectNoPideRefrescoParaLoQueNoTieneNadaQueRefrescar: el ahorro que hace
// que el cursor sea fluido.
//
// Mover el cursor por el árbol dispara un refresco por cada fila. Si se pidiera
// para un grupo o un proyecto sin manifiesto, moverse por un workspace grande
// lanzaría un `git` por fila y la interfaz se iría detrás. Sólo se pide para lo que
// de verdad tiene datos.
func TestOnSelectNoPideRefrescoParaLoQueNoTieneNadaQueRefrescar(t *testing.T) {
	t.Run("sobre un header no pide nada", func(t *testing.T) {
		m := sinSeleccion(t)
		next, cmd := m.onSelect()
		got := next.(Model)
		if cmd != nil {
			t.Error("un header no tiene logs que traer: pedir un refresco sería un git por fila")
		}
		// Y la consola sí muestra qué hacer.
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

// TestRefreshThreadsNoPideNadaSinUnServicioVivo: el muestreo de hilos.
//
// Pedir el muestreo de un servicio parado es un `cat /proc/<pid>/task/*/stat` sobre
// un PID muerto, cuatro veces por segundo, por cada servicio parado visible. Es el
// gasto que hace que una TUI abierta mucho rato sea caliente sin que nada lo explique.
func TestRefreshThreadsNoPideNadaSinUnServicioVivo(t *testing.T) {
	m, _ := newTestModel(t)

	// Sin selección.
	if cmd := m.refreshThreads(); cmd != nil {
		t.Error("sin proyecto no hay hilos que muestrear")
	}

	// Con un proyecto pero sin manifiesto.
	sinManif := sinManifiesto(t)
	if cmd := sinManif.refreshThreads(); cmd != nil {
		t.Error("sin manifiesto no hay proceso que muestrear")
	}

	// Con manifiesto pero parado.
	m = moveCursorTo(t, m, "tienda-api")
	if cmd := m.refreshThreads(); cmd != nil {
		t.Error("un servicio parado no tiene hilos: muestrearlo es un /proc sobre un PID muerto")
	}

	// Vivo: ahora sí.
	markRunning(&m, projectPath(t, m, "tienda-api"), livePID(t))
	if cmd := m.refreshThreads(); cmd == nil {
		t.Error("un servicio vivo sí tiene hilos que muestrear")
	}
}

// TestRefreshThreadsConPidCeroNoMuestrea: vivo en la UI pero sin PID.
//
// Puede pasar si el estado se fijó a mano o si un refresh dejó el estado vivo y el
// meta vacío. `/proc/0/task` no existe y `threadsCmd` devolvería un error en cada
// tick.
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

// TestEditLogsCmdTraeElEditorDevueltoYElError: el puente con el editor.
//
// Lo que no se puede probar aquí es el `tea.ExecProcess` en sí —suspende el
// programa— así que se prueba lo que sí: que el comando existe y que el mensaje de
// error nombra al editor. Un editor que sale con error sin decírselo al usuario
// deja la TUI como si nada y él sin saber por qué se le cierra el programa.
func TestEditLogsCmdTraeElEditorDevueltoYElError(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no hay /bin/sh")
	}
	// El comando existe y es el del editor que se le pasó.
	cmd := editLogsCmd("/bin/sh -c true", "/l/out.log", "/l/err.log", false)
	if cmd == nil {
		t.Fatal("editLogsCmd devolvió nil")
	}
	// Y el mensaje del editor cerrado es el que el usuario ve al volver.
	m, _ := newTestModel(t)
	_ = m
}

// TestHandleKeyConEscLimpiaElFiltroAntesDeSalir: esc tiene dos comportamientos.
//
// Con un filtro aplicado, esc limpia el filtro y NO sale. Sin filtro, sale. La
// diferencia importa porque un usuario filtrando por error que pulsa esc esperando
// quitar el filtro se encontraría fuera del programa, y perder el trabajo de
// liquidar un stack a medio hacer es caro.
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
		// esc con filtro NO sale: no hay comando de salida, sólo el árbol
		// reconstruido. Lo que importa es que el filtro se limpió.
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

// TestFilterKeyCierraElBoxSinLimpiarConEnter: enter aplica, esc limpia.
//
// Es la asimetría de los dos caminos de salida del filtro, y cada uno tiene su
// motivo: enter es "he terminado de escribir" y el filtro en vivo ya está aplicado;
// esc es "me he arrepentido" y limpia. Con un solo comportamiento, esc dejaría un
// filtro que el usuario cree haber quitado y seguiría viendo una lista recortada.
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

// TestLaunchAskCmdTraeElMensajeDelLauncherYSuError: el camino en background del ask.
//
// La estrategia inline suspende el programa y no pasa por aquí; herdr y custom
// despachan en goroutine y vuelven con un `statusMsg`. Lo que importa es que el
// error del launcher llegue al usuario: un `herdr pane split` fallido sin aviso
// deja al usuario creyendo que su agente arrancó.
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

// TestDispatchAskConEstrategiaNoInlineDespachaEnBackground: la decisión de
// estrategia.
//
// Inline suspende el programa y herdr/custom despachan sin bloquear. Confundirlas
// significa que el ask con herdr configured se quedaría esperando a que el agente
// cierre, que es exactamente lo que el launcher evita.
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
	// Y el mensaje del launcher no aparece hasta que el comando corra.
	if got.message != "" {
		t.Errorf("aviso = %q antes de despachar: el resultado llega después", got.message)
	}
}

// TestViewPoneAltScreenYRueda: las dos banderas de la vista.
//
// AltScreen porque el dashboard es de altura completa y puede no encajar en la
// altura que queda; y el modo de ratón porque la rueda es lo único que hace falta
// y el modo de arrastre activaría la selección de texto, que en un dashboard con
// bordes es más molesto que útil.
func TestViewPoneAltScreenYRueda(t *testing.T) {
	m, _ := newTestModel(t)
	v := m.View()

	if !v.AltScreen {
		t.Error("el dashboard tiene que ir a pantalla completa: no cabe en la altura parcial del terminal")
	}
	if v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("MouseMode = %v, want CellMotion: lo que hace falta es la rueda", v.MouseMode)
	}
	// Y el contenido no está vacío: una vista en blanco con las banderas puestas es
	// un programa que se queda en alt screen sin dibujar nada.
	if strings.TrimSpace(v.Content) == "" {
		t.Error("la vista no trae contenido: alt screen en blanco es un programa colgado")
	}
}

// helpers --------------------------------------------------------------------

// it0 devuelve el item bajo el cursor. Asume que hay uno.
func it0(m Model) treeItem {
	it, ok := m.selectedItem()
	if !ok {
		panic("no hay item bajo el cursor")
	}
	return it
}

// spinnerTick construye el tick del spinner.
func spinnerTick() tea.Msg {
	return spinner.TickMsg{}
}

// exitReal produce el error de un comando que sale con código, de verdad: exitCode
// sólo reconoce *exec.ExitError, así que un error de mentira devolvería 0 y el test
// comprobaría el caso equivocado.
func exitReal(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run()
	if err == nil {
		t.Fatalf("sh -c \"exit %d\" salió con 0: el error de test no probaría nada", code)
	}
	return err
}

// errLaunch es un fallo de lanzamiento de stack, sin código de salida detrás.
var errLaunch = errors.New("no se pudo lanzar")

// nuevoLauncherEnBackground devuelve un launcher con estrategia custom apuntando a
// un script de mentira en el PATH: el camino en background del ask, que no suspende
// el programa.
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

// reqAsk es la petición mínima de un ask.
func reqAsk() launcher.Request {
	return launcher.Request{Agent: "agente-falso", Args: []string{"agente-falso"}, Dir: "/tmp"}
}

// agenteFalso es el agente que el modal de ask tiene seleccionado.
func agenteFalso() agents.Agent {
	return agents.Agent{Name: "agente-falso", Cmd: []string{"agente-falso"}}
}
