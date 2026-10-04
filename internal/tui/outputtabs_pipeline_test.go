package tui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"vroom/internal/gitinfo"
	"vroom/internal/orchestrate"
	"vroom/internal/process"
	"vroom/internal/state"
)

// ---------------------------------------------------------------------------
// Los tests de outputtabs_test.go afirman el RENDER (metricsLines, gitLines)
// con la cache del modelo ya rellenada a mano. Eso prueba la mitad pura del
// sistema y deja la otra mitad sin proves: si applyMetrics se rompe, o metricsCmd
// lee el PID equivocado, esos tests siguen en verde.
//
// Estos tests cubren la mitad transicion: el cmd que produce el mensaje y el
// apply que lo integra. Y lo hacen contra las dependencias REALES, no contra
// stubs:
//
//   - metricsCmd/envCmd leen /proc, y se les da os.Getpid(): el muestreo es el
//     del propio proceso de test. Es una lectura real de /proc y determinista
//     en la existencia del PID, que es justo lo que el cmd necesita comprobar.
//   - probeHealth habla HTTP de verdad contra un httptest.Server real.
//   - gitCmd ejecuta git de verdad sobre un repo real inicializado en el árbol
//     temporal.
//
// La razón de no inyectar un seam para /proc ni para el HTTP es que no hace
// falta: los dos ya son deterministas sin stub, y un stub probaría la forma del
// mensaje en vez del camino que lo produce, que es donde estaban los huecos.
// ---------------------------------------------------------------------------

// runCmd ejecuta un tea.Cmd y devuelve su mensaje, o nil si el cmd es nil.
// Un cmd nil es parte del contrato de estos tabs: significa "no hay nada que
// muestrear", y hay que poder distinguirlo de "produjo un mensaje".
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// markRunning deja el servicio del path en estado vivo con el PID dado.
// isRunning exige pid > 0 y un status vivo, y los cmd de metrics/env/health
// pasan por ahi antes de leer nada.
func markRunning(m *Model, path string, pid int) {
	sv, ok := m.services[path]
	if !ok {
		sv = &ServiceState{}
		m.services[path] = sv
	}
	sv.Status = statusRunning
	sv.Meta.Pid = pid
	sv.Meta.State = state.StateRunning
}

// ---- Metrics ----

// TestMetricsCmdNilSinSeleccionOParado: los tres guards del cmd. Devolver nil
// es lo que evita un msg vacio que metería una fila basura en la cache.
func TestMetricsCmdNilSinSeleccionOParado(t *testing.T) {
	m, _ := newTestModel(t)

	// Cursor fuera del arbol: no hay proyecto seleccionado.
	m.cursor = -1
	if cmd := m.metricsCmd(); cmd != nil {
		t.Error("metricsCmd con nada seleccionado deberia devolver nil")
	}

	// Proyecto configurado pero parado: no hay PID que muestrear.
	m = moveCursorTo(t, m, "tienda-api")
	if cmd := m.metricsCmd(); cmd != nil {
		t.Error("metricsCmd con el servicio parado deberia devolver nil")
	}

	// Vivo pero sin PID: isRunning lo exige, asi que no hay nada que leer.
	path := pathOfSelected(t, m)
	m.services[path].Status = statusRunning
	m.services[path].Meta.Pid = 0
	if cmd := m.metricsCmd(); cmd != nil {
		t.Error("metricsCmd con pid 0 deberia devolver nil")
	}
}

// TestMetricsCmdMuestreaElProcesoRealYLoIntegra: el pipeline entero. El cmd se
// ejecuta contra /proc del propio proceso de test, y el mensaje se pasa por
// applyMetrics como haria Update. Lo que se afirma NO es el valor numerico
// (varia entre maquinas) sino que el mensaje trae el path correcto y que tras
// aplicarlo hay una vista de metricas utilizable.
func TestMetricsCmdMuestreaElProcesoRealYLoIntegra(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, os.Getpid())

	msg, ok := runCmd(m.metricsCmd()).(metricsMsg)
	if !ok {
		t.Fatal("metricsCmd no produjo un metricsMsg")
	}
	if msg.path != path {
		t.Errorf("metricsMsg.path = %q, want %q", msg.path, path)
	}
	if msg.err != nil {
		t.Fatalf("ReadMetrics(self) fallo: %v", msg.err)
	}
	// El proceso de test tiene threads y fds abiertos: si esto sale a cero, el
	// cmd esta leyendo el PID equivocado, que es el fallo que este test existe
	// para cazar.
	if msg.m.Threads == 0 {
		t.Error("Threads = 0 leyendo el propio proceso: se esta muestreando otro PID")
	}

	m.applyMetrics(msg)

	v := m.metrics[path]
	if v == nil {
		t.Fatal("applyMetrics no dejo metricas para el path del servicio")
	}
	if v.Threads != msg.m.Threads {
		t.Errorf("v.Threads = %d, want %d", v.Threads, msg.m.Threads)
	}
	if v.At.IsZero() {
		t.Error("v.At sinAssignar: la vista no lleva la hora del muestreo")
	}
	if m.metricsPrev[path] == nil {
		t.Error("applyMetrics no guardo la muestra previa, el CPU% nunca se podria calcular")
	}

	// El render tiene que ver ahora las metricas, no el placeholder.
	if got := strings.Join(m.metricsLines(80), "\n"); strings.Contains(got, "sampling metrics") {
		t.Errorf("metricsLines sigue en el placeholder tras integrar: %q", got)
	}
}

// TestApplyMetricsErrorBorraLaMuestraVieja: si el muestreo falla, la cache se
// borra en vez de quedarse con el ultimo valor bueno. Un CPU% congelado es
// peor que none: parece vivo.
func TestApplyMetricsErrorBorraLaMuestraVieja(t *testing.T) {
	m, _ := newTestModel(t)
	path := "/proj/x"
	m.metrics[path] = &metricsView{CPU: 42, RSSKB: 999}
	m.metricsPrev[path] = &metricsSample{at: time.Now(), ticks: 100}

	m.applyMetrics(metricsMsg{path: path, err: errors.New("boom")})

	if _, ok := m.metrics[path]; ok {
		t.Error("applyMetrics dejo la vista anterior tras un error: la UI mostraria un CPU congelado")
	}
}

// TestApplyMetricsCalculaCPUPorDelta: el CPU% es un delta entre dos muestras, no
// un valor absoluto. Sin muestra previa es 0; con ticks que avanzan es > 0.
//
// El segundo caso usa ticks imposibles a proposito: el valor exacto depende del
// reloj, pero el SIGNO no. Un assert sobre el numero seria flaky; un assert
// sobre "crecio" es estable y es lo que el render necesita saber.
func TestApplyMetricsCalculaCPUPorDelta(t *testing.T) {
	m, _ := newTestModel(t)
	const path = "/proj/x"

	// Primera muestra: no hay con quien comparar, CPU = 0.
	m.applyMetrics(metricsMsg{path: path, m: process.Metrics{RSSKB: 100, Ticks: 500, Threads: 2, FDs: 3}})
	if got := m.metrics[path].CPU; got != 0 {
		t.Errorf("primera muestra CPU = %v, want 0 (no hay delta todavia)", got)
	}

	// Los ticks no avanzan: CPU se queda en 0 en vez de dividir por algo.
	m.applyMetrics(metricsMsg{path: path, m: process.Metrics{RSSKB: 100, Ticks: 500, Threads: 2, FDs: 3}})
	if got := m.metrics[path].CPU; got != 0 {
		t.Errorf("ticks sin avanzar CPU = %v, want 0", got)
	}

	// Los ticks avanzan: hay delta, el CPU% sale de process.CPUPercent.
	m.metricsPrev[path].at = time.Now().Add(-time.Second)
	m.applyMetrics(metricsMsg{path: path, m: process.Metrics{RSSKB: 100, Ticks: 900, Threads: 2, FDs: 3}})
	if got := m.metrics[path].CPU; got <= 0 {
		t.Errorf("ticks avanzando CPU = %v, want > 0", got)
	}
}

// ---- Env ----

// TestEnvCmdNilSinSeleccionOParado: mismos guards que metricsCmd.
func TestEnvCmdNilSinSeleccionOParado(t *testing.T) {
	m, _ := newTestModel(t)

	m.cursor = -1
	if cmd := m.envCmd(); cmd != nil {
		t.Error("envCmd con nada seleccionado deberia devolver nil")
	}

	m = moveCursorTo(t, m, "tienda-api")
	if cmd := m.envCmd(); cmd != nil {
		t.Error("envCmd con el servicio parado deberia devolver nil")
	}

	path := pathOfSelected(t, m)
	m.services[path].Status = statusRunning
	m.services[path].Meta.Pid = 0
	if cmd := m.envCmd(); cmd != nil {
		t.Error("envCmd con pid 0 deberia devolver nil")
	}
}

// TestEnvCmdLeeElEntornoRealYLoOrdena: el entorno del proceso de test, con una
// variable heredada de verdad del runner que debe aparecer. Es la prueba de que
// el cmd lee el PID del servicio y no otro.
//
// NOTA sobre por que el marcador NO se inyecta con t.Setenv: ReadEnviron lee
// /proc/<pid>/environ, que es el bloque que el kernel congelo en el execve que
// lanzo el binario de test. t.Setenv llama a setenv(3) DESPUES, y eso cambia el
// entorno del proceso dentro de Go pero NO el bloque que /proc expone:
// comprobado, con la variable inyectada se leen 151 vars y el marcador no esta.
// Un test que marque con t.Setenv solo pasaria sobre una lectura-stub.
//
// NOTA sobre la variable heredada (la primera version de este test decia
// "inyectada", que era untrue):
// variable inyectada que debe aparecer. Es la prova de que el cmd lee el PID del
// servicio y no otro: si leyera otro proceso, la variable no estara.
func TestEnvCmdLeeElEntornoRealYLoOrdena(t *testing.T) {
	marker := pickRealEnvVar(t)

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, os.Getpid())

	msg, ok := runCmd(m.envCmd()).(envMsg)
	if !ok {
		t.Fatal("envCmd no produjo un envMsg")
	}
	if msg.path != path {
		t.Errorf("envMsg.path = %q, want %q", msg.path, path)
	}
	if msg.err != nil {
		t.Fatalf("ReadEnviron(self) fallo: %v", msg.err)
	}

	found := false
	for _, kv := range msg.vars {
		if strings.HasPrefix(kv, marker+"=") {
			found = true
		}
	}
	if !found {
		t.Errorf("la variable %q no esta en el entorno leido (%d vars): se leyo otro proceso", marker, len(msg.vars))
	}

	m.applyEnv(msg)

	vars := m.envVars[path]
	if len(vars) == 0 {
		t.Fatal("applyEnv no guardo el entorno")
	}
	// applyEnv ordena: el render lo asume para que la lista no baile entre
	// refrescos del mismo entorno.
	for i := 1; i < len(vars); i++ {
		if vars[i-1] > vars[i] {
			t.Fatalf("el entorno no esta ordenado en la posicion %d: %q > %q", i, vars[i-1], vars[i])
		}
	}
}

// TestApplyEnvErrorBorraElEntornoPrevio: mismo criterio que en metrics. Un
// entorno obsoleto tras un PID muerto son variables de otro proceso.
func TestApplyEnvErrorBorraElEntornoPrevio(t *testing.T) {
	m, _ := newTestModel(t)
	const path = "/proj/x"
	m.envVars[path] = []string{"SECRETO=antiguo"}

	m.applyEnv(envMsg{path: path, err: errors.New("no such process")})

	if _, ok := m.envVars[path]; ok {
		t.Error("applyEnv dejo el entorno anterior tras un error: la UI mostraria variables de otro proceso")
	}
}

// ---- Git ----

// TestGitCmdNilSinSeleccion.
func TestGitCmdNilSinSeleccion(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = -1
	if cmd := m.gitCmd(); cmd != nil {
		t.Error("gitCmd con nada seleccionado deberia devolver nil")
	}
}

// TestGitCmdLeeElRepoRealYLoIntegra: git de verdad sobre un repo real. El árbol
// de tests trae un .git/HEAD escrito a mano, que basta para Branch pero NO para
// `git status`; por eso se inicializa un repo de verdad, se configura y se
// commitea. Es lo que hace el cmd en produccion.
func TestGitCmdLeeElRepoRealYLoIntegra(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	initRepoWithCommit(t, path)

	msg, ok := runCmd(m.gitCmd()).(gitMsg)
	if !ok {
		t.Fatal("gitCmd no produjo un gitMsg")
	}
	if msg.path != path {
		t.Errorf("gitMsg.path = %q, want %q", msg.path, path)
	}
	if msg.st.Err != "" {
		t.Fatalf("git status fallo sobre un repo real: %q", msg.st.Err)
	}
	if msg.st.Branch == "" {
		t.Error("gitMsg sin rama tras leer un repo real")
	}

	m.applyGit(msg)

	if _, ok := m.gitStatus[path]; !ok {
		t.Error("applyGit no guardo el estado del proyecto")
	}
	if m.branches[path] != msg.st.Branch {
		t.Errorf("branches[%q] = %q, want %q: applyGit no sincronizo la rama", path, m.branches[path], msg.st.Branch)
	}
}

// TestGitCmdDetectaCambiosSinCommitear: la otra mitad de la informacion de la
// tab. Un fichero modificado tiene que salir como changed, no como clean.
func TestGitCmdDetectaCambiosSinCommitear(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	initRepoWithCommit(t, path)

	if err := os.WriteFile(filepath.Join(path, "go.mod"), []byte("module api\n// tocado\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	msg, ok := runCmd(m.gitCmd()).(gitMsg)
	if !ok {
		t.Fatal("gitCmd no produjo un gitMsg")
	}
	if msg.st.Err != "" {
		t.Fatalf("git status fallo: %q", msg.st.Err)
	}
	if !msg.st.Dirty() {
		t.Error("el repo tiene un fichero modificado y Dirty() dice que no")
	}

	m.applyGit(msg)
	if !m.gitStatus[path].Dirty() {
		t.Error("tras applyGit el estado deberia seguir sucio")
	}
}

// TestGitCmdRepoIlegiblePropagaElError: sin repo, git falla. El error tiene que
// llegar al estado, no desaparecer: la tab lo muestra y es la unica pista de
// que la ruta no es un repo.
func TestGitCmdRepoIlegiblePropagaElError(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "suelto")

	msg, ok := runCmd(m.gitCmd()).(gitMsg)
	if !ok {
		t.Fatal("gitCmd no producido un gitMsg")
	}
	if msg.path != pathOfSelected(t, m) {
		t.Errorf("gitMsg.path = %q, want el proyecto bajo el cursor", msg.path)
	}
	if msg.st.Err == "" {
		t.Error("git status sobre un directorio sin repo deberia traer error")
	}

	// El error tiene que llegar a la tab: es la unica pista de que la ruta no
	// es un repo. Si applyGit se lo comiera, la UI mostraria "clean" en un
	// directorio sin control de versiones.
	m.applyGit(msg)
	if m.gitStatus[msg.path].Err == "" {
		t.Error("el error de git no llego al estado tras applyGit")
	}
}

// TestApplyGitSinRamaNoPisaLaRamaConocida: una lectura sin rama (repo ilegible,
// o rama borrada) no debe borrar la rama que ya se sabia. Si lo hiciera, un
// fallo transitorio de git dejaria la UI sin nombre de rama hasta el siguiente
// exito.
func TestApplyGitSinRamaNoPisaLaRamaConocida(t *testing.T) {
	m, _ := newTestModel(t)
	const path = "/proj/x"
	m.branches[path] = "feature/vieja"

	m.applyGit(gitMsg{path: path, st: gitinfo.Status{Branch: "", Err: "no repo"}})

	if m.branches[path] != "feature/vieja" {
		t.Errorf("branches[%q] = %q, se perdio la rama conocida al leer una sin rama", path, m.branches[path])
	}
	if _, ok := m.gitStatus[path]; !ok {
		t.Error("applyGit no guardo el estado aunque la rama viniera vacia")
	}
}

// ---- Health ----

// TestHealthCmdNilSinSeleccion: el cmd tiene cuatro guards (sin seleccion, sin
// config, sin manifiesto, sin puerto). El de sin puerto es el que evita sondear
// un puerto que no es del servicio.
func TestHealthCmdNilSinSeleccion(t *testing.T) {
	m, _ := newTestModel(t)

	m.cursor = -1
	if cmd := m.healthCmd(); cmd != nil {
		t.Error("healthCmd con nada seleccionado deberia devolver nil")
	}

	m = moveCursorTo(t, m, "suelto")
	if cmd := m.healthCmd(); cmd != nil {
		t.Error("healthCmd sin manifiesto deberia devolver nil")
	}
}

// TestHealthCmdNoSondeaConPuertoSinResolver: hay un puerto en el manifiesto pero
// el bind no se confirmo. Sondear apuntaria a un puerto que puede ser el de otro
// worktree, asi que no se hace. Este guard es una decision de seguridad, no una
// optimizacion.
func TestHealthCmdNoSondeaConPuertoSinResolver(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	sv := m.services[path]
	sv.Status = statusRunning
	sv.Meta.Pid = 1
	sv.Meta.State = state.StatePortUnresolved
	sv.Meta.Port = 0

	if cmd := m.healthCmd(); cmd != nil {
		t.Error("healthCmd deberia devolver nil con el puerto sin resolver: sondearia el puerto de otro")
	}
}

// TestHealthCmdSondeaElPuertoRealDelServicio: el cmd completo contra un
// httptest.Server real. El puerto del manifiesto se redirige al del server, que
// es como se verifica que el cmd sondea el puerto de displayPort y no el
// declarado a ciegas.
func TestHealthCmdSondeaElPuertoRealDelServicio(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	port := serverPort(t, srv.URL)

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	setManifestPort(&m, "tienda-api", port)
	setHealthPath(&m, "tienda-api", "/healthz")
	markRunning(&m, path, os.Getpid())
	// El puerto resuelto manda sobre el del manifiesto; hay que alinearlos o el
	// cmd seguiria apuntando al puerto declarado.
	m.services[path].Meta.Port = port

	msg, ok := runCmd(m.healthCmd()).(healthMsg)
	if !ok {
		t.Fatal("healthCmd no produjo un healthMsg")
	}
	if msg.path != path {
		t.Errorf("healthMsg.path = %q, want %q", msg.path, path)
	}
	if msg.r == nil {
		t.Fatal("healthMsg sin resultado")
	}
	if msg.r.Err != "" {
		t.Fatalf("el probe contra el server real fallo: %q", msg.r.Err)
	}
	if msg.r.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want 200", msg.r.StatusCode)
	}
	if msg.r.ContentType != "application/json" {
		t.Errorf("ContentType = %q, want application/json", msg.r.ContentType)
	}
	if msg.r.Snippet != `{"ok":true}` {
		t.Errorf("Snippet = %q, want el cuerpo del server", msg.r.Snippet)
	}

	next, _ := m.Update(msg)
	m = next.(Model)
	if m.healthRes[path] == nil {
		t.Error("Update no guardo el resultado del probe")
	}
	if got := strings.Join(m.healthLines(80), "\n"); strings.Contains(got, "probing…") {
		t.Errorf("healthLines sigue en probing tras el resultado: %q", got)
	}
}

// TestProbeHealthErroresYEstados: probeHealth contra un server real que
// devuelve 500, y contra un puerto donde no hay nadie. Los dos casos tienen que
// distinguirse: 500 es un servicio vivo que responde mal (se muestra la
// respuesta), y conexion rehusada es que no hay servicio.
func TestProbeHealthErroresYEstados(t *testing.T) {
	t.Run("500 con cuerpo", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		}))
		defer srv.Close()

		r := probeHealth(srv.URL)
		if r.Err != "" {
			t.Fatalf("un 500 no es un error de transporte: %q", r.Err)
		}
		if r.StatusCode != 500 {
			t.Errorf("StatusCode = %d, want 500", r.StatusCode)
		}
		if r.Snippet != "boom" {
			t.Errorf("Snippet = %q, want %q", r.Snippet, "boom")
		}
	})

	t.Run("nadie escuchando", func(t *testing.T) {
		// Server cerrado: el puerto queda libre y el probe no puede conectar.
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close()

		r := probeHealth(url)
		if r.Err == "" {
			t.Error("un probe a un puerto sin servicio deberia traer Err")
		}
		if r.StatusCode != 0 {
			t.Errorf("StatusCode = %d tras fallo de transporte, want 0", r.StatusCode)
		}
	})
}

// TestFirstLine: la primera linea NO vacia, recortada a 100. Es lo que se ve
// como snippet en la tab Health, asi que un recorte mal hecho o un salto
// inicial no tratado se notan.
func TestFirstLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"primera linea normal", "hola\nadios", "hola"},
		{"saltos iniciales", "\n\n  \nhola", "hola"},
		{"espacios alrededor", "   hola   \n", "hola"},
		{"solo espacios", "   \n\t\n", ""},
		{"vacio", "", ""},
		{"sin salto", "una linea", "una linea"},
		// trunc() gasta el ultimo caracter en la elipsis: 99 x + "…". El
		// total son 100 runes, que es el contrato de anchura de la columna.
		{"recorta a 100 con elipsis", strings.Repeat("x", 250), strings.Repeat("x", 99) + "…"},
		{"crlf", "hola\r\nadios", "hola"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstLine(tt.in); got != tt.want {
				t.Errorf("firstLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---- Timeline de stacks ----

// TestRecordStackEventPorNombreConComposeFile: un arranque o parada de stack
// deja un evento en el timeline de CADA servicio del stack, no solo del
// primero. Es la unica trazabilidad de que un stack arranco entero.
func TestRecordStackEventPorNombreConComposeFile(t *testing.T) {
	m, _ := newTestModel(t)
	api := projectPath(t, m, "tienda-api")
	web := projectPath(t, m, "tienda-web")

	m.composeFile = &orchestrate.ComposeFile{Stacks: []orchestrate.Stack{{
		Name: "shop",
		Stages: []orchestrate.Stage{
			{Name: "infra", Services: []string{"tienda-api"}},
			{Name: "apps", Services: []string{"tienda-web"}},
		},
	}}}

	m.recordStackEventByName("shop", true)

	if len(m.events[api]) != 1 {
		t.Errorf("el timeline de %s tiene %d eventos, want 1", api, len(m.events[api]))
	}
	if len(m.events[web]) != 1 {
		t.Errorf("el timeline de %s tiene %d eventos, want 1", web, len(m.events[web]))
	}
	if got := m.events[api][0]; got.Kind != "stack" || got.Detail != "shop" || !got.OK {
		t.Errorf("evento = %+v, want kind=stack detail=shop OK", got)
	}
}

// TestRecordStackEventDeduplicaServiciosRepetidos: un servicio puede aparecer en
// varias etapas del stack. Solo debe quedar un evento: duplicarlo haria que un
// stack de dos etapas con un servicio compartido pareciese haber arrancado ese
// servicio dos veces.
func TestRecordStackEventDeduplicaServiciosRepetidos(t *testing.T) {
	m, _ := newTestModel(t)
	api := projectPath(t, m, "tienda-api")

	m.composeFile = &orchestrate.ComposeFile{Stacks: []orchestrate.Stack{{
		Name: "shop",
		Stages: []orchestrate.Stage{
			{Name: "a", Services: []string{"tienda-api"}},
			{Name: "b", Services: []string{"tienda-api"}},
		},
	}}}

	m.recordStackEventByName("shop", false)

	if got := len(m.events[api]); got != 1 {
		t.Errorf("un servicio en dos etapas produjo %d eventos, want 1 (dedup)", got)
	}
	if m.events[api][0].OK {
		t.Error("un stack fallido debe registrar el evento con OK=false")
	}
}

// TestRecordStackEventSinComposeFileNoHaceNada: sin compose file no hay stacks
// que registrar. Es el estado normal de un proyecto sin orquestacion, y no puede
// dejar basura en ningun timeline.
func TestRecordStackEventSinComposeFileNoHaceNada(t *testing.T) {
	m, _ := newTestModel(t)
	api := projectPath(t, m, "tienda-api")
	m.composeFile = nil

	m.recordStackEventByName("shop", true)

	if len(m.events[api]) != 0 {
		t.Errorf("sin compose file se escribieron %d eventos, want 0", len(m.events[api]))
	}
}

// TestRecordStackEventNombreDesconocidoNoEscribe: un nombre que no esta en el
// compose file no es un error (el stack puede haberse filtrado), pero tampoco
// puede atribuirse a ningun servicio.
func TestRecordStackEventNombreDesconocidoNoEscribe(t *testing.T) {
	m, _ := newTestModel(t)
	api := projectPath(t, m, "tienda-api")

	m.composeFile = &orchestrate.ComposeFile{Stacks: []orchestrate.Stack{{
		Name:   "shop",
		Stages: []orchestrate.Stage{{Name: "a", Services: []string{"tienda-api"}}},
	}}}

	m.recordStackEventByName("otro-stack", true)

	if len(m.events[api]) != 0 {
		t.Errorf("un stack desconocido escribio %d eventos, want 0", len(m.events[api]))
	}
}

// ---- Helpers ----

// projectPath devuelve la ruta absoluta del proyecto con ese nombre de manifiesto.
func projectPath(t *testing.T, m Model, name string) string {
	t.Helper()
	for _, p := range m.projects {
		if p.Manifest != nil && p.Manifest.Name == name {
			return p.Path
		}
	}
	t.Fatalf("proyecto %s no encontrado", name)
	return ""
}

// setManifestPort reescribe el puerto del manifiesto del proyecto indicado.
// Se hace sobre el árbol, no sobre el manifest ya parseado, porque el cmd de
// health lee el manifiesto que hay en disco a traves del proyecto escaneado.
func setManifestPort(m *Model, name string, port int) {
	for i := range m.tree {
		if m.tree[i].kind == itemProject && m.tree[i].project.Manifest != nil &&
			m.tree[i].project.Manifest.Name == name {
			m.tree[i].project.Manifest.Port = port
			for j := range m.projects {
				if m.projects[j].Manifest != nil && m.projects[j].Manifest.Name == name {
					m.projects[j].Manifest.Port = port
				}
			}
			return
		}
	}
}

// setHealthPath reesplica setManifestPort para health_path.
func setHealthPath(m *Model, name, path string) {
	for i := range m.tree {
		if m.tree[i].kind == itemProject && m.tree[i].project.Manifest != nil &&
			m.tree[i].project.Manifest.Name == name {
			m.tree[i].project.Manifest.HealthPath = path
			for j := range m.projects {
				if m.projects[j].Manifest != nil && m.projects[j].Manifest.Name == name {
					m.projects[j].Manifest.HealthPath = path
				}
			}
			return
		}
	}
}

// pickRealEnvVar devuelve el nombre (sin valor) de una variable de entorno que
// el proceso de test heredo de verdad del runner, para marcarla al leer
// /proc/self/environ. Se elige de una lista de candidatos habituales en vez de
// "la primera que exista", para no depender de que el runner exporte algo
// inesperado.
func pickRealEnvVar(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"HOME", "PATH", "USER", "LANG", "TERM", "PWD", "SHELL"} {
		if _, ok := os.LookupEnv(name); ok {
			return name
		}
	}
	t.Skip("el proceso de test no heredo ninguna variable de entorno utilizable como marcador")
	return ""
}

// serverPort extrae el puerto de una URL de httptest ("http://127.0.0.1:PORT").
// Se parte por "//" y no por el primer ":", que es el del esquema.
func serverPort(t *testing.T, rawURL string) int {
	t.Helper()
	_, hostport, ok := strings.Cut(rawURL, "//")
	if !ok {
		t.Fatalf("no se pudo localizar el host en %q", rawURL)
	}
	_, portStr, ok := strings.Cut(hostport, ":")
	if !ok {
		t.Fatalf("no se pudo extraer el puerto de %q", rawURL)
	}
	portStr, _, _ = strings.Cut(portStr, "/")
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("puerto %q no numerico: %v", portStr, err)
	}
	return port
}

// initRepoWithCommit crea un repo git real en path, con un commit, para que
// `git status` y `git log` tengan algo que responder.
func initRepoWithCommit(t *testing.T, path string) {
	t.Helper()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "test@vroom.local")
	git("config", "user.name", "vroom test")
	git("add", "-A")
	git("commit", "-q", "-m", "test base")
}
