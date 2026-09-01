package tui

import "strings"

// renderLogs dibuja la vista de logs con viewport (spec R16).
// El stream mostrado es conmutable (Tab), no fusionado.
func (m Model) renderLogs() string {
	var b strings.Builder

	p := m.selected()
	name := ""
	stream := "stdout.log"
	if m.logStderr {
		stream = "stderr.log"
	}
	if p != nil {
		name = p.Name
	}

	b.WriteString(styleTitle.Render("logs: "+name) + "  " + styleDim.Render("["+stream+"]") + "\n\n")
	b.WriteString(m.logViewport.View() + "\n")
	b.WriteString(styleHelp.Render(helpLine(viewLogs, m.width)) + "\n")
	if m.message != "" {
		b.WriteString(styleMsg.Render("ℹ "+m.message) + "\n")
	}
	return b.String()
}
