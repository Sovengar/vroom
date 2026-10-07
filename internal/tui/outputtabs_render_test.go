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

func asPanel(t *testing.T, m Model, f func(Model, int) []string) string {
	t.Helper()
	return tail.StripANSI(strings.Join(f(m, m.rightW), "\n"))
}

func TestSamplingPanelsSayWhyTheyHaveNoData(t *testing.T) {
	panels := []struct {
		name   string
		render func(Model, int) []string
		wants  string
	}{
		{"metrics", Model.metricsLines, "select a service"},
		{"git", Model.gitLines, "select a project"},
		{"env", Model.envLines, "select a service"},
		{"timeline", Model.timelineLines, "select a service"},
		{"health", Model.healthLines, "select a service"},
	}
	for _, p := range panels {
		t.Run(p.name+"/no selection", func(t *testing.T) {
			m := noSelection(t)
			got := asPanel(t, m, p.render)
			if !strings.Contains(got, p.wants) {
				t.Errorf("%s with no selection = %q, want an invitation to choose", p.name, got)
			}
		})
	}

	for _, p := range []struct {
		name   string
		render func(Model, int) []string
		wants  string
	}{
		{"metrics", Model.metricsLines, "no manifest"},
		{"env", Model.envLines, "no manifest"},
		{"health", Model.healthLines, "no manifest"},
	} {
		t.Run(p.name+"/no manifest", func(t *testing.T) {
			m := noManifest(t)
			got := asPanel(t, m, p.render)
			if !strings.Contains(got, p.wants) {
				t.Errorf("%s with no manifest = %q", p.name, got)
			}
			if strings.Contains(got, "select a service") {
				t.Errorf("%s = %q: the project is selected, the reason is different", p.name, got)
			}
		})
	}
}

func TestSamplingPanelsSayServiceNotRunning(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Status = statusStopped

	for _, p := range []struct {
		name   string
		render func(Model, int) []string
		wants  string
	}{
		{"metrics", Model.metricsLines, "not running"},
		{"env", Model.envLines, "start it"},
		{"health", Model.healthLines, "not running"},
	} {
		t.Run(p.name, func(t *testing.T) {
			if got := asPanel(t, m, p.render); !strings.Contains(got, p.wants) {
				t.Errorf("%s with the service stopped = %q, want %q", p.name, got, p.wants)
			}
		})
	}
}

func TestSamplingPanelsSayTheyAreReading(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	setManifestPort(&m, "tienda-api", 4321)

	for _, p := range []struct {
		name   string
		render func(Model, int) []string
		wants  string
	}{
		{"metrics", Model.metricsLines, "sampling"},
		{"env", Model.envLines, "reading"},
		{"health", Model.healthLines, "probing"},
		{"timeline", Model.timelineLines, "no events"},
	} {
		t.Run(p.name, func(t *testing.T) {
			if got := asPanel(t, m, p.render); !strings.Contains(got, p.wants) {
				t.Errorf("%s = %q, want %q", p.name, got, p.wants)
			}
		})
	}

	// git reads per project, not per live process, so it stays in the reading state whatever the run status.
	t.Run("git", func(t *testing.T) {
		if got := asPanel(t, m, Model.gitLines); !strings.Contains(got, "reading") {
			t.Errorf("git = %q, want it to say it is reading", got)
		}
	})
}

func TestGitLinesDistinguishesRepoFromNonRepoAndCleanFromDirty(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	t.Run("clean repo", func(t *testing.T) {
		m.gitStatus[path] = gitinfo.Status{Branch: "main", Commits: []string{"abc1234 first"}}
		got := asPanel(t, m, Model.gitLines)
		for _, want := range []string{"branch:", "main", "status:", "clean", "commits:", "abc1234"} {
			if !strings.Contains(got, want) {
				t.Errorf("a clean repo does not bring %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "changes:") {
			t.Errorf("a clean repo cannot bring a changes section:\n%s", got)
		}
	})

	t.Run("dirty repo", func(t *testing.T) {
		m.gitStatus[path] = gitinfo.Status{
			Branch:  "feature/login",
			Changed: []string{" M app.go", "?? new.go"},
		}
		got := asPanel(t, m, Model.gitLines)
		if !strings.Contains(got, "2 changed") {
			t.Errorf("the change count must be the number, not a \"dirty\":\n%s", got)
		}
		if !strings.Contains(got, "changes:") || !strings.Contains(got, "new.go") {
			t.Errorf("the panel must list the files:\n%s", got)
		}
	})

	t.Run("no repo", func(t *testing.T) {
		m.gitStatus[path] = gitinfo.Status{}
		if got := asPanel(t, m, Model.gitLines); !strings.Contains(got, "no git repo") {
			t.Errorf("a project outside git = %q, want the label of project outside git, because it is not an error", got)
		}
	})

	t.Run("git fails", func(t *testing.T) {
		m.gitStatus[path] = gitinfo.Status{Err: "no such repository"}
		got := asPanel(t, m, Model.gitLines)
		if !strings.Contains(got, "no such repository") {
			t.Errorf("a git failure must appear: %q", got)
		}
		if strings.Contains(got, "clean") {
			t.Errorf("a git failure cannot show as clean, because it would be lying about the state:\n%s", got)
		}
	})
}

func TestHealthLinesShowsBodyWhenProbeReturnsOne(t *testing.T) {
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

	got := asPanel(t, m, Model.healthLines)
	for _, want := range []string{"status:", "HTTP 500", "latency:", "type:", "application/json", `{"error":"boom"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("the health panel does not bring %q:\n%s", want, got)
		}
	}

	m.healthRes[path].Snippet = ""
	if got := asPanel(t, m, Model.healthLines); strings.Contains(got, "snippet") {
		t.Errorf("without snippet there is nothing to show:\n%s", got)
	}
}

func TestHealthLinesDistinguishes200From404ByCode(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	setManifestPort(&m, "tienda-api", 4321)

	for _, code := range []int{200, 204, 301, 302, 400, 404, 500, 503} {
		m.healthRes[path] = &healthResult{StatusCode: code, ContentType: "text/plain"}
		got := asPanel(t, m, Model.healthLines)
		if !strings.Contains(got, "HTTP "+strconv.Itoa(code)) {
			t.Errorf("code %d does not appear: %q", code, got)
		}
	}
}

func TestHealthLinesDoesNotProbeWithPortlessManifestAndSaysSo(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	m.services[path].Meta = state.Meta{State: state.StateStopped, Port: 0}
	if p := m.projectByPath(path); p != nil && p.Manifest != nil {
		p.Manifest.Port = 0
	}

	got := asPanel(t, m, Model.healthLines)
	if !strings.Contains(got, "port = N") {
		t.Errorf("= %q, want it to say WHAT to write in the manifest", got)
	}
}

func TestEnvLinesCountsAndListsVariables(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))
	m.envVars[path] = []string{"A=1", "B=2", "C=3"}

	got := asPanel(t, m, Model.envLines)
	if !strings.Contains(got, "3 variables") {
		t.Errorf("= %q, want the count", got)
	}
	for _, want := range []string{"A=1", "B=2", "C=3"} {
		if !strings.Contains(got, want) {
			t.Errorf("does not list %q:\n%s", want, got)
		}
	}
}

// Inverted from log order on purpose: the cursor already sits at the end, so "what just happened" must come first.
func TestTimelineLinesOrdersFromNewestToOldest(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)

	base := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	m.events[path] = []timelineEvent{
		{At: base, Kind: "start", Detail: "started", Elapsed: 0, OK: true},
		{At: base.Add(time.Minute), Kind: "build", Detail: "failed", Elapsed: 2 * time.Second, OK: false},
		{At: base.Add(2 * time.Minute), Kind: "stop", Detail: "", Elapsed: time.Millisecond, OK: true},
	}

	got := asPanel(t, m, Model.timelineLines)
	iStop := strings.Index(got, "stop")
	iBuild := strings.Index(got, "build")
	iStart := strings.Index(got, "start")

	if iStop < 0 || iBuild < 0 || iStart < 0 {
		t.Fatalf("missing events:\n%s", got)
	}
	if iStop >= iBuild || iBuild >= iStart {
		t.Errorf("the order is chronological, want the most recent first:\n%s", got)
	}
	if !strings.Contains(got, "✓") || !strings.Contains(got, "✗") {
		t.Errorf("each event must bring its result mark:\n%s", got)
	}
	if !strings.Contains(got, "(2s)") {
		t.Errorf("an event with duration shows it:\n%s", got)
	}
	if strings.Contains(got, "(0s)") {
		t.Errorf("an instant event does not show duration:\n%s", got)
	}
}

func TestMetricsLinesSamplesAndNotesTime(t *testing.T) {
	m, _ := newTestModel(t)
	m = moveCursorTo(t, m, "tienda-api")
	path := pathOfSelected(t, m)
	markRunning(&m, path, livePID(t))

	now := time.Date(2026, 10, 3, 15, 4, 5, 0, time.Local)
	m.metrics[path] = &metricsView{CPU: 12.5, RSSKB: 1536, FDs: 42, Threads: 8, At: now}

	got := asPanel(t, m, Model.metricsLines)
	for _, want := range []string{"CPU%", "RSS", "FDs", "Threads", "12.5%", "1.5 MB", "42", "8", "sampled at"} {
		if !strings.Contains(got, want) {
			t.Errorf("the table does not bring %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, now.Format("15:04:05")) {
		t.Errorf("missing the sampling time: %q", got)
	}
}

// A real httptest server, not a stub: an invented Content-Type and a snippet not taken from the response are the probe's two failure modes.
func TestProbeHealthReturnsRealBodyAndContentType(t *testing.T) {
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
		t.Fatalf("the probe against a real server failed: %v", res.Err)
	}
	if res.StatusCode != 200 {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(res.ContentType, "json") {
		t.Errorf("content-type = %q, want the one the server returns", res.ContentType)
	}
	if !strings.Contains(res.Snippet, `"ok":true`) {
		t.Errorf("snippet = %q, want the real body", res.Snippet)
	}

	m.healthRes[path] = res
	got := asPanel(t, m, Model.healthLines)
	for _, want := range []string{"HTTP 200", "json", `/api/health`, `"ok":true`} {
		if !strings.Contains(got, want) {
			t.Errorf("the panel does not bring %q:\n%s", want, got)
		}
	}
}
