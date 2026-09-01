package tui

import (
	"fmt"
	"strings"
)

// renderService dibuja el detalle del servicio seleccionado.
// Los valores largos (rutas) se recortan conservando el final para
// adaptarse al ancho del terminal.
func (m Model) renderService() string {
	var b strings.Builder

	p := m.selected()
	if p == nil {
		b.WriteString(styleDim.Render("No project selected") + "\n")
		b.WriteString("\n" + styleHelp.Render(helpLine(viewService, m.width)) + "\n")
		return b.String()
	}
	sv := m.services[p.Path]

	b.WriteString(styleTitle.Render(trunc(p.Name, m.width)) + "  " + statusBadge(*p, sv) + "\n\n")

	valueW := max(12, m.width-17)
	row := func(label, value string) {
		b.WriteString(styleLabel.Render(pad(label, 16)) + truncTail(value, valueW) + "\n")
	}
	row("path:", p.Path)
	row("language:", p.Language)
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
		b.WriteString("\n" + styleWarn.Render("No manifest — create a .svc.toml to enable") + "\n")
		if p.ManifestErr != "" {
			b.WriteString(styleWarn.Render(trunc("parse error: "+p.ManifestErr, m.width)) + "\n")
		}
		b.WriteString("\n" + styleDim.Render(exampleManifest(p.Name)) + "\n")
	}

	b.WriteString("\n" + styleHelp.Render(helpLine(viewService, m.width)) + "\n")
	if m.message != "" {
		b.WriteString(styleMsg.Render(trunc("ℹ "+m.message, m.width)) + "\n")
	}
	return b.String()
}

func exampleManifest(name string) string {
	return fmt.Sprintf(`# %s/.svc.toml
name = "%s"
group = ""
command = "go run main.go"
port = 0
process_pattern = ""`, name, name)
}
