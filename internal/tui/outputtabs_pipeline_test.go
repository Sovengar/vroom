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

// These cover the transition half -- the cmd that produces the message and the apply that integrates it -- against real /proc, real HTTP and real git, because a stub would assert the shape of the message instead of the path that produces it.

// A nil cmd is part of these tabs' contract, meaning "nothing to sample", so it must stay distinguishable from "produced a message".
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

// isRunning demands pid > 0 and a live status, which the metrics/env/health cmds check before reading anything.
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

// Returning nil keeps an empty message from putting a junk row in the cache.
func TestMetricsCmdNilSinSeleccionOParado(t *testing.T) {
	m, _ := newTestModel(t)

	m.cursor = -1
	if cmd := m.metricsCmd(); cmd != nil {
		t.Error("metricsCmd con nada seleccionado deberia devolver nil")
	}

	m = moveCursorTo(t, m, "tienda-api")
	if cmd := m.metricsCmd(); cmd != nil {
		t.Error("metricsCmd con el servicio parado deberia devolver nil")
	}

	path := pathOfSelected(t, m)
	m.services[path].Status = statusRunning
	m.services[path].Meta.Pid = 0
	if cmd := m.metricsCmd(); cmd != nil {
		t.Error("metricsCmd con pid 0 deberia devolver nil")
	}
}

// The numbers vary per machine, so what is asserted is the path and that applying the message leaves a usable view.
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
	// A zero here means the cmd sampled the wrong PID, which is the failure this test exists to catch.
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

	if got := strings.Join(m.metricsLines(80), "\n"); strings.Contains(got, "sampling metrics") {
		t.Errorf("metricsLines sigue en el placeholder tras integrar: %q", got)
	}
}

// A frozen CPU% is worse than none because it looks alive, so the cached sample is dropped on error.
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

// The exact value depends on the clock, so the assertion is on the sign (it grew) rather than on a number, which would be flaky.
func TestApplyMetricsCalculaCPUPorDelta(t *testing.T) {
	m, _ := newTestModel(t)
	const path = "/proj/x"

	m.applyMetrics(metricsMsg{path: path, m: process.Metrics{RSSKB: 100, Ticks: 500, Threads: 2, FDs: 3}})
	if got := m.metrics[path].CPU; got != 0 {
		t.Errorf("primera muestra CPU = %v, want 0 (no hay delta todavia)", got)
	}

	m.applyMetrics(metricsMsg{path: path, m: process.Metrics{RSSKB: 100, Ticks: 500, Threads: 2, FDs: 3}})
	if got := m.metrics[path].CPU; got != 0 {
		t.Errorf("ticks sin avanzar CPU = %v, want 0", got)
	}

	m.metricsPrev[path].at = time.Now().Add(-time.Second)
	m.applyMetrics(metricsMsg{path: path, m: process.Metrics{RSSKB: 100, Ticks: 900, Threads: 2, FDs: 3}})
	if got := m.metrics[path].CPU; got <= 0 {
		t.Errorf("ticks avanzando CPU = %v, want > 0", got)
	}
}

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

// The marker must be an inherited variable, not t.Setenv: ReadEnviron reads the block the kernel froze at execve, and a later setenv(3) never shows up in /proc/<pid>/environ.
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
	// applyEnv sorts because the render assumes it, so the list does not jump between refreshes of the same environment.
	for i := 1; i < len(vars); i++ {
		if vars[i-1] > vars[i] {
			t.Fatalf("el entorno no esta ordenado en la posicion %d: %q > %q", i, vars[i-1], vars[i])
		}
	}
}

// A stale environment after a dead PID is another process's variables, so it is dropped rather than kept.
func TestApplyEnvErrorBorraElEntornoPrevio(t *testing.T) {
	m, _ := newTestModel(t)
	const path = "/proj/x"
	m.envVars[path] = []string{"SECRETO=antiguo"}

	m.applyEnv(envMsg{path: path, err: errors.New("no such process")})

	if _, ok := m.envVars[path]; ok {
		t.Error("applyEnv dejo el entorno anterior tras un error: la UI mostraria variables de otro proceso")
	}
}

func TestGitCmdNilSinSeleccion(t *testing.T) {
	m, _ := newTestModel(t)
	m.cursor = -1
	if cmd := m.gitCmd(); cmd != nil {
		t.Error("gitCmd con nada seleccionado deberia devolver nil")
	}
}

// The test tree's hand-written .git/HEAD is enough for Branch but not for git status, so a real repo is initialised and committed here.
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

// The git error must reach the state because it is the only clue that the path is not a repo.
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

	m.applyGit(msg)
	if m.gitStatus[msg.path].Err == "" {
		t.Error("el error de git no llego al estado tras applyGit")
	}
}

// A read with no branch must not erase the known one, or a transient git failure leaves the UI branchless until the next success.
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

// The cmd has four guards (no selection, no config, no manifest, no port); only the first and third are reachable from this tree.
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

// Probing an unresolved port could hit a port owned by another worktree, so this guard is a safety decision, not an optimization.
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

// Redirecting the manifest port to the server's is what proves the cmd probes the resolved port instead of the declared one.
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
	// The resolved port overrides the manifest's, so both have to be aligned or the cmd would still probe the declared port.
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

// A 500 is a live service answering badly (its body is shown) while a refused connection means there is no service, and the two must stay distinguishable.
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
		// The server is closed first, so the port is free and the probe cannot connect.
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

// It is what the Health tab shows as the snippet, so a bad trim or an untreated leading newline is visible.
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
		// trunc() spends the last rune on the ellipsis, so 99 x plus it is exactly the 100-rune column contract.
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

// This is the only record that a whole stack started, so every service of the stack gets an event, not just the first.
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

// Duplicating would make a two-stage stack with a shared service look like that service started twice.
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

// A project without orchestration is the normal case here, and it must leave no events behind.
func TestRecordStackEventSinComposeFileNoHaceNada(t *testing.T) {
	m, _ := newTestModel(t)
	api := projectPath(t, m, "tienda-api")
	m.composeFile = nil

	m.recordStackEventByName("shop", true)

	if len(m.events[api]) != 0 {
		t.Errorf("sin compose file se escribieron %d eventos, want 0", len(m.events[api]))
	}
}

// An unknown name is not an error (the stack may have been filtered out) but cannot be attributed to any service.
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

// The health cmd reads the manifest through the scanned project, so both the tree item and the project copy have to be rewritten.
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

// Picked from a list of common candidates rather than the first that exists, so the test does not depend on the runner exporting something unexpected.
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

// Splits on the "//" and not on the first ":", which belongs to the scheme.
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
