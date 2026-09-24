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

// renderDashboard compone el dashboard completo como una fila de cajas con
// borde redondeado y título: Projects (izquierda, alto completo), la columna
// derecha (Details arriba + Output abajo) y, bajo ambas, la caja Keybinds a
// todo el ancho. El mensaje de estado se dibuja como leyenda del borde
// inferior de la caja de keybinds.
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

// frameTitle compone el título de una caja: el texto con un espacio a cada
// lado para separarlo del trazo del borde.
func frameTitle(text string) string {
	return styleTitle.Render(" " + text + " ")
}

// frameBoxLines dibuja una caja redondeada con título superior y devuelve sus
// líneas. width es el ancho TOTAL (marco incluido); content debe venir ya
// recortado al ancho interior (width-boxFrame) para que no haya re-wrap.
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

// padLines rellena lines con líneas vacías hasta n (y recorta si sobran).
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

// fitLines recorta cada línea a w celdas visibles (ANSI-aware) para que el
// compositor de cajas no tenga que envolverlas y altere el alto.
func fitLines(lines []string, w int) []string {
	for i := range lines {
		lines[i] = truncANSI(lines[i], w)
	}
	return lines
}

// rightColumnLines compone la columna derecha como dos cajas apiladas:
// Details (si cabe) arriba y Output abajo, cada una del ancho rightW+boxFrame.
func (m Model) rightColumnLines() []string {
	outerW := m.rightW + boxFrame
	var out []string
	if m.detailsShown {
		out = append(out, frameBoxLines("Details", strings.Join(m.detailsContentLines(), "\n"), outerW)...)
	}
	out = append(out, frameBoxLines("Output", strings.Join(m.consoleContentLines(), "\n"), outerW)...)
	return out
}

// detailsContentLines devuelve exactamente detailsHeight líneas de detalle del
// item seleccionado, aplicando el scroll vertical.
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

// consoleContentLines devuelve exactamente contentH+1 líneas: la barra de
// pestañas y el contenido de la pestaña activa (consola o hilos).
func (m Model) consoleContentLines() []string {
	lines := []string{m.tabsBar(m.rightW)}
	if m.activeTab == tabConsole {
		lines = append(lines, strings.Split(m.consoleView.View(), "\n")...)
	} else {
		lines = append(lines, m.threadsLines(m.rightW, m.contentH)...)
	}
	return fitLines(padLines(lines, m.contentH+1), m.rightW)
}

// keybindsBox dibuja la caja inferior a todo el ancho con las dos líneas de
// ayuda y, si hay mensaje de estado, la leyenda en el borde inferior derecho.
// Ambas líneas se recortan al ancho interior (ANSI-aware) para que el
// compositor no las envuelva y rompa el alto fijo de la caja.
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

// tabLabel renderiza la etiqueta de una pestaña: activa con fondo
// invertido, inactiva atenuada.
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

// tabsBar dibuja las pestañas con la activa resaltada y, a la
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
	pickerTasks  pickerKind = iota // tasks de mise
	pickerAgents                   // agentes de IA
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
// render del box y el width del textarea): crece con la pantalla hasta
// un cap (110) para que el prefill del template quepa
// en una línea en pantallas normales.
func askInnerW(width int) int {
	innerW := 72
	if w := width - 14; w > innerW {
		innerW = w
	}
	if innerW > 110 {
		innerW = 110
	}
	if innerW < 28 {
		innerW = 28
	}
	return innerW
}

// askBox renderiza el modal del prompt de ask AI. El
// textarea es multi-línea con alto dinámico (crece con el contenido
// hasta el cap de pantalla, luego scroll interno) y llega ya
// dimensionado por sizeAskPrompt, así que se renderiza sin truncar.
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

// ---- Filtro del árbol ----

// filterBarVisible reporta si la barra del filtro ocupa la primera línea
// de la columna del árbol: box abierto o filtro aplicado.
func (m Model) filterBarVisible() bool {
	return m.filterOpen || m.filterText != ""
}

// treeVis es el alto visible del árbol: la barra consume su
// primera línea cuando es visible.
func (m Model) treeVis() int {
	if m.filterBarVisible() {
		return m.bodyH - 1
	}
	return m.bodyH
}

// treeColumnLines compone las líneas de la columna de árbol: rebanadas
// por treeTop (fix: el auto-scroll ahora sí tiene efecto
// visual), capadas al alto visible y, si la barra es visible, con el
// filtro en la línea 0 y "no matches" si el árbol quedó vacío.
// El clamp de top evita rebanar fuera cuando el árbol se
// reduce (plegado) con un treeTop ya obsoleto.
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

// filterBar dibuja la línea de filtro: el input (prompt "/" + cursor)
// si el box está abierto; con filtro aplicado, el indicador persistente
// `⌕ texto · n` en dim (n = proyectos matcheados).
func (m Model) filterBar() string {
	if m.filterOpen {
		return m.filterInput.View()
	}
	return styleDim.Render(trunc(fmt.Sprintf("⌕ %s · %d", m.filterText, len(m.entries)), treeWidth-2))
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
