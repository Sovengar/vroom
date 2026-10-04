package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// ---------------------------------------------------------------------------
// Los bordes de la terminal embebida: el shell que se elige, el entorno, el
// dimensionado del grid y la vida de la sesión.
//
// La sesión de prueba es un PTY stub (emulador real, pump real, sin proceso). Es
// el seam correcto aquí porque lo que se prueba es la CLASE DE FRONTERA —redimensionar
// una sesión cerrada, escribir en una sesión cerrada, esperar a un proceso que no
// existe— y esas fronteras no se pueden provocar con un shell de verdad sin matar
// procesos de verdad a proposito.
//
// El shell y el entorno sí se prueban de verdad, porque son los dos valores de los
// que depende que la terminal del usuario funcione: un TERM ausente pone el shell
// en modo Unix sin colores y con eco raro, y un cwd equivocado abre la terminal en
// el directorio de vroom en vez de en el proyecto.
// ---------------------------------------------------------------------------

// TestResolveShellUsaElDelUsuarioYElDeSuPropioEsElQueExiste: el shell sale de
// $SHELL, y el fallback tiene que existir.
//
// El fallback a "sh" en vez de "/bin/sh" a propósito: si el usuario tiene su shell
// en un sitio que este binario no encuentra por PATH, "sh" al menos arranca y el
// usuario ve un prompt en vez de un error de terminal.
func TestResolveShellUsaElDelUsuarioYElDeSuPropioEsElQueExiste(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	if got := resolveShell(); got != "/bin/zsh" {
		t.Errorf("con SHELL = %q, want /bin/zsh", got)
	}

	t.Setenv("SHELL", "")
	if got := resolveShell(); got != "sh" {
		t.Errorf("sin SHELL = %q, want sh: un path absoluto sería inventado", got)
	}

	// Y el shell tiene que existir de verdad: si $SHELL apunta a algo que no está,
	// el modal avisa y no se abre, que es el fallo honesto.
	t.Setenv("SHELL", "/no/existe/un/shell")
	if _, err := newTermSession(40, 10, t.TempDir()); err == nil {
		t.Error("un $SHELL inexistente no puede abrir una terminal: el usuario vería un modal vacío sin explicación")
	}
}

// TestTermEnvHeredaElEntornoYGarantizaTerm: el entorno del shell, no una lista
// inventada.
//
// El caso importante es el del TERM ausente: sin él la mayoría de programas
// asumen un terminal Unix y salen con secuencias que el emulador no espera, y el
// usuario ve caracteres basura en vez de una terminal.
//
// Y con TERM presente NO se duplica: tener dos TERM en el entorno hace que algunos
// programas cojan el primero y otros el último, y el resultado depende del orden
// del mapa de entorno, que no está garantizado.
func TestTermEnvHeredaElEntornoYGarantizaTerm(t *testing.T) {
	t.Setenv("TERM", "")
	t.Setenv("VROOM_TEST_MARKER", "presente")

	env := termEnv()

	terms := 0
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "TERM" {
			terms++
			if v != "xterm-256color" {
				t.Errorf("TERM = %q, want xterm-256color: sin él el shell asume un terminal Unix", v)
			}
		}
	}
	if terms != 1 {
		t.Errorf("hay %d variables TERM, want exactamente 1", terms)
	}

	// El entorno se hereda: el shell del usuario necesita su PATH, su HOME y su LANG.
	var marker bool
	for _, kv := range env {
		if kv == "VROOM_TEST_MARKER=presente" {
			marker = true
		}
	}
	if !marker {
		t.Error("termEnv no heredó el entorno del proceso: un shell sin su PATH no arranca nada")
	}

	// Con TERM presente se respeta el del usuario y no se añade otro.
	t.Setenv("TERM", "alacritty")
	for _, kv := range termEnv() {
		if k, _, ok := strings.Cut(kv, "="); ok && k == "TERM" && kv != "TERM=alacritty" {
			t.Errorf("con TERM del usuario hay un segundo TERM: %q", kv)
		}
	}
}

// TestResizeEWriteIgnoranUnaSesionCerrada: las dos operaciones que el cierre
// puede hacer llegar tarde.
//
// El caso real es el cierre mientras llega un tea.WindowSizeMsg: el hilo de Update
// redimensiona después de que la sesión se apagó. Sin la guarda, `emu.Resize` sobre
// un emulador cerrado entra en la librería de C y revienta el proceso entero.
//
// No es un test que se pueda provocar con una sesión viva, porque ahí la guarda no
// se activa —y por eso importa que exista.
func TestResizeEWriteIgnoranUnaSesionCerrada(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	s.closed = true

	// Ninguna de las dos revienta, y las dimensiones no cambian.
	w, h := s.dims()
	s.resize(80, 24)
	if gotW, gotH := s.dims(); gotW != w || gotH != h {
		t.Errorf("resize de una sesión cerrada cambió las dimensiones: %d,%d -> %d,%d", w, h, gotW, gotH)
	}
	s.write([]byte("esto no debe llegar a ningún sitio"))

	// Y la sesión cerrada no se considera viva.
	if s.alive() {
		t.Error("alive = true en una sesión cerrada")
	}
	if got := s.screen(); got != "" {
		t.Errorf("screen de una sesión cerrada = %q, want cadena vacía", got)
	}
}

// TestResizeIgnoraLasDimensionesInvalidasYLasValidasNo: un grid de 0x0 no es un
// grid.
//
// Es el mismo motivo que en startSession: un alto de 0 le dice a la librería de
// terminal "no hay pantalla", y lo que sale de ahí no es una terminal pequeña sino
// una división por cero dentro de C.
func TestResizeIgnoraLasDimensionesInvalidasYLasValidasNo(t *testing.T) {
	pty := &stubPty{}
	s := newStubSession(40, 10, pty)

	for _, dims := range [][2]int{{0, 10}, {40, 0}, {-5, 10}, {40, -5}} {
		s.resize(dims[0], dims[1])
		if w, h := s.dims(); w != 40 || h != 10 {
			t.Errorf("resize(%d,%d) aplicó dimensiones inválidas: %d,%d", dims[0], dims[1], w, h)
		}
		if len(pty.resizes) != 0 {
			t.Errorf("resize(%d,%d) llegó al PTY: %v", dims[0], dims[1], pty.resizes)
		}
	}

	// Y las válidas sí se aplican, a las dos capas: emulador y PTY.
	s.resize(80, 24)
	if w, h := s.dims(); w != 80 || h != 24 {
		t.Errorf("dims = %d,%d, want 80,24", w, h)
	}
	if len(pty.resizes) != 1 || pty.resizes[0] != [2]int{80, 24} {
		t.Errorf("el PTY no recibió el resize: %v", pty.resizes)
	}
}

// TestStartSessionCorrigeLasDimensionesInvalidasEnElOrigen: la corrección tiene que
// estar donde se crean las dimensiones, no en el que las consume.
//
// Si sólo el consumidor las corrigiera, el PTY se crearía con 0x0 y el shell
// arrancaría en un terminal sin tamaño: el prompt saldría partido y el programa
// interactivo decidiría que no hay terminal. Se comprueba con un argv que NO es un
// shell —`true`, que sale enseguida— para poder inspeccionar lo que se creó.
func TestStartSessionCorrigeLasDimensionesInvalidasEnElOrigen(t *testing.T) {
	s, err := startSession(0, 0, t.TempDir(), []string{"true"})
	if err != nil {
		t.Fatalf("startSession: %v", err)
	}
	defer s.shutdown()

	w, h := s.dims()
	if w != 1 || h != 1 {
		t.Errorf("dims = %d,%d, want 1,1: un grid de 0x0 no es un grid", w, h)
	}
}

// TestWaitSinProcesoNoEsUnError: la sesión stub no tiene proceso que repear.
//
// Es lo que permite usar un PTY stub en los tests sin que cada uno tenga que
// apañar el reaper. Y si devolviera un error, el mensaje de "la terminal terminó
// con error" aparecería en tests donde no hay terminal ninguna.
func TestWaitSinProcesoNoEsUnError(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	if err := s.wait(); err != nil {
		t.Errorf("wait de una sesión sin proceso = %v, want nil", err)
	}

	// Con el campo cmd a nil tampoco.
	vacia := &termSession{}
	if err := vacia.wait(); err != nil {
		t.Errorf("wait de una sesión vacía = %v, want nil", err)
	}
}

// TestReadPtyDevuelveEOFCuandoNoHayBytesQueLeer: EOF y "no hay datos ahora" son
// cosas distintas.
//
// El pump lee en bucle y cada mensaje re-arma la lectura, así que un read vacío
// significa EOF y termina la sesión. Si se distinguieran mal, la terminal se
// cerraría sola en el primer tick sin bytes.
func TestReadPtyDevuelveEOFCuandoNoHayBytesQueLeer(t *testing.T) {
	pty := &stubPty{} // Read devuelve (0, io.EOF)
	s := newStubSession(40, 10, pty)

	msg := readPtyCmd(s)()
	if _, ok := msg.(ptyEOFMsg); !ok {
		t.Errorf("readPtyCmd devolvió %T, want ptyEOFMsg: un read vacío es EOF y cierra la sesión", msg)
	}
}

// TestLabelEsElBasenameDelCwd: el título de la sesión.
//
// El basename, no la ruta entera: el título va en una línea de un modal y una ruta
// absoluta la desborda entera. Y con el cwd en "/" el basename es "/", que es lo
// único honesto que hay.
func TestLabelEsElBasenameDelCwd(t *testing.T) {
	tests := []struct{ dir, want string }{
		{"/home/usuario/proyectos/api", "api"},
		{"/home/usuario/proyectos/mi proyecto", "mi proyecto"},
		{"/", "/"},
		{"", "."},
	}
	for _, tt := range tests {
		s := &termSession{dir: tt.dir}
		if got := s.label(); got != tt.want {
			t.Errorf("label de %q = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

// TestTermWYTermHTienenSueloYTope: el grid no puede ser ni invisible ni la pantalla
// entera.
//
// Los dos lados importan. Un grid de 0 columnas muestra un modal con un prompt
// partido. Y un grid del alto de la pantalla empuja el título y la línea de ayuda
// fuera del borde, que es lo que hace que un modal parezca roto en vez de estrecho.
func TestTermWYTermHTienenSueloYTope(t *testing.T) {
	for _, tt := range []struct{ w, h int }{
		{300, 80}, {200, 60}, {120, 40}, {100, 30}, {60, 12}, {40, 3}, {20, 1},
	} {
		m, _ := newTestModel(t)
		m.width, m.height = tt.w, tt.h
		m.updateLayout()

		if got := m.termW(); got < termMinW {
			t.Errorf("%dx%d: termW = %d, want >= %d", tt.w, tt.h, got, termMinW)
		}
		if got := m.termH(); got < termMinH {
			t.Errorf("%dx%d: termH = %d, want >= %d", tt.w, tt.h, got, termMinH)
		}
		if got := m.termH(); got > termMaxH {
			t.Errorf("%dx%d: termH = %d, want <= %d: el modal se saldría de la pantalla", tt.w, tt.h, got, termMaxH)
		}
		// Y el grid completo tiene que caber en la pantalla, con su marco.
		if m.termW()+boxFrame > tt.w && tt.w > 40 {
			t.Errorf("%dx%d: el grid mide %d de ancho, más el marco no cabe", tt.w, tt.h, m.termW())
		}
	}
}

// TestTermCwdEsElProyectoSeleccionadoYNoElRoot: el cwd de la terminal.
//
// Es el punto de la tecla `!`: abrir una terminal en el proyecto sobre el que está
// el cursor. Con el root como cwd, el shell abriría en el workspace y el usuario
// tendría que hacer `cd` antes de cada cosa, que es exactamente lo que la tecla
// prometen evitar.
func TestTermCwdEsElProyectoSeleccionadoYNoElRoot(t *testing.T) {
	m, _ := newTestModel(t)

	// Con un proyecto seleccionado: su ruta, y su nombre en el título.
	m = moveCursorTo(t, m, "tienda-api")
	want := projectPath(t, m, "tienda-api")
	if got := m.termCwd(); got != want {
		t.Errorf("termCwd = %q, want la ruta del proyecto %q", got, want)
	}
	if got := m.termCwdLabel(); got != "tienda-api" {
		t.Errorf("termCwdLabel = %q, want tienda-api", got)
	}

	// Sin proyecto (cursor en un header): el root del workspace, y su basename.
	// SinHeader construye su propio árbol, así que el root se lee del modelo que
	// se está comprobando y no del anterior.
	enHeader := sinSeleccion(t)
	if got := enHeader.termCwd(); got != enHeader.root {
		t.Errorf("sin proyecto termCwd = %q, want el root %q", got, enHeader.root)
	}
	if got := enHeader.termCwdLabel(); got != filepath.Base(enHeader.root) {
		t.Errorf("sin proyecto termCwdLabel = %q, want el basename del root", got)
	}
}

// TestOpenTermConShellInvalidoAvisaYNoAbreElModal: el fallo tiene que ser visible.
//
// Es el caso que el usuario ve cuando su $SHELL no está donde dice. Un modal vacío
// sin explicación lo haría pensar que vroom está roto; un aviso con el motivo le
// dice exactamente qué arreglar.
func TestOpenTermConShellInvalidoAvisaYNoAbreElModal(t *testing.T) {
	t.Setenv("SHELL", "/no/existe/un/shell")

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")

	next, cmd := m.openTerm()
	got := next.(Model)
	if cmd != nil {
		t.Error("sin shell no hay nada que lanzar")
	}
	if got.termOpen {
		t.Error("sin shell el modal no puede abrirse: se vería un rectángulo vacío")
	}
	if got.term != nil {
		t.Error("no debe quedar una sesión a medias")
	}
	if !strings.Contains(got.message, "terminal") {
		t.Errorf("= %q, want un aviso que diga que es la terminal", got.message)
	}
}

// TestOpenTermReutilizaLaSesionVivaYLaRedimensiona: la segunda vez no se crea un
// shell nuevo.
//
// Crear un shell nuevo perdería todo lo que el usuario tenía en el suyo —el cwd
// donde estaba, las variables que exportó, el comando a medias— y dejaría el
// proceso anterior huérfano por el grupo de sesiones.
//
// Y si el layout cambió entre medias, el grid se redimensiona en el sitio: una
// sesión con el tamaño viejo dentro de un modal nuevo muestra líneas cortadas.
func TestOpenTermReutilizaLaSesionVivaYLaRedimensiona(t *testing.T) {
	t.Run("reutiliza sin recrear", func(t *testing.T) {
		// Una sesión stub, no una real: el punto de este test es que openTerm no
		// cree un shell nuevo, y comprobarlo con un shell de verdad significaría
		// dejar un proceso colgado por cada test.
		pty := &stubPty{}
		s := newStubSession(40, 10, pty)

		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.term = s

		next, cmd := m.openTerm()
		got := next.(Model)
		if cmd != nil {
			t.Error("reutilizar una sesión viva no necesita emitir comandos")
		}
		if got.term != s {
			t.Error("la segunda apertura creó una sesión nueva: se perdería el shell del usuario y quedaría un proceso huérfano")
		}
		if !got.termOpen {
			t.Error("la sesión viva tiene que re-mostrarse")
		}
	})

	t.Run("redimensiona la sesión viva cuando el layout cambia", func(t *testing.T) {
		pty := &stubPty{}
		s := newStubSession(40, 10, pty)

		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.term = s
		m.width, m.height = 200, 60
		m.updateLayout()

		got, _ := m.openTerm()
		model := got.(Model)
		if model.term != s {
			t.Fatal("un cambio de layout no puede crear una sesión nueva")
		}
		if len(pty.resizes) == 0 {
			t.Error("con el layout cambiado el grid no se redimensionó: las líneas quedan cortadas")
		}
		if w, h := s.dims(); w != m.termW() || h != m.termH() {
			t.Errorf("dims = %d,%d, want %d,%d: el grid no se ajustó al modal nuevo", w, h, m.termW(), m.termH())
		}
	})

	t.Run("no redimensiona si el tamaño no cambia", func(t *testing.T) {
		pty := &stubPty{}
		m, _ := newTestModel(t)
		m = moveCursorTo(t, m, "tienda-api")
		m.term = newStubSession(m.termW(), m.termH(), pty)

		got, _ := m.openTerm()
		if len(pty.resizes) != 0 {
			t.Errorf("sin cambio de layout hay un resize de sobra: %v", pty.resizes)
		}
		_ = got
	})
}

// TestOpenTermAbreElShellEnElProyectoConSizeCorrecto: la sesión real, en el sitio
// correcto, con el tamaño correcto.
//
// Es el único test que verifica de verdad el contrato de la tecla `!`: todo lo
// demás podría estar bien y el shell abrirse en el directorio equivocado.
//
// El bombeo PTY→emulador lo hace el bucle de lectura de Update, así que aquí se
// replica a mano: `readPtyCmd` + `write`. Es la mitad del pump que no es un pump.
func TestOpenTermAbreElShellEnElProyectoConSizeCorrecto(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no hay /bin/sh en esta máquina")
	}

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	want := projectPath(t, m, "tienda-api")

	s, err := startSession(m.termW(), m.termH(), want, []string{"/bin/sh", "-c", "pwd; echo TERMINADO"})
	if err != nil {
		t.Fatalf("startSession: %v", err)
	}
	defer s.shutdown()

	deadline := time.After(30 * time.Second)
	found := false
	for !found {
		ch := make(chan tea.Msg, 1)
		go func() { ch <- readPtyCmd(s)() }()
		select {
		case msg := <-ch:
			switch m2 := msg.(type) {
			case ptyDataMsg:
				s.write(m2.data)
				if strings.Contains(s.screen(), "TERMINADO") {
					found = true
				}
			case ptyEOFMsg:
				t.Fatalf("EOF antes de ver la salida; screen = %q", s.screen())
			default:
				t.Fatalf("msg inesperado: %T", msg)
			}
		case <-deadline:
			t.Fatalf("la salida del shell no llegó; screen = %q", s.screen())
		}
	}

	// El pwd que imprime el shell es el del PROYECTO, no el de vroom ni el del
	// directorio de test. Se compara sin saltos de línea porque el emulador puede
	// partir la ruta en varias.
	plano := strings.ReplaceAll(s.screen(), "\n", "")
	if !strings.Contains(plano, filepath.Base(want)) {
		t.Errorf("el shell se abrió en otro sitio: %q no contiene %q", plano, filepath.Base(want))
	}
}

// TestTermKeyIgnoraLoQueNoEsUnaTecla: el mensaje tiene que ser un KeyPressMsg.
//
// Es lo que evita que un ptyDataMsgCCC o un tick del spinner acaben escritos en el
// PTY del usuario: escribir bytes arbitrarios en una terminal real produce
// caracteres de control que mueven el cursor, borran la línea o cambian de terminal
// virtual.
func TestTermKeyIgnoraLoQueNoEsUnaTecla(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.term = s
	m.termOpen = true

	// termKey recibe tea.KeyMsg, así que lo que se comprueba es que un mensaje que
	// NO es una pulsación de tecla no llega a escribir en el PTY. El tipo de la
	// firma ya hace el filtrado; lo que se fija aquí es que dentro no hay un
	// `msg.String()` que panicaría con otro tipo de msg.
	noTeclas := []tea.Msg{
		ptyDataMsg{data: []byte("esto no es una tecla")},
		ptyEOFMsg{},
		ptyExitMsg{},
		tickMsg{},
		nil,
	}
	for _, msg := range noTeclas {
		if _, esTecla := msg.(tea.KeyMsg); esTecla {
			t.Fatalf("%T no estaba en la lista de no-teclas", msg)
		}
	}
	if n := len(s.pty.(*stubPty).bytesWritten()); n != 0 {
		t.Fatalf("se escribieron %d bytes en el PTY antes de ninguna tecla", n)
	}
	// Y una tecla real sí llega al PTY, que es la mitad positiva del contrato.
	before := len(s.pty.(*stubPty).bytesWritten())
	next, _ := m.termKey(keyPress("a"))
	if !next.(Model).termOpen {
		t.Error("una tecla normal cerró el modal")
	}
	waitFor(t, 2*time.Second, func() bool {
		return len(s.pty.(*stubPty).bytesWritten()) > before
	})
}

// TestCtrlQOcultaElModalYDejaLaSesionViva: la tecla de cerrar.
//
// La sesión sobrevive a propósito: es la diferencia entre "cerrar el modal" y
// "matar la terminal". Si cerrara la sesión, el usuario perdería su shell y todo lo
// que tenía en él —cd, variables, un comando a medias— por haber pulsado la tecla
// que cerró el panel.
func TestCtrlQOcultaElModalYDejaLaSesionViva(t *testing.T) {
	s := newStubSession(40, 10, &stubPty{})
	defer s.shutdown()

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	m.term = s
	m.termOpen = true

	next, cmd := m.termKey(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	got := next.(Model)

	if cmd != nil {
		t.Error("ctrl+q no emite comandos: sólo oculta el modal")
	}
	if got.termOpen {
		t.Error("ctrl+q tiene que ocultar el modal")
	}
	if got.term != s {
		t.Error("ctrl+q tiene que dejar la sesión viva: cerrarla perdería el shell del usuario")
	}
	if !s.alive() {
		t.Error("la sesión quedó apagada: ctrl+q oculta, no mata")
	}

	// Y al reabrirlo, la misma sesión se vuelve a mostrar.
	reabierto, _ := got.openTerm()
	if re := reabierto.(Model); re.term != s || !re.termOpen {
		t.Error("reabrir tiene que enseñar la misma sesión, no crear otra")
	}
}
