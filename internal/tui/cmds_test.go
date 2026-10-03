package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// Los tea.Cmd de app.go y las funciones puras que los acompaña.
//
// Todos devuelven un Cmd —una función que devuelve un Msg— así que todos son
// ejecutables en un test sin Bubbletea: se invoca el Cmd y se mira el Msg. Eso es
// lo que hace falta para poder afirmar qué le dice la TUI al runtime.
//
// Y el msg es la AFIRMACIÓN que la vista va a renderizar. Un `refreshedMsg` con
// status equivocado produce una TUI que dice "corriendo" sobre un servicio
// parado, y eso no se ve hasta que se mira el msg, no la función.
// ---------------------------------------------------------------------------

// TestStateOfMetaTraduceAlVocabularioDeProceso: el estado persistido y el de
// proceso comparten nombres A PROPÓSITO, y el translate vive en un solo sitio.
//
// Que el string crudo se convierta en Status es lo que evita que se separen: si
// un estado nuevo se publica sin pasar por aquí, la TUI lo trataría como
// desconocido y el servicio aparecería como parado. El caso "" es el que obliga a
// ello: un Meta sin estado no es un servicio en ningún estado, y "unknown" lo
// dice sin afirmar que esté parado.
func TestStateOfMetaTraduceAlVocabularioDeProceso(t *testing.T) {
	tests := []struct {
		in   state.Meta
		want process.Status
	}{
		{state.Meta{}, process.StatusUnknown},
		{state.Meta{State: state.StateRunning}, process.StatusRunning},
		{state.Meta{State: state.StateStopped}, process.StatusStopped},
		{state.Meta{State: state.StatePortPending}, process.StatusPortPending},
		{state.Meta{State: state.StatePortUnresolved}, process.StatusPortUnresolved},
		{state.Meta{State: state.StateNoPort}, process.StatusNoPort},
		{state.Meta{State: "inventado"}, process.Status("inventado")},
	}
	for _, tt := range tests {
		if got := stateOfMeta(tt.in); got != tt.want {
			t.Errorf("stateOfMeta(State=%q) = %q, want %q", tt.in.State, got, tt.want)
		}
	}
}

// TestInitDevuelveLosCincoRelojes: Init arranca el polling, el tail de consola y
// los dos spinners.
//
// Los cinco, y no "alguno": el polling y el tail son relojes DISTINTOS con
// intervalos distintos (2s el estado, 400ms la consola) porque sirven cosas
// distintas, y perder uno deja la TUI showing datos viejos sin que nada falle
// visiblemente. El conteo es la afirmación.
func TestInitDevuelveLosCincoRelojes(t *testing.T) {
	m, _ := newTestModel(t)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init devolvió nil: sin relojes la TUI no se actualiza nunca")
	}

	msgs := collectBatch(t, cmd)
	// El Msg no lleva identidad de qué reloj es, así que lo que se comprueba es
	// que hay AL MENOS los cinco latidos y que el tipo del tick de estado trae
	// su marca de tiempo (que es lo que distingue un reloj de otro).
	if len(msgs) < 5 {
		t.Errorf("Init produjo %d mensajes, want al menos 5 (polling, consola y dos spinners)", len(msgs))
	}
	var sawTick, sawConsoleTick bool
	for _, msg := range msgs {
		switch v := msg.(type) {
		case tickMsg:
			sawTick = true
			if time.Time(v).IsZero() {
				t.Error("el tick de estado no lleva su marca de tiempo")
			}
		case consoleTickMsg:
			sawConsoleTick = true
			if time.Time(v).IsZero() {
				t.Error("el tick de consola no lleva su marca de tiempo")
			}
		}
	}
	if !sawTick {
		t.Error("Init no arrancó el reloj de estado: la TUI mostraría estados rancios para siempre")
	}
	if !sawConsoleTick {
		t.Error("Init no arrancó el reloj de consola: los logs no se moverían sin recargar")
	}
}

// TestTickCmdDevuelveSuMsg: los dos relojes sueltos, ejecutados.
//
// Se ejecutan de verdad, con su espera. Son 2s y 400ms, y la prueba de que un
// reloj devuelve SU mensaje (y no otro, ni uno con otro nombre) tiene que
// pagar ese coste.
func TestTickCmdDevuelveSuMsg(t *testing.T) {
	if msg := runCmd(tickCmd()); !isTickOfState(msg) {
		t.Errorf("tickCmd devolvió %T, want tickMsg", msg)
	}
	if msg := runCmd(consoleTickCmd()); !isConsoleTick(msg) {
		t.Errorf("consoleTickCmd devolvió %T, want consoleTickMsg", msg)
	}
}

// TestThreadsCmdMuestreaElPidYPropagaElError: el muestreo de hilos es
// informativo, y su fallo tiene que LLEGAR en el msg y no en un panic.
//
// El caso que importa es el PID muerto: threadsCmd se lanza desde el tick para
// cualquier servicio corriendo, y entre el Evaluate y el muestreo el proceso
// puede morir. Un error aquí es routine, no una excepción.
func TestThreadsCmdMuestreaElPidYPropagaElError(t *testing.T) {
	// Un PID que no existe: el msg trae el error y el modelo lo tolera.
	msg := runCmd(threadsCmd("/tmp/x", 0))
	th, ok := msg.(threadsMsg)
	if !ok {
		t.Fatalf("threadsCmd devolvió %T", msg)
	}
	if th.path != "/tmp/x" {
		t.Errorf("path = %q, want /tmp/x: sin el path el msg no se puede atribuir a un servicio", th.path)
	}
	if th.err == nil {
		t.Error("un PID inexistente debería venir con error: si no, la UI mostraría cero hilos como un hecho")
	}

	// Un proceso real sí trae hilos, y sin error.
	msg = runCmd(threadsCmd("/tmp/x", livePID(t)))
	th = msg.(threadsMsg)
	if th.err != nil {
		t.Errorf("un proceso real no debería dar error de muestreo: %v", th.err)
	}
	if len(th.threads) == 0 {
		t.Error("un proceso vivo tiene al menos un hilo: un lista vacía haría pensar que está muerto")
	}
}

// TestRefreshCmdRecorreSoloLosConfiguradosYAtribuyeElMotivo: el polling le
// repregunta al store a cada servicio configurado, y el resultado dice POR QUÉ
// lo que dice.
//
// Las cuatro ramas del switch sobre el error del store son cuatro hechos
// distintos —nunca arrancado, meta ilegible, servicio vivo, meta sin PID— y
// confundirlas produce una TUI que afirma "corriendo" sobre un servicio parado.
func TestRefreshCmdRecorreSoloLosConfiguradosYAtribuyeElMotivo(t *testing.T) {
	// isolateConfig es OBLIGATORIO aquí y no un detalle: New lee config.Load(), y
	// si la config del developer trae scanner.root el modelo escanea su workspace
	// real. Un test que pase por casualidad con la config ajena está probando los
	// proyectos del developer.
	isolateConfig(t)
	root := writeTestTree(t, false)
	store := state.NewStoreAt(t.TempDir())

	apiPath := filepath.Join(root, "tienda-api")
	rotoPath := filepath.Join(root, "tienda-web")

	for _, dir := range []string{apiPath, rotoPath} {
		if err := os.MkdirAll(store.ServiceDir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// tienda-web: meta.json con basura -> error de lectura, no ausencia.
	if err := os.WriteFile(filepath.Join(store.ServiceDir(rotoPath), "meta.json"), []byte("{no-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// api: vivo y con puerto.
	live := livePID(t)
	if err := store.SaveMeta(apiPath, state.Meta{
		Name: "tienda-api", Pid: live, Pgid: live, Port: 8081, State: state.StateRunning,
	}); err != nil {
		t.Fatal(err)
	}

	m := New(store, &stubManager{}, root)
	m.updateLayout()

	msg := runCmd(refreshCmd(store, &stubManager{}, m.projects))
	ref, ok := msg.(refreshedMsg)
	if !ok {
		t.Fatalf("refreshCmd devolvió %T", msg)
	}

	// El meta ilegible: stopped, PERO con un aviso que lo dice.
	//
	// Es un fallo cerrado a propósito —una TUI que dice "desconocido" para un
	// servicio con el store corrupto sería peor que una que dice "parado" y lo
	// avisa— y la mitad importante es el `warn`: sin él, "parado" sería una
	// afirmación que nadie sabe. El aviso tiene que nombrar el proyecto, porque la
	// columna muestra uno solo cada vez.
	got := ref.results[rotoPath]
	if got.status != process.StatusStopped {
		t.Errorf("un meta ilegible dio %q, want stopped (fallo cerrado)", got.status)
	}
	if got.warn == "" {
		t.Fatal("un meta ilegible sin warn afirma 'parado' como si fuera un hecho")
	}
	if !strings.Contains(got.warn, "tienda-web") || !strings.Contains(got.warn, "meta.json ilegible") {
		t.Errorf("el aviso no dice qué proyecto y por qué: %q", got.warn)
	}

	// El vivo: con el stubManager Evaluate devuelve stopped, así que lo que se
	// comprueba aquí es que el msg trae la rama git y que hay resultado para él.
	if got, ok := ref.results[apiPath]; !ok {
		t.Error("no hay resultado para un servicio configurado")
	} else if got.meta.Pid != live {
		t.Errorf("meta.Pid = %d, want el del Meta persistido", got.meta.Pid)
	}

	// Un servicio NUNCA arrancado sí se consulta, y sale stopped: un meta ausente
	// es un hecho —no hay proceso— y tratarlo como error llenaría la TUI de avisos
	// de nada en cada arranque en frío. Es el tercer proyecto del árbol, que está
	// configurado y no tiene Meta.
	sinMetaPath := filepath.Join(root, "suelto")
	if got := ref.results[sinMetaPath].status; got != process.StatusStopped {
		t.Errorf("un servicio sin meta dio %q, want stopped: un meta ausente es un hecho, no un fallo", got)
	}

	// Y un proyecto NO configurado no aparece: no hay Manifest que arrancar, así
	// que preguntarle por él produce ruido y nada más.
	noCfg := filepath.Join(root, "sin-manifiesto")
	if err := os.MkdirAll(noCfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noCfg, "go.mod"), []byte("module sin-manifiesto\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m2 := New(store, &stubManager{}, root)
	m2.projects = append(m2.projects, scanner.Project{Path: noCfg, Name: "sin-manifiesto", Configured: false})
	ref2 := runCmd(refreshCmd(store, &stubManager{}, m2.projects)).(refreshedMsg)
	if _, ok := ref2.results[noCfg]; ok {
		t.Error("refreshCmd preguntó por un proyecto sin manifiesto: no hay nada que preguntar")
	}
}

// TestRefreshCmdPropagaLaRamaGitDeCadaProyecto: el polling también refresca la
// rama cacheada, que es lo que hace que el header del árbol no se quede en la
// rama del último commit del que se miró.
//
// El msg trae la rama POR servicio, no por proyecto suelto, y sin ella el
// `git branch -m` de una app cambiaría su nombre de ruta sin que vroom se
// enterara —que es exactamente el caso que obliga a la reconciliación de
// portless.
func TestRefreshCmdPropagaLaRamaGitDeCadaProyecto(t *testing.T) {
	isolateConfig(t)
	root := writeTestTree(t, false)
	store := state.NewStoreAt(t.TempDir())
	m := New(store, &stubManager{}, root)

	ref := runCmd(refreshCmd(store, &stubManager{}, m.projects)).(refreshedMsg)

	// Cada proyecto CONFIGURADO trae rama. No se exige que no sea vacía: un
	// proyecto que no es un repo no tiene rama, y eso es un hecho que el polling
	// tiene que poder reportar sin inventar nada.
	for _, p := range m.projects {
		if !p.Configured {
			continue
		}
		r, ok := ref.results[p.Path]
		if !ok {
			t.Errorf("no hay resultado para el proyecto configurado %s", p.Name)
			continue
		}
		if p.Name == "tienda-api" && !strings.Contains(r.branch, "main") {
			t.Errorf("la rama de %s es %q, want main: el .git/HEAD del árbol apunta a refs/heads/main", p.Name, r.branch)
		}
	}

	// La rama del proyecto que NO es repo viene vacía, no con el nombre del
	// directorio. Un fallback al nombre inventaría una rama que el usuario
	//可能在 tiene, y esa rama es la que decide el nombre de la ruta en auto.
	for _, p := range m.projects {
		if p.Name == "tienda-api" {
			continue
		}
		if r, ok := ref.results[p.Path]; ok && r.branch != "" {
			t.Errorf("%s no es un repo y aun así trae rama %q", p.Name, r.branch)
		}
	}
}

// TestJobCmdDistingueElFalloDelComandoDelFalloDeLanzamiento: es la distinción
// que hace que el `exit_code` signifique algo.
//
// El comando que sale 1 NO es un error de vroom: el jobMsg lleva exit_code y err
// vacío, y así el footer del log dice "falló (exit 1)". Un fallo de lanzamiento
// —no hay `sh`, o no se puede escribir el log— sí es err, y entonces no hay
// exit_code que atribuir a nadie.
func TestJobCmdDistingueElFalloDelComandoDelFalloDeLanzamiento(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.log")
	errLog := filepath.Join(dir, "stderr.log")

	t.Run("el comando falla con su exit code", func(t *testing.T) {
		jm := runCmd(jobCmd(dir, "build", "echo fuera; exit 7", dir, out, errLog)).(jobMsg)
		if jm.err != nil {
			t.Errorf("un exit 7 no es un error de vroom: %v", jm.err)
		}
		if jm.exitCode != 7 {
			t.Errorf("exitCode = %d, want 7", jm.exitCode)
		}
		if jm.kind != "build" {
			t.Errorf("kind = %q", jm.kind)
		}
	})

	t.Run("el comando sale bien", func(t *testing.T) {
		jm := runCmd(jobCmd(dir, "install", "echo dentro", dir, out, errLog)).(jobMsg)
		if jm.err != nil || jm.exitCode != 0 {
			t.Errorf("un job correcto dio err=%v exit=%d", jm.err, jm.exitCode)
		}
	})

	t.Run("el comando no se puede lanzar", func(t *testing.T) {
		t.Setenv("PATH", "") // sin `sh`
		jm := runCmd(jobCmd(dir, "build", "echo hola", dir, out, errLog)).(jobMsg)
		if jm.err == nil {
			t.Error("sin intérprete el job tiene que traer error: si no, parecería un build correcto")
		}
		if jm.exitCode != 0 {
			t.Errorf("exitCode = %d sin proceso no puede haber exit code", jm.exitCode)
		}
	})

	t.Run("el log no se puede escribir", func(t *testing.T) {
		// stdout.log es un directorio: el banner no cabe.
		blocked := makeDir(t, filepath.Join(dir, "stdout-es-dir"))
		jm := runCmd(jobCmd(dir, "build", "echo hola", dir, blocked, errLog)).(jobMsg)
		if jm.err == nil {
			t.Error("sin log no se puede ejecutar el comando: el agente vería ok sin salida")
		}
	})
}

// TestAppendLineCreaElFicheroYAnadeAlFinal: la primitiva de la que dependen el
// banner del job, los avisos del stop y las líneas de estado del servicio.
//
// Lo que importa es que ANADE: si truncara, el log del servicio perdería su
// historia en cada arranque, y `--tail` dejaría de ser útil.
func TestAppendLineCreaElFicheroYAnadeAlFinal(t *testing.T) {
	// MEDIDO: appendLine NO crea los directorios intermedios, a diferencia de
	// runLogged, que sí. Es correcto: quien llama (el stop, el arranque) ya ha
	// creado el directorio de servicio, y crear un árbol de directorios como
	// efecto secundario de "escribir una línea" escondería un bug de rutas.
	dir := t.TempDir()
	path := filepath.Join(dir, "linea.log")

	if err := appendLine(path, "primera"); err != nil {
		t.Fatalf("appendLine en un path nuevo: %v", err)
	}
	if err := appendLine(path, "segunda"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != "primera\nsegunda\n" {
		t.Errorf("contenido = %q, want las dos líneas en orden", got)
	}
}

// TestAppendLineFallaDondeNoPuedeEscribir: y falla en vez de tragarse el error.
//
// Un `appendLine` que ignorara el fallo perdería el aviso de un stop sin que nada
// lo dijera, y el usuario vería un servicio parado sin ninguna explicación.
func TestAppendLineFallaDondeNoPuedeEscribir(t *testing.T) {
	dir := t.TempDir()
	blocked := makeDir(t, filepath.Join(dir, "bloqueado"))
	if err := appendLine(blocked, "x"); err == nil {
		t.Error("appendLine sobre un directorio debería fallar")
	}

	// Y un directorio padre inexistente también: sin crear nada.
	if err := appendLine(filepath.Join(dir, "no-existe", "x.log"), "y"); err == nil {
		t.Error("appendLine no debe crear directorios: su contrato es escribir una línea, no un árbol")
	}
}

// TestJobBannerIdentificaElJob: el separador del log nombra el kind y el comando.
//
// Es lo que permite atribuir una línea del log a un build y no al servicio: sin
// el banner, un `echo fuera` de un build y el mismo `echo` del servicio serían
// indistinguibles en el mismo fichero.
func TestJobBannerIdentificaElJob(t *testing.T) {
	got := jobBanner("build", "make build")
	want := "── vroom ▶ build: make build ──"
	if got != want {
		t.Errorf("jobBanner = %q, want %q", got, want)
	}
}

// TestReadNewStrippedDevuelveDesdeElOffsetYSinANSI: el tail de la consola lee
// desde un offset y quita los códigos de escape.
//
// El offset es lo que evita releer el log entero en cada tick, y el StripANSI es
// lo que evita que los códigos se dibujen como texto. Y un error NO avanza el
// offset: si avanzara, el siguiente tick se saltaría justo los bytes que no se
// pudieron leer.
func TestReadNewStripped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "consola.log")
	if err := os.WriteFile(path, []byte("\x1b[31mrojo\x1b[0m\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, off, err := readNewStripped(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("quedan códigos ANSI en la salida: %q", got)
	}
	if !strings.Contains(got, "rojo") {
		t.Errorf("se perdió el texto al quitar ANSI: %q", got)
	}
	if off != int64(len("\x1b[31mrojo\x1b[0m\n")) {
		t.Errorf("offset = %d, want el tamaño del fichero", off)
	}

	// Segundo tramo desde el offset: no relee.
	if err := os.WriteFile(path, []byte("\x1b[31mrojo\x1b[0m\nmas\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err = readNewStripped(path, off)
	if err != nil {
		t.Fatal(err)
	}
	if got != "mas\n" {
		t.Errorf("segunda lectura = %q, want sólo lo nuevo", got)
	}

	// Un log que aún NO existe no es un error: es un servicio que todavía no ha
	// escrito nada. Tratarlo como error llenaría el tick de consola de avisos de
	// nada en cada arranque.
	missing := filepath.Join(t.TempDir(), "no-existe.log")
	got, same, err := readNewStripped(missing, 1234)
	if err != nil {
		t.Errorf("un log ausente no es un error: %v", err)
	}
	if got != "" {
		t.Errorf("data = %q de un log ausente, want vacio", got)
	}
	if same != 1234 {
		t.Errorf("offset = %d con un log ausente, want el que se pasó: avanzar perdería bytes", same)
	}

	// Un log que SÍ da error (un directorio donde debería haber fichero) sí lo
	// propaga, y el offset queda intacto para que el siguiente tick reintente.
	asDir := makeDir(t, filepath.Join(t.TempDir(), "log-es-dir"))
	_, same, err = readNewStripped(asDir, 1234)
	if err == nil {
		t.Error("un log ilegible debe propagar el error: si no, la consola mostraría contenido vacío como si fuera verdad")
	}
	if same != 1234 {
		t.Errorf("offset = %d tras un error, want 1234: avanzar perdería justo los bytes que no se leyeron", same)
	}

	// Y un log truncado por debajo del offset se relee ENTERO: rotar un log no
	// puede hacer que la consola deje de mostrarlo.
	short := filepath.Join(t.TempDir(), "corto.log")
	if err := os.WriteFile(short, []byte("nuevo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err = readNewStripped(short, 9999)
	if err != nil {
		t.Fatal(err)
	}
	if got != "nuevo\n" {
		t.Errorf("tras una rotación la consola mostró %q, want el contenido entero", got)
	}
}

// TestConsoleTickYReadDevuelvenElOffsetQueSeHaLeido: el reloj de la consola es
// lo que hace que el contenido nuevo llegue, y su offset es lo que evita
// duplicarlo.
//
// Laproperty importante es que los offsets de stdout y stderr son INDEPENDIENTES:
// un comando que escribe en los dos dos no puede hacer que uno se pise al otro.
func TestConsoleReadDevuelveOffsetIndependientePorFlujo(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "o.log")
	errLog := filepath.Join(dir, "e.log")
	if err := os.WriteFile(out, []byte("salida\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(errLog, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	msg := runCmd(consoleTailCmd("/tmp/x", 0, 0, out, errLog))
	d, ok := msg.(consoleDeltaMsg)
	if !ok {
		t.Fatalf("consoleTailCmd devolvió %T", msg)
	}
	if d.path != "/tmp/x" {
		t.Errorf("path = %q, want /tmp/x", d.path)
	}
	if !strings.Contains(d.stdout, "salida") {
		t.Errorf("stdout = %q", d.stdout)
	}
	if d.offS != int64(len("salida\n")) {
		t.Errorf("offS = %d, want el tamaño de stdout", d.offS)
	}
	if d.offE != 0 {
		t.Errorf("offE = %d con un stderr vacío, want 0", d.offE)
	}
	if d.errS != nil {
		t.Errorf("errS = %v con un log legible", d.errS)
	}
}

// TestTickCmdEsMasLentoQueElTickDeConsola: los dos relojes tienen intervalos
// distintos A PROPÓSITO, y confundirlos rompe una de las dos cosas.
//
// El estado se sondea cada 2s porque leer el store y evaluar procesos es caro; la
// consola cada 400ms porque una línea de log se nota al instante y `tail.ReadNew`
// es barato. Si ambos fueran 2s, escribir en un log se vería con dos segundos de
// retraso; si ambos fueran 400ms, el polling se comería la CPU.
//
// Se mide sobre el intervalo declarado, no sobre el reloj: correr un tick real de
// 2s en un test no compra nada sobre la constante.
func TestTickCmdEsMasLentoQueElTickDeConsola(t *testing.T) {
	if consoleTick >= pollInterval {
		t.Errorf("consoleTick (%v) debería ser más rápido que pollInterval (%v): "+
			"el log se nota al instante y el store no", consoleTick, pollInterval)
	}
	if consoleTick <= 0 || pollInterval <= 0 {
		t.Error("un reloj en cero es un busy loop")
	}
}

// ---- helpers ----

// collectBatch ejecuta un Cmd que devuelve un batch y recoge TODOS los mensajes.
//
// tea.Batch devuelve un Cmd que al ejecutarse produce un tea.BatchMsg con la
// lista. Ejecutarlo sin descomponer perdería la mitad de lo que se quiere
// comprobar: Init promete cinco relojes y sólo se verían como un valor opaco.
func collectBatch(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := runCmd(cmd)
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		// Un Cmd suelto también es válido; se devuelve como lista de uno.
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		if c == nil {
			continue
		}
		out = append(out, runCmd(c))
	}
	return out
}

// isTickOfState distingue el tick de estado del de consola por su TIPO, que es lo
// que separa los dos relojes.
func isTickOfState(msg tea.Msg) bool {
	_, ok := msg.(tickMsg)
	return ok
}

func isConsoleTick(msg tea.Msg) bool {
	_, ok := msg.(consoleTickMsg)
	return ok
}

// makeDir crea un directorio y devuelve su ruta.
func makeDir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// livePID arranca un proceso vivo para los tests que necesitan un PID real.
//
// Se hace aquí y no con un stub porque lo que se mide es el muestreo de hilos del
// SISTEMA: un doble devolvería los hilos que el test le dicta, y no hay nada que
// verificar entonces.
func livePID(t *testing.T) int {
	t.Helper()
	dir := t.TempDir()
	mgr := process.NewManager()
	res, err := mgr.Start(process.StartSpec{
		Command: "sleep 60", WorkDir: dir,
		StdoutPath: filepath.Join(dir, "o.log"), StderrPath: filepath.Join(dir, "e.log"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = mgr.Stop(process.StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: process.DefaultStopTimeout})
	})
	return res.Pid
}
