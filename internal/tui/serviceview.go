package tui

import (
	"fmt"
	"strings"

	"vroom/internal/orchestrate"
	"vroom/internal/scanner"
)

// commandColGap separa la columna meta de la columna de comandos.
const commandColGap = 2

// detailsLines renderiza el panel de detalles del servicio seleccionado
// Cabecera con nombre+estado y, debajo, dos columnas —
// meta a la izquierda (path, rama, puerto...) y los comandos del
// manifiesto (start/stop/install/build) a la derecha. Recorta a
// detailsHeight para uso en tests y otros contexts.
func (m Model) detailsLines(w int) []string {
	return clipLines(m.allDetailsLines(w), detailsHeight, w)
}

// allDetailsLines devuelve todas las líneas de detalles SIN recortar.
// Usado por rightLines que aplica scroll offset.
func (m Model) allDetailsLines(w int) []string {
	p := m.selected()
	if p == nil {
		if s := m.selectedStack(); s != nil {
			return m.stackDetailsLines(s, w)
		}
		if pr, sec := m.selectedNode(); pr != "" { // Resumen del nodo
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

	// Dos columnas: meta (55%) | comandos. En panel estrecho cae a una.
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

// metaColumn compone la columna izquierda del panel de detalles: los
// campos del servicio sin los comandos (van en su propia columna).
func (m Model) metaColumn(p scanner.Project, sv *ServiceState, w int) []string {
	var lines []string
	valueW := max(8, w-11)
	row := func(label, value string) {
		lines = append(lines, styleLabel.Render(pad(label, 10))+truncTail(value, valueW))
	}
	row("path:", p.Path)
	if b := m.branches[p.Path]; b != "" { // Rama git
		row("branch:", b)
	}
	if p.Manifest != nil && p.Manifest.PrimaryGroup != "" {
		// El compuesto primario/secundario cuando exista
		// secundario (solo con primario).
		g := p.Manifest.PrimaryGroup
		if p.Manifest.SecondaryGroup != "" {
			g += "/" + p.Manifest.SecondaryGroup
		}
		row("group:", g)
	}
	if p.Manifest != nil && p.Manifest.Port > 0 {
		row("port:", fmt.Sprintf("%d", p.Manifest.Port))
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

// commandsColumn compone la columna derecha del panel de detalles: los
// 4 comandos del manifiesto; los no configurados se muestran con "—".
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

// zipColumns compone dos columnas lado a lado: la izquierda rellena a
// su ancho (ANSI-aware) y la derecha se trunca al suyo.
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

// clipLines recorta lines a h filas, cada una a w columnas visibles.
func clipLines(lines []string, h, w int) []string {
	if len(lines) > h {
		lines = lines[:h]
	}
	for i := range lines {
		lines[i] = truncANSI(lines[i], w)
	}
	return lines
}

// groupDetailsLines muestra el resumen del nodo seleccionado:
// conteo running/total y los miembros con su punto de estado.
// El título usa el nombre del secundario si es un nodo secundario.
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
		if p.Manifest != nil && p.Manifest.Port > 0 {
			label += styleDim.Render(fmt.Sprintf(":%d", p.Manifest.Port))
		}
		lines = append(lines, treeDot(p, m.services[p.Path], m.spinner.View(), m.startSpinner.View())+" "+label)
	}
	return lines
}

// pad rellena s con espacios a n runes (para etiquetas sin ANSI).
func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// stackDetailsLines muestra el panel de detalles de un stack seleccionado
// Nombre + [stack] + estado, tipo, etapas, servicios con
// su estado y puerto (igual que groupDetailsLines).
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
			// Resolución con el criterio compartido: ante un
			// nombre ambiguo no se elige arbitrariamente el primero.
			p, err := orchestrate.LookupService(name, m.projects)
			if err != nil {
				lines = append(lines, "    "+styleWarn.Render("⚠")+" "+label)
				continue
			}
			if p.Manifest.Port > 0 {
				label += styleDim.Render(fmt.Sprintf(":%d", p.Manifest.Port))
			}
			dot := treeDot(p, m.services[p.Path], m.spinner.View(), m.startSpinner.View())
			lines = append(lines, "    "+dot+" "+label)
		}
	}
	return lines
}
