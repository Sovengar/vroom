package tui

import (
	"fmt"
	"strings"

	"vroom/internal/scanner"
)

// commandColGap separa la columna meta de la columna de comandos.
const commandColGap = 2

// detailsLines renderiza el panel de detalles del servicio seleccionado
// (spec 0002 R21): cabecera con nombre+estado y, debajo, dos columnas —
// meta a la izquierda (path, rama, puerto...) y los comandos del
// manifiesto (start/stop/install/build) a la derecha. La altura la fija
// detailsHeight en rightLines.
func (m Model) detailsLines(w int) []string {
	p := m.selected()
	if p == nil {
		if pr, sec := m.selectedNode(); pr != "" { // R24 + 0006 R39: resumen del nodo
			return m.groupDetailsLines(pr, sec, w)
		}
		return []string{styleDim.Render("No project selected")}
	}
	sv := m.services[p.Path]
	header := trunc(p.Name, w) + "  " + statusBadge(*p, sv)

	if !p.Configured {
		lines := []string{truncANSI(header, w)}
		lines = append(lines, styleWarn.Render("No manifest — create a .vroom.toml"))
		lines = append(lines, styleDim.Render(trunc(exampleManifest(p.Name), w)))
		return clipLines(lines, detailsHeight, w)
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
	return clipLines(append([]string{truncANSI(header, w)}, rows...), detailsHeight, w)
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
	row("language:", p.Language)
	if b := m.branches[p.Path]; b != "" { // R21: rama git
		row("branch:", b)
	}
	if p.Manifest != nil && p.Manifest.PrimaryGroup != "" {
		// 0006 R39: el compuesto primario/secundario cuando exista
		// secundario (solo con primario; S39.4).
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

// groupDetailsLines muestra el resumen del nodo seleccionado (R24 +
// 0006 R39): conteo running/total y los miembros con su punto de estado.
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
		lines = append(lines, treeDot(p, m.services[p.Path])+" "+trunc(p.Name, w-3))
	}
	return clipLines(lines, detailsHeight, w)
}

// pad rellena s con espacios a n runes (para etiquetas sin ANSI).
func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
