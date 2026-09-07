package tui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// lipglossWidth devuelve el ancho visible de s ignorando secuencias ANSI.
func lipglossWidth(s string) int {
	return lipgloss.Width(s)
}

// truncANSI recorta s a un ancho visible w conservando las secuencias
// ANSI (para líneas ya estilizadas).
func truncANSI(s string, w int) string {
	if lipglossWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "")
}

// renderDashboard compone el dashboard completo (spec 0002 R18):
// header, columna de árbol a la izquierda con separador vertical, panel
// derecho (detalles + pestañas) y barra de ayuda/mensajes.
func (m Model) renderDashboard() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render(trunc("vroom — projects in "+m.root, m.width)) + "\n")

	tree, _ := m.treeLines()
	right := m.rightLines()
	for i := 0; i < m.bodyH; i++ {
		l := ""
		if i < len(tree) {
			l = tree[i]
		}
		r := ""
		if i < len(right) {
			r = right[i]
		}
		b.WriteString(padW(l, treeWidth) + styleSep.Render("│") + r + "\n")
	}
	b.WriteString(styleHelp.Render(trunc(dashboardHelp(m.width), m.width)) + "\n")
	if m.message != "" {
		b.WriteString(styleMsg.Render(trunc("ℹ "+m.message, m.width)))
	}
	return b.String()
}

// rightLines compone el panel derecho: detalles (si visibles), separador
// horizontal, barra de pestañas y contenido de la pestaña activa.
func (m Model) rightLines() []string {
	lines := make([]string, 0, m.bodyH)
	if m.detailsShown {
		d := m.detailsLines(m.rightW)
		for len(d) < detailsHeight {
			d = append(d, "")
		}
		lines = append(lines, d...)
		lines = append(lines, styleSep.Render(strings.Repeat("─", m.rightW)))
	}
	lines = append(lines, m.tabsBar(m.rightW))
	if m.activeTab == tabConsole {
		lines = append(lines, strings.Split(m.consoleView.View(), "\n")...)
	} else {
		lines = append(lines, m.threadsLines(m.rightW, m.contentH)...)
	}

	out := make([]string, m.bodyH)
	for i := range out {
		if i < len(lines) {
			out[i] = truncANSI(lines[i], m.rightW)
		}
	}
	return out
}

// tabLabel renderiza la etiqueta de una pestaña: activa con fondo
// invertido, inactiva atenuada (S18.4).
func tabLabel(tab tabKind, active bool) string {
	text := "[1] Console"
	if tab == tabThreads {
		text = "[2] Threads"
	}
	if active {
		return styleTabActive.Render(text)
	}
	return styleTabInactive.Render(text)
}

// tabsBar dibuja las pestañas con la activa resaltada (S18.4) y, a la
// derecha, el estado del stream o del proceso muestreado.
func (m Model) tabsBar(w int) string {
	var info string
	if m.activeTab == tabConsole {
		info = m.stream.String()
		if m.consoleFollow {
			info += " · follow"
		} else {
			info += " · paused"
		}
	} else if p := m.selected(); p != nil && p.Configured {
		if sv := m.services[p.Path]; sv != nil && sv.Meta.Pid > 0 {
			info = "pid " + strconv.Itoa(sv.Meta.Pid)
		} else {
			info = "pid —"
		}
	}
	bar := tabLabel(tabConsole, m.activeTab == tabConsole) + " " +
		tabLabel(tabThreads, m.activeTab == tabThreads)
	if info != "" && lipglossWidth(bar)+1+lipglossWidth(info) <= w {
		bar = padW(bar, w-lipglossWidth(info)-1) + styleDim.Render(info)
	}
	return truncANSI(bar, w)
}

// ---- Modal genérico: tasks de mise y agentes de IA ----

// pickerKind identifica el tipo de modal de selección.
type pickerKind int

const (
	pickerTasks  pickerKind = iota // tasks de mise (0003 R28)
	pickerAgents                   // agentes de IA (0004 R32)
)

// pickerItem es una fila del modal de selección.
type pickerItem struct {
	Name        string
	Description string
	agentCmd    []string // solo pickerAgents: plantilla argv del agente
}

// pickerBox renderiza el modal: título, lista con el cursor resaltado
// y ventana deslizante si no caben todos.
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

// askInnerW es el ancho interior del modal ask (compartido entre el
// render del box y el width del input): crece con la pantalla hasta un
// cap (88) para que el prefill del template (spec 0005 R35) sea legible.
func askInnerW(width int) int {
	innerW := 56
	if w := width - 14; w > innerW {
		innerW = w
	}
	if innerW > 88 {
		innerW = 88
	}
	if innerW < 28 {
		innerW = 28
	}
	return innerW
}

// askBox renderiza el modal del prompt de ask AI (0004 R32). El input
// renderiza su propia ventana alrededor del cursor (width fijado en
// startAskPrompt), así que truncANSI queda como no-op y el cursor al
// final del prefill siempre es visible.
func (m Model) askBox() string {
	innerW := askInnerW(m.width)
	lines := []string{
		styleTitle.Render(trunc("ask "+m.askAgent.Name+" — "+m.pickerTitle(), innerW)),
		"",
		truncANSI(m.promptInput.View(), innerW),
		"",
		styleDim.Render("enter launch · esc cancel"),
	}
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

// pickerTitle es el nombre del proyecto mostrado en el título del modal.
func (m Model) pickerTitle() string {
	if p := m.selected(); p != nil {
		return p.Name
	}
	return "?"
}

// pickerMaxRows es el alto de la lista del modal (según la pantalla).
func (m Model) pickerMaxRows() int {
	max := m.bodyH - 7 // título + blank + hint + bordes + margen
	if max < 3 {
		max = 3
	}
	if max > len(m.pickerItems) {
		max = len(m.pickerItems)
	}
	return max
}

// pickerInnerW es el ancho interior del modal: cabe la fila más larga
// sin exceder la pantalla.
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

// pickerRows devuelve las filas visibles con el cursor resaltado; more
// son las filas ocultas por la ventana deslizante.
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

// overlay compone box centrado sobre base sin perder el contenido
// circundante (recortes ANSI-safe).
func overlay(base, box string, width, height int) string {
	lines := strings.Split(base, "\n")
	blocks := strings.Split(box, "\n")
	bw := lipglossWidth(blocks[0])
	bh := len(blocks)
	if bh > height {
		bh = height
	}
	x := (width - bw) / 2
	if x < 0 {
		x = 0
	}
	y := (height - bh) / 2
	if y < 0 {
		y = 0
	}
	for j := 0; j < bh && y+j < len(lines); j++ {
		line := lines[y+j]
		lines[y+j] = ansi.Truncate(line, x, "") + blocks[j] + ansi.TruncateLeft(line, x+bw, "")
	}
	return strings.Join(lines, "\n")
}
