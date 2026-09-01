package tui

import (
	"strings"

	"svc/internal/group"
	"svc/internal/scanner"
)

// renderList dibuja la lista principal (spec R11): nombre, badge de
// lenguaje, grupo (si aplica) y estado visual por proyecto. En anchos
// reducidos se degrada: sin columna de lenguaje y ayuda compacta.
func (m Model) renderList() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render(trunc("svc — projects in "+m.root, m.width)) + "\n\n")

	if len(m.entries) == 0 {
		msg := "No projects detected (scanning for language markers and .svc.toml up to 2 levels)"
		b.WriteString(styleDim.Render(trunc(msg, m.width)) + "\n")
	}

	for i, e := range m.entries {
		if group.IsGroupHeader(m.entries, i) { // S10.1/S10.2
			b.WriteString(styleGroupHeader.Render(trunc("── "+e.Group, m.width)) + "\n")
		}
		sv := m.services[e.Project.Path]
		cursor := "  "
		if i == m.cursor {
			cursor = "▶ "
		}
		b.WriteString(cursor + m.renderRow(e.Project, sv) + "\n")
	}

	b.WriteString("\n" + styleHelp.Render(helpLine(viewList, m.width)) + "\n")
	if m.message != "" {
		b.WriteString(styleMsg.Render(trunc("ℹ "+m.message, m.width)) + "\n")
	}
	return b.String()
}

// renderRow compone la fila de un proyecto según el ancho disponible.
func (m Model) renderRow(p scanner.Project, sv *ServiceState) string {
	badge := statusBadge(p, sv)
	// Ancho visible del badge ~18; cursor ya está fuera.
	if m.width > 0 && m.width < 60 {
		// Estrecho: nombre + estado, sin columna de lenguaje.
		return trunc(p.Name, max(8, m.width-20)) + "  " + badge
	}
	return pad(trunc(p.Name, 24), 25) + pad(p.Language, 11) + badge
}
