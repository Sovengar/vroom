package tui

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"vroom/internal/gitinfo"
	"vroom/internal/state"
	"vroom/internal/tail"
)

func comoPanel(t *testing.T, m Model, f func(Model, int) []string) string {
	t.Helper()
	return tail.StripANSI(strings.Join(f(m, m.rightW), "\n"))
}

func TestPanelesDeMuestreoDicenPorQueNoTraenDatos(t *testing.T) {
	paneles := []struct {
		nombre string
		render func(Model, int) []string
		quiere string
	}{
		{"metrics", Model.metricsLines, "select a service"},
		{"git", Model.gitLines, "select a project"},
		{"env", Model.envLines, "select a service"},
		{"timeline", Model.timelineLines, "select a service"},
		{"health", Model.healthLines, "select a service"},
	}
	for _, p := range paneles {
		t.Run(p.nombre+"/sin selección", func(t *testing.T) {
			m := sinSeleccion(t)
			got := comoPanel(t, m, p.render)
			if !strings.Contains(got, p.quiere) {
				t.Errorf("%s sin selección = %q, want una invitación a elegir", p.nombre, got)
			}
		})
	}

	for _, p := range []struct {
		nombre string
		render func(Model, int) []string
		quiere string
	}{
		{"metrics", Model.metricsLines, "no manifest"},
		{"env", Model.envLines, "no manifest"},
		{"health", Model.healthLines, "no manifest"},
	} {
		t.Run(p.nombre+"/sin manifiesto", func(t *testing.T) {
			m := sinManifiesto(t)
			got := comoPanel(t, m, p.render)
			if !strings.Contains(got, p.quiere) {
				t.Errorf("%s sin manifiesto = %q", p.nombre, got)
			}
			if strings.Contains(got, "select a service") {
				t.Errorf("%s = %q: el proyecto está seleccionado, el motivo es otro", p.nombre, got)
			}
		})
	}
}

func TestPanelesDeMuestreoDicenQueElServicioNoCorre(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusStopped

	for _, p := range []struct {
		nombre string
		render func(Model, int) []string
		quiere string
	}{
		{"metrics", Model.metricsLines, "not running"},
		{"env", Model.envLines, "start it"},
		{"health", Model.healthLines, "not running"},
	} {
		t.Run(p.nombre, func(t *testing.T) {
			if got := comoPanel(t, m, p.render); !strings.Contains(got, p.quiere) {
				t.Errorf("%s con el servicio parado = %q, want %q", p.nombre, got, p.quiere)
			}
		})
	}
}

func TestPanelesDeMuestreoDicenQueEstanLeyendo(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	setManifestPort(&m, "tienda-api", 4321)

	for _, p := range []struct {
		nombre string
		render func(Model, int) []string
		quiere string
	}{
		{"metrics", Model.metricsLines, "sampling"},
		{"env", Model.envLines, "reading"},
		{"health", Model.healthLines, "probing"},
		{"timeline", Model.timelineLines, "no events"},
	} {
		t.Run(p.nombre, func(t *testing.T) {
			if got := comoPanel(t, m, p.render); !strings.Contains(got, p.quiere) {
				t.Errorf("%s = %q, want %q", p.nombre, got, p.quiere)
			}
		})
	}

	// git reads per project, not per live process, so it stays in the reading state whatever the run status.
	t.Run("git", func(t *testing.T) {
		if got := comoPanel(t, m, Model.gitLines); !strings.Contains(got, "reading") {
			t.Errorf("git = %q, want que diga que está leyendo", got)
		}
	})
}

func TestGitLinesDistingueRepoDeNoRepoYLimpioDeSucio(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	t.Run("repo limpio", func(t *testing.T) {
		m.gitStatus[path] = gitinfo.Status{Branch: "main", Commits: []string{"abc1234 primero"}}
		got := comoPanel(t, m, Model.gitLines)
		for _, want := range []string{"branch:", "main", "status:", "clean", "commits:", "abc1234"} {
			if !strings.Contains(got, want) {
				t.Errorf("un repo limpio no trae %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "changes:") {
			t.Errorf("un repo limpio no puede traer sección de cambios:\n%s", got)
		}
	})

	t.Run("repo sucio", func(t *testing.T) {
		m.gitStatus[path] = gitinfo.Status{
			Branch:  "feature/login",
			Changed: []string{" M app.go", "?? nuevo.go"},
		}
		got := comoPanel(t, m, Model.gitLines)
		if !strings.Contains(got, "2 changed") {
			t.Errorf("el recuento de cambios tiene que ser el número, no un \"dirty\":\n%s", got)
		}
		if !strings.Contains(got, "changes:") || !strings.Contains(got, "nuevo.go") {
			t.Errorf("el panel tiene que listar los ficheros:\n%s", got)
		}
	})

	t.Run("sin repo", func(t *testing.T) {
		m.gitStatus[path] = gitinfo.Status{}
		if got := comoPanel(t, m, Model.gitLines); !strings.Contains(got, "no git repo") {
			t.Errorf("un proyecto fuera de git = %q, want la etiqueta de proyecto fuera de git, porque no es un error", got)
		}
	})

	t.Run("git falla", func(t *testing.T) {
		m.gitStatus[path] = gitinfo.Status{Err: "no such repository"}
		got := comoPanel(t, m, Model.gitLines)
		if !strings.Contains(got, "no such repository") {
			t.Errorf("un fallo de git tiene que aparecer: %q", got)
		}
		if strings.Contains(got, "clean") {
			t.Errorf("un fallo de git no puede salir como clean, porque sería mentir sobre el estado:\n%s", got)
		}
	})
}

func TestHealthLinesMuestraElCuerpoCuandoElProbeTraeUno(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	setManifestPort(&m, "tienda-api", 4321)
	m.healthRes[path] = &healthResult{
		StatusCode:  500,
		Latency:     350 * time.Millisecond,
		ContentType: "application/json",
		Snippet:     `{"error":"boom"}`,
	}

	got := comoPanel(t, m, Model.healthLines)
	for _, want := range []string{"status:", "HTTP 500", "latency:", "type:", "application/json", `{"error":"boom"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("el panel de salud no trae %q:\n%s", want, got)
		}
	}

	m.healthRes[path].Snippet = ""
	if got := comoPanel(t, m, Model.healthLines); strings.Contains(got, "snippet") {
		t.Errorf("sin snippet no hay nada que enseñar:\n%s", got)
	}
}

func TestHealthLinesDistingueUn200DeUn404PorElCodigo(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	setManifestPort(&m, "tienda-api", 4321)

	for _, code := range []int{200, 204, 301, 302, 400, 404, 500, 503} {
		m.healthRes[path] = &healthResult{StatusCode: code, ContentType: "text/plain"}
		got := comoPanel(t, m, Model.healthLines)
		if !strings.Contains(got, "HTTP "+strconv.Itoa(code)) {
			t.Errorf("código %d no aparece: %q", code, got)
		}
	}
}

func TestHealthLinesNoSondeaConElManifestoSinPuertoYLoDice(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Meta = state.Meta{State: state.StateStopped, Port: 0}
	if p := m.projectByPath(path); p != nil && p.Manifest != nil {
		p.Manifest.Port = 0
	}

	got := comoPanel(t, m, Model.healthLines)
	if !strings.Contains(got, "port = N") {
		t.Errorf("= %q, want que diga QUÉ escribir en el manifiesto", got)
	}
}

func TestEnvLinesCuentaLasVariablesYLasLista(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	m.envVars[path] = []string{"A=1", "B=2", "C=3"}

	got := comoPanel(t, m, Model.envLines)
	if !strings.Contains(got, "3 variables") {
		t.Errorf("= %q, want el recuento", got)
	}
	for _, want := range []string{"A=1", "B=2", "C=3"} {
		if !strings.Contains(got, want) {
			t.Errorf("no lista %q:\n%s", want, got)
		}
	}
}

// Inverted from log order on purpose: the cursor already sits at the end, so "what just happened" must come first.
func TestTimelineLinesLosOrdenaDelMasRecienteAlMasAntiguo(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	base := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	m.events[path] = []timelineEvent{
		{At: base, Kind: "start", Detail: "arrancado", Elapsed: 0, OK: true},
		{At: base.Add(time.Minute), Kind: "build", Detail: "falló", Elapsed: 2 * time.Second, OK: false},
		{At: base.Add(2 * time.Minute), Kind: "stop", Detail: "", Elapsed: time.Millisecond, OK: true},
	}

	got := comoPanel(t, m, Model.timelineLines)
	iStop := strings.Index(got, "stop")
	iBuild := strings.Index(got, "build")
	iStart := strings.Index(got, "start")

	if iStop < 0 || iBuild < 0 || iStart < 0 {
		t.Fatalf("faltan eventos:\n%s", got)
	}
	if iStop >= iBuild || iBuild >= iStart {
		t.Errorf("el orden es cronológico, want el más reciente primero:\n%s", got)
	}
	if !strings.Contains(got, "✓") || !strings.Contains(got, "✗") {
		t.Errorf("cada evento tiene que traer su marca de resultado:\n%s", got)
	}
	if !strings.Contains(got, "(2s)") {
		t.Errorf("un evento con duración la enseña:\n%s", got)
	}
	if strings.Contains(got, "(0s)") {
		t.Errorf("un evento instantáneo no enseña duración:\n%s", got)
	}
}

func TestMetricsLinesMuestreaYAnotaElMomento(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))

	ahora := time.Date(2026, 10, 3, 15, 4, 5, 0, time.Local)
	m.metrics[path] = &metricsView{CPU: 12.5, RSSKB: 1536, FDs: 42, Threads: 8, At: ahora}

	got := comoPanel(t, m, Model.metricsLines)
	for _, want := range []string{"CPU%", "RSS", "FDs", "Threads", "12.5%", "1.5 MB", "42", "8", "sampled at"} {
		if !strings.Contains(got, want) {
			t.Errorf("la tabla no trae %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, ahora.Format("15:04:05")) {
		t.Errorf("falta la hora del muestreo: %q", got)
	}
}

// A real httptest server, not a stub: an invented Content-Type and a snippet not taken from the response are the probe's two failure modes.
func TestProbeHealthTraeElCuerpoYElContentTypeDeVerdad(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	setManifestPort(&m, "tienda-api", serverPort(t, srv.URL))
	setHealthPath(&m, "tienda-api", "/api/health")

	p := m.projectByPath(path)
	res := probeHealth(healthURL(p, m.services[path]))
	if res.Err != "" {
		t.Fatalf("la sonda contra un servidor real falló: %v", res.Err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(res.ContentType, "json") {
		t.Errorf("content-type = %q, want el que devuelve el servidor", res.ContentType)
	}
	if !strings.Contains(res.Snippet, `"ok":true`) {
		t.Errorf("snippet = %q, want el cuerpo real", res.Snippet)
	}

	m.healthRes[path] = res
	got := comoPanel(t, m, Model.healthLines)
	for _, want := range []string{"HTTP 200", "json", `/api/health`, `"ok":true`} {
		if !strings.Contains(got, want) {
			t.Errorf("el panel no trae %q:\n%s", want, got)
		}
	}
}
