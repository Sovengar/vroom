package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"vroom/internal/tui/bordered"
)

func lipglossWidth(s string) int {
	return lipgloss.Width(s)
}

func truncANSI(s string, w int) string {
	if lipglossWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "")
}

func (m Model) renderDashboard() string {
	left := frameBoxLines("Projects", strings.Join(fitLines(padLines(m.treeColumnLines(), m.bodyH), treeWidth), "\n"), treeWidth+boxFrame)
	right := m.rightColumnLines()

	var b strings.Builder
	for i := 0; i < m.bodyOuterH; i++ {
		var l, r string
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		b.WriteString(padW(l, treeWidth+boxFrame) + r + "\n")
	}
	b.WriteString(m.keybindsBox())
	return b.String()
}

func frameTitle(text string) string {
	return styleTitle.Render(" " + text + " ")
}

// width is the total box width and content must already be trimmed to width-boxFrame, or the compositor re-wraps and changes the height.
func frameBoxLines(title, content string, width int) []string {
	return strings.Split(bordered.RenderWithTitleEx(
		lipgloss.RoundedBorder(),
		borderFg,
		bordered.AlignLeft,
		frameTitle(title),
		content,
		width,
	), "\n")
}

func padLines(lines []string, n int) []string {
	if n < 0 {
		n = 0
	}
	if len(lines) > n {
		return lines[:n]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines
}

func fitLines(lines []string, w int) []string {
	for i := range lines {
		lines[i] = truncANSI(lines[i], w)
	}
	return lines
}

func (m Model) rightColumnLines() []string {
	outerW := m.rightW + boxFrame
	var out []string
	if m.detailsShown {
		out = append(out, frameBoxLines("Details", strings.Join(m.detailsContentLines(), "\n"), outerW)...)
	}
	out = append(out, frameBoxLines("Output", strings.Join(m.consoleContentLines(), "\n"), outerW)...)
	return out
}

func (m Model) detailsContentLines() []string {
	d := m.allDetailsLines(m.rightW)
	top := m.detailsTop
	if top < 0 {
		top = 0
	}
	if top > 0 && top > len(d)-detailsHeight {
		top = max(0, len(d)-detailsHeight)
	}
	d = d[top:]
	if len(d) > detailsHeight {
		d = d[:detailsHeight]
	}
	return fitLines(padLines(d, detailsHeight), m.rightW)
}

func (m Model) consoleContentLines() []string {
	lines := []string{m.tabsBar(m.rightW)}
	switch m.activeTab {
	case tabConsole:
		lines = append(lines, strings.Split(m.consoleView.View(), "\n")...)
	case tabThreads:
		lines = append(lines, m.threadsLines(m.rightW, m.contentH)...)
	case tabMetrics:
		lines = append(lines, m.metricsLines(m.rightW)...)
	case tabGit:
		lines = append(lines, m.gitLines(m.rightW)...)
	case tabEnv:
		lines = append(lines, m.envLines(m.rightW)...)
	case tabTimeline:
		lines = append(lines, m.timelineLines(m.rightW)...)
	case tabHealth:
		lines = append(lines, m.healthLines(m.rightW)...)
	}
	return fitLines(padLines(lines, m.contentH+1), m.rightW)
}

func (m Model) keybindsBox() string {
	innerW := m.width - boxFrame
	if innerW < 1 {
		innerW = 1
	}
	scanMethod := "walk"
	if m.usedFD {
		scanMethod = "fd"
	}
	scanW := lipglossWidth(scanMethod)

	help1 := truncANSI(dashboardHelp1(innerW, m.cfg.Keybindings), innerW)
	help2 := truncANSI(dashboardHelp2(max(1, innerW-scanW-2), m.cfg.Keybindings), max(1, innerW-scanW-1))
	padding := strings.Repeat(" ", max(0, innerW-lipglossWidth(help2)-scanW-1))
	line2 := styleHelp.Render(help2) + padding + styleDim.Render(scanMethod)

	bottom := ""
	if m.message != "" {
		bottom = styleMsg.Render(" " + trunc(m.message, max(1, innerW-2)) + " ")
	}
	return bordered.RenderWithTitlesEx(
		lipgloss.RoundedBorder(),
		borderFg,
		frameTitle("Keybinds"), bordered.AlignLeft,
		bottom, bordered.AlignRight,
		styleHelp.Render(help1)+"\n"+line2,
		m.width,
	)
}

func tabLabel(tab tabKind, active bool) string {
	text := tabLabelText(tab)
	if active {
		return styleTabActive.Render(text)
	}
	return styleTabInactive.Render(text)
}

func (m Model) tabsBar(w int) string {
	parts := make([]string, 0, tabCount)
	for k := tabKind(0); k < tabCount; k++ {
		parts = append(parts, tabLabel(k, m.activeTab == k))
	}
	bar := strings.Join(parts, " ")

	var info string
	switch m.activeTab {
	case tabConsole:
		info = m.stream.String()
		if m.consoleFollow {
			info += " · follow"
		} else {
			info += " · paused"
		}
	case tabThreads, tabMetrics, tabEnv:
		if p := m.selected(); p != nil && p.Configured {
			if sv := m.services[p.Path]; sv != nil && sv.Meta.Pid > 0 {
				info = "pid " + strconv.Itoa(sv.Meta.Pid)
			} else {
				info = "pid —"
			}
		}
	case tabHealth:
		if p := m.selected(); p != nil && p.Configured && p.Manifest != nil {
			if n := displayPort(*p, m.services[p.Path]); n > 0 {
				info = ":" + strconv.Itoa(n)
			}
		}
	case tabTimeline:
		if p := m.selected(); p != nil {
			info = fmt.Sprintf("%d events", len(m.events[p.Path]))
		}
	}
	if info != "" && lipglossWidth(bar)+2+lipglossWidth(info) <= w {
		bar = padW(bar, w-lipglossWidth(info)-1) + styleDim.Render(info)
	}
	return truncANSI(bar, w)
}

type pickerKind int

const (
	pickerTasks pickerKind = iota
	pickerAgents
)

type pickerItem struct {
	Name        string
	Description string
	agentCmd    []string // only for pickerAgents: the agent argv template
}

func (m Model) pickerBox() string {
	innerW := m.pickerInnerW()
	rows, more := m.pickerRows(m.pickerMaxRows(), innerW)
	title := "tasks — " + m.pickerTitle()
	if m.pickerKind == pickerAgents {
		title = "ask — choose an agent"
	}
	lines := []string{
		styleTitle.Render(trunc(title, innerW)),
		"",
	}
	lines = append(lines, rows...)
	if more > 0 {
		lines = append(lines, styleDim.Render(fmt.Sprintf("… %d more", more)))
	}
	lines = append(lines, styleDim.Render("j/k select · enter run · esc close"))
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

// The width must shrink with the screen because overlay never clips: a too-wide modal wraps and pushes everything below it down.
func askInnerW(width int) int {
	avail := width - 14 // borders, padding and margin
	innerW := min(avail, 110)
	innerW = max(innerW, min(72, avail)) // prefill width is a wish, the screen is the limit
	if innerW < 28 {
		innerW = 28 // the textarea needs a minimum usable width
	}
	return innerW
}

// The textarea arrives already sized by sizeAskPrompt, so it is rendered untruncated.
func (m Model) askBox() string {
	innerW := askInnerW(m.width)
	lines := []string{
		styleTitle.Render(trunc("ask "+m.askAgent.Name+" — "+m.pickerTitle(), innerW)),
		"",
		m.promptInput.View(),
		"",
		styleDim.Render("enter launch · esc cancel"),
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

func (m Model) pickerTitle() string {
	if p := m.selected(); p != nil {
		return p.Name
	}
	return "?"
}

func (m Model) pickerMaxRows() int {
	max := m.bodyH - 7 // title + blank + hint + borders + margin
	if max < 3 {
		max = 3
	}
	if max > len(m.pickerItems) {
		max = len(m.pickerItems)
	}
	return max
}

func (m Model) pickerInnerW() int {
	w := m.width - 14
	if w < 28 {
		w = 28
	}
	longest := 0
	for _, tk := range m.pickerItems {
		if n := utf8.RuneCountInString(tk.Name) + utf8.RuneCountInString(tk.Description) + 2; n > longest {
			longest = n
		}
	}
	if longest+2 < w {
		w = longest + 2
	}
	if w < 28 {
		w = 28
	}
	return w
}

func (m Model) pickerRows(maxRows, w int) (rows []string, more int) {
	items := m.pickerItems
	if maxRows <= 0 {
		return nil, len(items)
	}
	start := 0
	if len(items) > maxRows {
		start = m.pickerCursor - maxRows/2
		if start < 0 {
			start = 0
		}
		if start+maxRows > len(items) {
			start = len(items) - maxRows
		}
	}
	end := start + maxRows
	if end > len(items) {
		end = len(items)
	}
	for i := start; i < end; i++ {
		line := items[i].Name
		if items[i].Description != "" {
			line += "  " + styleDim.Render(items[i].Description)
		}
		line = truncANSI(line, w-2)
		if i == m.pickerCursor {
			rows = append(rows, stylePickerCursor.Render(padW("▶ "+line, w)))
		} else {
			rows = append(rows, padW("  "+line, w))
		}
	}
	return rows, len(items) - (end - start)
}

func (m Model) filterBarVisible() bool {
	return m.filterOpen || m.filterText != ""
}

func (m Model) treeVis() int {
	if m.filterBarVisible() {
		return m.bodyH - 1
	}
	return m.bodyH
}

// The top clamp avoids slicing out of range when the tree shrinks by collapsing with a stale treeTop.
func (m Model) treeColumnLines() []string {
	tree, _ := m.treeLines()
	visH := m.bodyH
	var extra []string
	if m.filterBarVisible() {
		visH = m.treeVis()
		extra = []string{m.filterBar()}
		if len(m.tree) == 0 {
			extra = append(extra, styleDim.Render("no matches"))
		}
	}
	top := m.treeTop
	if top > len(tree)-1 {
		top = len(tree) - 1
	}
	if top < 0 {
		top = 0
	}
	tree = tree[top:]
	if len(tree) > visH {
		tree = tree[:visH]
	}
	return append(extra, tree...)
}

func (m Model) filterBar() string {
	if m.filterOpen {
		return m.filterInput.View()
	}
	return styleDim.Render(trunc(fmt.Sprintf("⌕ %s · %d", m.filterText, len(m.entries)), treeWidth-2))
}

func overlay(base, box string, width, height int) string {
	lines := strings.Split(base, "\n")
	blocks := strings.Split(box, "\n")
	bw := lipglossWidth(blocks[0])
	bh := len(blocks)
	if bh > height {
		bh = height
	}
	// Both clamps are reachable (a modal bigger than the screen) and a negative x or y would index before the start of lines.
	x := max((width-bw)/2, 0)
	y := max((height-bh)/2, 0)
	for j := 0; j < bh && y+j < len(lines); j++ {
		line := lines[y+j]
		lines[y+j] = ansi.Truncate(line, x, "") + blocks[j] + ansi.TruncateLeft(line, x+bw, "")
	}
	return strings.Join(lines, "\n")
}
