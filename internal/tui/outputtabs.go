package tui

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"charm.land/bubbletea/v2"

	"vroom/internal/gitinfo"
	"vroom/internal/orchestrate"
	"vroom/internal/process"
)

// ---- Tab 3: Metrics ----

// metricsSample es la muestra previa para calcular el CPU% por delta.
type metricsSample struct {
	at    time.Time
	ticks uint64
}

// metricsView es la métrica de recursos mostrada por el servicio.
type metricsView struct {
	CPU     float64
	RSSKB   int64
	Threads int
	FDs     int
	At      time.Time
}

type metricsMsg struct {
	path string
	m    process.Metrics
	err  error
}

// metricsCmd muestrea recursos del PID del servicio seleccionado (si corre).
func (m Model) metricsCmd() tea.Cmd {
	p := m.selected()
	if p == nil || !p.Configured || !m.isRunning(p.Path) {
		return nil
	}
	pid := m.services[p.Path].Meta.Pid
	return func() tea.Msg {
		mm, err := process.ReadMetrics(pid)
		return metricsMsg{path: p.Path, m: mm, err: err}
	}
}

// applyMetrics integra la muestra y calcula el CPU% por delta de ticks.
func (m *Model) applyMetrics(msg metricsMsg) {
	if msg.err != nil {
		delete(m.metrics, msg.path)
		return
	}
	now := time.Now()
	v := &metricsView{RSSKB: msg.m.RSSKB, Threads: msg.m.Threads, FDs: msg.m.FDs, At: now}
	if prev := m.metricsPrev[msg.path]; prev != nil && now.After(prev.at) {
		if d := msg.m.Ticks - prev.ticks; d > 0 {
			v.CPU = process.CPUPercent(d, now.Sub(prev.at).Seconds())
		}
	}
	m.metrics[msg.path] = v
	m.metricsPrev[msg.path] = &metricsSample{at: now, ticks: msg.m.Ticks}
}

// metricsLines renderiza la tabla de recursos del servicio seleccionado.
func (m Model) metricsLines(w int) []string {
	p := m.selected()
	if p == nil {
		return []string{styleDim.Render(trunc("select a service to see its metrics", w))}
	}
	if !p.Configured {
		return []string{styleDim.Render(trunc("no manifest — create a .vroom.toml to enable", w))}
	}
	if !m.isRunning(p.Path) {
		return []string{styleDim.Render("service not running")}
	}
	v := m.metrics[p.Path]
	if v == nil {
		return []string{styleDim.Render("sampling metrics…")}
	}
	return []string{
		styleLabel.Render(fmt.Sprintf("%-8s %-11s %-6s %-8s", "CPU%", "RSS", "FDs", "Threads")),
		fmt.Sprintf("%-8s %-11s %-6d %-8d", fmt.Sprintf("%.1f%%", v.CPU), humanKB(v.RSSKB), v.FDs, v.Threads),
		"",
		styleDim.Render("sampled at " + v.At.Format("15:04:05")),
	}
}

// humanKB formatea kB en un tamaño legible (KB/MB/GB).
func humanKB(kb int64) string {
	switch {
	case kb >= 1024*1024:
		return fmt.Sprintf("%.1f GB", float64(kb)/(1024*1024))
	case kb >= 1024:
		return fmt.Sprintf("%.1f MB", float64(kb)/1024)
	default:
		return fmt.Sprintf("%d KB", kb)
	}
}

// ---- Tab 4: Git ----

type gitMsg struct {
	path string
	st   gitinfo.Status
}

// gitCmd lee el estado git del proyecto seleccionado.
func (m Model) gitCmd() tea.Cmd {
	p := m.selected()
	if p == nil {
		return nil
	}
	path := p.Path
	return func() tea.Msg { return gitMsg{path: path, st: gitinfo.ReadStatus(path)} }
}

func (m *Model) applyGit(msg gitMsg) {
	m.gitStatus[msg.path] = msg.st
	if msg.st.Branch != "" {
		m.branches[msg.path] = msg.st.Branch
	}
}

// gitLines renderiza rama, estado y últimos commits del proyecto.
func (m Model) gitLines(w int) []string {
	p := m.selected()
	if p == nil {
		return []string{styleDim.Render(trunc("select a project to see its git status", w))}
	}
	st, ok := m.gitStatus[p.Path]
	if !ok {
		return []string{styleDim.Render("reading git status…")}
	}
	if st.Err != "" {
		return []string{styleWarn.Render(trunc("git: "+st.Err, w))}
	}
	branch := st.Branch
	if branch == "" {
		branch = "(no git repo)"
	}
	dirty := styleRunning.Render("clean")
	if st.Dirty() {
		dirty = styleWarn.Render(fmt.Sprintf("%d changed", len(st.Changed)))
	}
	lines := []string{
		styleLabel.Render(pad("branch:", 9)) + trunc(branch, max(8, w-9)),
		styleLabel.Render(pad("status:", 9)) + dirty,
		"",
	}
	if len(st.Commits) > 0 {
		lines = append(lines, styleLabel.Render("commits:"))
		for _, c := range st.Commits {
			lines = append(lines, "  "+styleDim.Render(trunc(c, max(4, w-2))))
		}
	}
	if len(st.Changed) > 0 {
		lines = append(lines, "", styleLabel.Render("changes:"))
		for _, c := range st.Changed {
			lines = append(lines, "  "+trunc(c, max(4, w-2)))
		}
	}
	return lines
}

// ---- Tab 5: Env ----

type envMsg struct {
	path string
	vars []string
	err  error
}

// envCmd lee el entorno del proceso del servicio seleccionado (si corre).
func (m Model) envCmd() tea.Cmd {
	p := m.selected()
	if p == nil || !p.Configured || !m.isRunning(p.Path) {
		return nil
	}
	pid := m.services[p.Path].Meta.Pid
	return func() tea.Msg {
		vars, err := process.ReadEnviron(pid)
		return envMsg{path: p.Path, vars: vars, err: err}
	}
}

func (m *Model) applyEnv(msg envMsg) {
	if msg.err != nil {
		delete(m.envVars, msg.path)
		return
	}
	sort.Strings(msg.vars)
	m.envVars[msg.path] = msg.vars
}

// envLines renderiza el entorno del proceso (key=value), ordenado.
func (m Model) envLines(w int) []string {
	p := m.selected()
	if p == nil {
		return []string{styleDim.Render(trunc("select a service to see its environment", w))}
	}
	if !p.Configured {
		return []string{styleDim.Render(trunc("no manifest — create a .vroom.toml to enable", w))}
	}
	if !m.isRunning(p.Path) {
		return []string{styleDim.Render("service not running — start it to inspect its environment")}
	}
	vars, ok := m.envVars[p.Path]
	if !ok {
		return []string{styleDim.Render("reading environment…")}
	}
	lines := []string{styleLabel.Render(fmt.Sprintf("%d variables", len(vars))), ""}
	for _, kv := range vars {
		lines = append(lines, trunc(kv, w))
	}
	return lines
}

// ---- Tab 6: Timeline ----

// timelineEvent es un evento operativo del servicio (start/stop/build/...).
type timelineEvent struct {
	At      time.Time
	Kind    string
	Detail  string
	Elapsed time.Duration
	OK      bool
}

// maxTimeline es el nº máximo de eventos conservados por servicio.
const maxTimeline = 100

// addEvent registra un evento en el timeline del servicio (en memoria).
func (m *Model) addEvent(path, kind, detail string, elapsed time.Duration, ok bool) {
	if path == "" {
		return
	}
	list := append(m.events[path], timelineEvent{At: time.Now(), Kind: kind, Detail: detail, Elapsed: elapsed, OK: ok})
	if len(list) > maxTimeline {
		list = list[len(list)-maxTimeline:]
	}
	m.events[path] = list
}

// timelineLines renderiza los eventos del servicio, más reciente arriba.
func (m Model) timelineLines(w int) []string {
	p := m.selected()
	if p == nil {
		return []string{styleDim.Render(trunc("select a service to see its timeline", w))}
	}
	events := m.events[p.Path]
	if len(events) == 0 {
		return []string{styleDim.Render("no events yet")}
	}
	lines := make([]string, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		mark := styleRunning.Render("✓")
		if !e.OK {
			mark = styleWarn.Render("✗")
		}
		row := fmt.Sprintf("%s %s %s", styleDim.Render(e.At.Format("15:04:05")), mark, e.Kind)
		if e.Detail != "" {
			row += " " + styleDim.Render(e.Detail)
		}
		if e.Elapsed > 0 {
			row += styleDim.Render(fmt.Sprintf(" (%s)", e.Elapsed))
		}
		lines = append(lines, truncANSI(row, w))
	}
	return lines
}

// recordStackEventByName registra en el timeline de cada servicio del stack
// el resultado de su orquestación (start/stop del stack).
func (m *Model) recordStackEventByName(name string, ok bool) {
	if m.composeFile == nil {
		return
	}
	for i := range m.composeFile.Stacks {
		s := &m.composeFile.Stacks[i]
		if s.Name != name {
			continue
		}
		seen := make(map[string]bool)
		for _, stage := range s.Stages {
			for _, svc := range stage.Services {
				if seen[svc] {
					continue
				}
				seen[svc] = true
				if p, err := orchestrate.LookupService(svc, m.projects); err == nil {
					m.addEvent(p.Path, "stack", s.Name, 0, ok)
				}
			}
		}
	}
}

// ---- Tab 7: Health ----

// healthResult es el resultado de un probe HTTP al puerto del servicio.
type healthResult struct {
	StatusCode  int
	Latency     time.Duration
	ContentType string
	Snippet     string
	Err         string
	At          time.Time
}

type healthMsg struct {
	path string
	r    *healthResult
}

// healthCmd hace un GET al puerto del manifest con timeout corto.
func (m Model) healthCmd() tea.Cmd {
	p := m.selected()
	if p == nil || !p.Configured || p.Manifest == nil || p.Manifest.Port == 0 {
		return nil
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", p.Manifest.Port, p.Manifest.HealthURLPath())
	path := p.Path
	return func() tea.Msg { return healthMsg{path: path, r: probeHealth(url)} }
}

// probeHealth ejecuta el GET y resume el resultado.
func probeHealth(url string) *healthResult {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	start := time.Now()
	resp, err := client.Get(url)
	if err != nil {
		return &healthResult{Err: err.Error(), At: time.Now()}
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &healthResult{
		StatusCode:  resp.StatusCode,
		Latency:     time.Since(start),
		ContentType: resp.Header.Get("Content-Type"),
		Snippet:     firstLine(string(body)),
		At:          time.Now(),
	}
}

// firstLine devuelve la primera línea no vacía, recortada.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return trunc(line, 100)
		}
	}
	return ""
}

// healthLines renderiza el último probe de salud del servicio.
func (m Model) healthLines(w int) []string {
	p := m.selected()
	if p == nil {
		return []string{styleDim.Render(trunc("select a service to probe its health", w))}
	}
	if !p.Configured {
		return []string{styleDim.Render(trunc("no manifest — create a .vroom.toml to enable", w))}
	}
	if p.Manifest == nil || p.Manifest.Port == 0 {
		return []string{styleDim.Render("no port configured — set port = N in .vroom.toml")}
	}
	if !m.isRunning(p.Path) {
		return []string{styleDim.Render("service not running")}
	}
	r := m.healthRes[p.Path]
	if r == nil {
		return []string{styleDim.Render("probing…")}
	}
	if r.Err != "" {
		return []string{
			styleWarn.Render(trunc("probe failed: "+r.Err, w)),
			"",
			styleDim.Render("url: http://127.0.0.1:" + fmt.Sprintf("%d%s", p.Manifest.Port, p.Manifest.HealthURLPath())),
		}
	}
	status := styleRunning.Render(fmt.Sprintf("HTTP %d", r.StatusCode))
	if r.StatusCode < 200 || r.StatusCode >= 400 {
		status = styleWarn.Render(fmt.Sprintf("HTTP %d", r.StatusCode))
	}
	lines := []string{
		styleLabel.Render(pad("status:", 9)) + status,
		styleLabel.Render(pad("latency:", 9)) + r.Latency.Round(time.Millisecond).String(),
		styleLabel.Render(pad("type:", 9)) + trunc(r.ContentType, max(8, w-9)),
		styleLabel.Render(pad("path:", 9)) + trunc(p.Manifest.HealthURLPath(), max(8, w-9)),
		"",
	}
	if r.Snippet != "" {
		lines = append(lines, styleDim.Render(trunc(r.Snippet, w)))
	}
	return lines
}
