package tui

import (
	"fmt"
	"strings"
)

// detailsLines renderiza el panel de detalles del servicio seleccionado
// (spec 0002 R21): campos de 0001 + rama git. Las líneas se recortan al
// ancho del panel y la altura la fija detailsHeight en rightLines.
func (m Model) detailsLines(w int) []string {
	var lines []string

	p := m.selected()
	if p == nil {
		if g := m.selectedGroup(); g != "" { // R24: resumen del grupo
			return m.groupDetailsLines(g, w)
		}
		return []string{styleDim.Render("No project selected")}
	}
	sv := m.services[p.Path]
	lines = append(lines, trunc(p.Name, w)+"  "+statusBadge(*p, sv))

	valueW := max(8, w-11)
	row := func(label, value string) {
		lines = append(lines, styleLabel.Render(pad(label, 10))+truncTail(value, valueW))
	}
	row("path:", p.Path)
	row("language:", p.Language)
	if b := m.branches[p.Path]; b != "" { // R21: rama git
		row("branch:", b)
	}
	if p.Manifest != nil && p.Manifest.Group != "" {
		row("group:", p.Manifest.Group)
	}
	if p.Configured {
		row("command:", p.Manifest.Command)
		if p.Manifest.Port > 0 {
			row("port:", fmt.Sprintf("%d", p.Manifest.Port))
		}
		if p.Manifest.ProcessPattern != "" {
			row("pattern:", p.Manifest.ProcessPattern)
		}
		if sv.Meta.Pid > 0 {
			row("pid/pgid:", fmt.Sprintf("%d / %d", sv.Meta.Pid, sv.Meta.Pgid))
		}
		if sv.Meta.StartedAt != "" {
			row("started:", sv.Meta.StartedAt)
		}
		row("logs:", m.store.ServiceDir(p.Path))
	} else {
		lines = append(lines, styleWarn.Render("No manifest — create a .vroom.toml"))
		lines = append(lines, styleDim.Render(trunc(exampleManifest(p.Name), w)))
	}

	// Recortar al alto del panel; cada línea al ancho visible.
	if len(lines) > detailsHeight {
		lines = lines[:detailsHeight]
	}
	for i := range lines {
		lines[i] = truncANSI(lines[i], w)
	}
	return lines
}

// groupDetailsLines muestra el resumen del grupo seleccionado (R24):
// conteo running/total y los miembros con su punto de estado.
func (m Model) groupDetailsLines(g string, w int) []string {
	r, n := m.groupStats(g)
	lines := []string{trunc(fmt.Sprintf("%s (%d/%d)", g, r, n), w)}
	lines = append(lines,
		styleLabel.Render(pad("services:", 10))+fmt.Sprintf("%d", n),
		styleLabel.Render(pad("running:", 10))+fmt.Sprintf("%d", r),
	)
	for _, p := range m.groupMembers(g) {
		lines = append(lines, treeDot(p, m.services[p.Path])+" "+trunc(p.Name, w-3))
	}
	if len(lines) > detailsHeight {
		lines = lines[:detailsHeight]
	}
	for i := range lines {
		lines[i] = truncANSI(lines[i], w)
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
