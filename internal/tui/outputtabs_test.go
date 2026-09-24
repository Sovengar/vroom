package tui

import (
	"strings"
	"testing"
	"time"

	"vroom/internal/gitinfo"
)

// La tab Metrics muestra la muestra cacheada del servicio en ejecución.
func TestMetricsLinesShowsSample(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	p := pathOfSelected(t, m)
	m.services[p].Status = statusRunning
	m.services[p].Meta.Pid = 4242
	m.metrics[p] = &metricsView{CPU: 12.5, RSSKB: 2048, Threads: 7, FDs: 19, At: time.Now()}

	joined := strings.Join(m.metricsLines(m.rightW), "\n")
	for _, want := range []string{"12.5%", "2.0 MB", "19", "Threads"} {
		if !strings.Contains(joined, want) {
			t.Errorf("metricsLines sin %q: %q", want, joined)
		}
	}
}

// Sin servicio corriendo, Metrics muestra el placeholder.
func TestMetricsLinesNotRunning(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	if got := strings.Join(m.metricsLines(m.rightW), "\n"); !strings.Contains(got, "not running") {
		t.Errorf("metricsLines = %q, want placeholder de parado", got)
	}
}

// La tab Git muestra rama y commits cacheados; el error se propaga.
func TestGitLines(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	p := pathOfSelected(t, m)
	m.gitStatus[p] = gitinfo.Status{Branch: "main", Commits: []string{"abc123 first commit"}}

	joined := strings.Join(m.gitLines(m.rightW), "\n")
	for _, want := range []string{"branch:", "main", "abc123 first commit"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gitLines sin %q: %q", want, joined)
		}
	}

	m.gitStatus[p] = gitinfo.Status{Err: "not a git repository"}
	if got := strings.Join(m.gitLines(m.rightW), "\n"); !strings.Contains(got, "not a git repository") {
		t.Errorf("gitLines debe mostrar el error: %q", got)
	}
}

// La tab Env muestra el entorno cacheado y ordenado.
func TestEnvLinesShowsVars(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	p := pathOfSelected(t, m)
	m.services[p].Status = statusRunning
	m.services[p].Meta.Pid = 1
	m.envVars[p] = []string{"HOME=/root", "PATH=/bin"}

	joined := strings.Join(m.envLines(m.rightW), "\n")
	if !strings.Contains(joined, "HOME=/root") || !strings.Contains(joined, "PATH=/bin") {
		t.Errorf("envLines = %q", joined)
	}
	if !strings.Contains(joined, "2 variables") {
		t.Errorf("envLines debe indicar el conteo: %q", joined)
	}
}

// El timeline registra eventos de jobs con su resultado.
func TestTimelineRecordsJobs(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	p := pathOfSelected(t, m)

	next, _ := m.Update(jobMsg{path: p, kind: "build", command: "go build", elapsed: 1200 * time.Millisecond, exitCode: 0})
	m2 := next.(Model)
	joined := strings.Join(m2.timelineLines(m2.rightW), "\n")
	if !strings.Contains(joined, "build") || !strings.Contains(joined, "✓") {
		t.Errorf("timeline sin evento build ok: %q", joined)
	}

	next, _ = m2.Update(jobMsg{path: p, kind: "install", elapsed: 500 * time.Millisecond, exitCode: 2})
	m3 := next.(Model)
	joined = strings.Join(m3.timelineLines(m3.rightW), "\n")
	if !strings.Contains(joined, "install") || !strings.Contains(joined, "✗") {
		t.Errorf("timeline sin evento install fallido: %q", joined)
	}
}

// Sin eventos, el timeline muestra el placeholder.
func TestTimelineEmpty(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	if got := strings.Join(m.timelineLines(m.rightW), "\n"); !strings.Contains(got, "no events") {
		t.Errorf("timelineLines = %q, want placeholder", got)
	}
}

// La tab Health muestra el resultado del probe; sin puerto avisa.
func TestHealthLines(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	p := pathOfSelected(t, m)
	m.services[p].Status = statusRunning
	m.services[p].Meta.Pid = 1

	m.healthRes[p] = &healthResult{StatusCode: 200, Latency: 12 * time.Millisecond, ContentType: "text/html", Snippet: "ok", At: time.Now()}
	joined := strings.Join(m.healthLines(m.rightW), "\n")
	for _, want := range []string{"HTTP 200", "12ms", "text/html", "ok"} {
		if !strings.Contains(joined, want) {
			t.Errorf("healthLines sin %q: %q", want, joined)
		}
	}

	m.healthRes[p] = &healthResult{Err: "connection refused", At: time.Now()}
	if got := strings.Join(m.healthLines(m.rightW), "\n"); !strings.Contains(got, "connection refused") {
		t.Errorf("healthLines debe mostrar el error: %q", got)
	}
}

// La tecla numérica activa cada una de las 7 pestañas del panel Output.
func TestAllTabsReachableByNumber(t *testing.T) {
	m, _ := newTestModel(t)
	for i := 0; i < int(tabCount); i++ {
		key := string(rune('1' + i))
		next, _ := press(m, key)
		if next.activeTab != tabKind(i) {
			t.Errorf("tecla %s activa %v, want %v", key, next.activeTab, tabKind(i))
		}
	}
}
