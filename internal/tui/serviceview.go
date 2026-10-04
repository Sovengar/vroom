package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"vroom/internal/orchestrate"
	"vroom/internal/portless"
	"vroom/internal/scanner"
)

const commandColGap = 2

func (m Model) detailsLines(w int) []string {
	return clipLines(m.allDetailsLines(w), detailsHeight, w)
}

func (m Model) allDetailsLines(w int) []string {
	p := m.selected()
	if p == nil {
		if s := m.selectedStack(); s != nil {
			return m.stackDetailsLines(s, w)
		}
		if pr, sec := m.selectedNode(); pr != "" {
			return m.groupDetailsLines(pr, sec, w)
		}
		return []string{styleDim.Render("No project selected")}
	}
	sv := m.services[p.Path]
	header := trunc(p.Name, w) + "  " + statusBadge(*p, sv, m.spinner.View(), m.startSpinner.View())

	if !p.Configured {
		lines := []string{truncANSI(header, w)}
		lines = append(lines, styleWarn.Render("No manifest — create a .vroom.toml"))
		lines = append(lines, styleDim.Render(trunc(exampleManifest(p.Name), w)))
		return lines
	}

	metaW := w * 55 / 100
	cmdW := w - metaW - commandColGap
	if cmdW < 16 {
		metaW = w
		cmdW = 0
	}
	meta := m.metaColumn(*p, sv, metaW)
	var rows []string
	if cmdW > 0 {
		rows = zipColumns(meta, m.commandsColumn(*p, cmdW), metaW, cmdW)
	} else {
		rows = meta
	}
	return append([]string{truncANSI(header, w)}, rows...)
}

func (m Model) metaColumn(p scanner.Project, sv *ServiceState, w int) []string {
	var lines []string
	valueW := max(8, w-11)
	row := func(label, value string) {
		lines = append(lines, styleLabel.Render(pad(label, 10))+truncTail(value, valueW))
	}
	row("path:", p.Path)
	if b := m.branches[p.Path]; b != "" {
		row("branch:", b)
	}
	if p.Manifest != nil && p.Manifest.PrimaryGroup != "" {
		g := p.Manifest.PrimaryGroup
		if p.Manifest.SecondaryGroup != "" {
			g += "/" + p.Manifest.SecondaryGroup
		}
		row("group:", g)
	}
	if n := displayPort(p, sv); n > 0 {
		row("port:", fmt.Sprintf("%d", n))
	}
	if u := displayRouteURL(p, sv); u != "" {
		row("url:", u)
	}
	if p.Manifest != nil && p.Manifest.ProcessPattern != "" {
		row("pattern:", p.Manifest.ProcessPattern)
	}
	if sv.Meta.Pid > 0 {
		row("pid/pgid:", fmt.Sprintf("%d / %d", sv.Meta.Pid, sv.Meta.Pgid))
	}
	if sv.Meta.StartedAt != "" {
		row("started:", sv.Meta.StartedAt)
	}
	row("logs:", m.store.ServiceDir(p.Path))
	return lines
}

// Read from persisted Meta and never probed live: the TUI refreshes every 2s, so probing the proxy there would spawn once per service, and a degraded route has no URL to show.
func displayRouteURL(p scanner.Project, sv *ServiceState) string {
	if sv == nil || sv.Meta.RouteStatus != portless.StatusRegistered {
		return ""
	}
	return sv.Meta.RouteURL
}

func (m Model) commandsColumn(p scanner.Project, w int) []string {
	valueW := max(4, w-9)
	row := func(label, value string) string {
		if value == "" {
			return styleLabel.Render(pad(label, 9)) + styleDim.Render("—")
		}
		return styleLabel.Render(pad(label, 9)) + truncTail(value, valueW)
	}
	return []string{
		row("start:", p.Manifest.Command),
		row("stop:", p.Manifest.Stop),
		row("install:", p.Manifest.Install),
		row("build:", p.Manifest.Build),
	}
}

func zipColumns(a, b []string, aw, bw int) []string {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	gap := strings.Repeat(" ", commandColGap)
	out := make([]string, n)
	for i := 0; i < n; i++ {
		l, r := "", ""
		if i < len(a) {
			l = a[i]
		}
		if i < len(b) {
			r = b[i]
		}
		out[i] = padW(truncANSI(l, aw), aw) + gap + truncANSI(r, bw)
	}
	return out
}

func clipLines(lines []string, h, w int) []string {
	if len(lines) > h {
		lines = lines[:h]
	}
	for i := range lines {
		lines[i] = truncANSI(lines[i], w)
	}
	return lines
}

func (m Model) groupDetailsLines(primary, secondary string, w int) []string {
	label := primary
	if secondary != "" {
		label = secondary
	}
	r, n := m.nodeStats(primary, secondary)
	lines := []string{trunc(fmt.Sprintf("%s (%d/%d)", label, r, n), w)}
	lines = append(lines,
		styleLabel.Render(pad("services:", 10))+fmt.Sprintf("%d", n),
		styleLabel.Render(pad("running:", 10))+fmt.Sprintf("%d", r),
	)
	for _, p := range m.nodeMembers(primary, secondary) {
		label := trunc(p.Name, w-3)
		if n := displayPort(p, m.services[p.Path]); n > 0 {
			label += styleDim.Render(fmt.Sprintf(":%d", n))
		}
		lines = append(lines, treeDot(p, m.services[p.Path], m.spinner.View(), m.startSpinner.View())+" "+label)
	}
	return lines
}

// Measured: it padded to n BYTES, invisible today because every label is ASCII, but the first accented label would shift the column; counted in runes because that is what the terminal measures in the BMP.
func pad(s string, n int) string {
	if r := utf8.RuneCountInString(s); r >= n {
		return s
	} else {
		return s + strings.Repeat(" ", n-r)
	}
}

func (m Model) stackDetailsLines(s *orchestrate.Stack, w int) []string {
	r, n, conflict := m.stackStats(s)
	running := ""
	if r > 0 {
		running = fmt.Sprintf("  %s", styleRunning.Render("● running"))
	} else {
		running = fmt.Sprintf("  %s", styleStopped.Render("· stopped"))
	}
	header := trunc(fmt.Sprintf("🎵 %s [stack]%s", s.Name, running), w)
	lines := []string{truncANSI(header, w)}
	row := func(label, value string) {
		lines = append(lines, styleLabel.Render(pad(label, 10))+truncTail(value, max(8, w-11)))
	}
	row("type:", "orchestration stack")
	row("stages:", fmt.Sprintf("%d", len(s.Stages)))
	row("services:", fmt.Sprintf("%d (%d running)", n, r))
	row("group:", s.PrimaryGroup)
	if conflict != nil {
		lines = append(lines, styleWarn.Render(trunc("⚠ "+conflict.Error(), w)))
	}
	lines = append(lines, "")
	for _, stage := range s.Stages {
		lines = append(lines, styleDim.Render(trunc(fmt.Sprintf("  %s:", stage.Name), w-4)))
		for _, name := range stage.Services {
			label := trunc(name, w-5)
			// An ambiguous service name warns instead of picking the first match, the same rule as the engine and the CLI.
			p, err := orchestrate.LookupService(name, m.projects)
			if err != nil {
				lines = append(lines, "    "+styleWarn.Render("⚠")+" "+label)
				continue
			}
			if n := displayPort(p, m.services[p.Path]); n > 0 {
				label += styleDim.Render(fmt.Sprintf(":%d", n))
			}
			dot := treeDot(p, m.services[p.Path], m.spinner.View(), m.startSpinner.View())
			lines = append(lines, "    "+dot+" "+label)
		}
	}
	return lines
}
