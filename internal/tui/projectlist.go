package tui

import (
	"fmt"

	"vroom/internal/group"
	"vroom/internal/scanner"
)

type treeItemKind int

const (
	itemGroup treeItemKind = iota
	itemProject
)

// treeItem es una fila navegable del árbol: header de grupo o proyecto.
type treeItem struct {
	kind    treeItemKind
	group   string          // válido en itemGroup
	project scanner.Project // válido en itemProject
}

// buildTree compone las filas del árbol (R24): header de grupo antes de
// sus miembros; los miembros de un grupo colapsado se omiten. El header
// conserva su índice al colapsar/expander (los miembros están después).
func (m Model) buildTree() []treeItem {
	items := make([]treeItem, 0, len(m.entries)+1)
	skip := "" // grupo colapsado cuyos miembros se omiten
	for i, e := range m.entries {
		if group.IsGroupHeader(m.entries, i) {
			items = append(items, treeItem{kind: itemGroup, group: e.Group})
			skip = "" // nuevo bloque: reset
			if m.collapsed[e.Group] {
				skip = e.Group
				continue
			}
		}
		if skip != "" && e.Group == skip {
			continue
		}
		items = append(items, treeItem{kind: itemProject, project: e.Project})
	}
	return items
}

// treeLines genera las líneas de la columna de árbol; la posición de
// línea del cursor es su propio índice (una fila por ítem).
func (m Model) treeLines() ([]string, int) {
	lines := make([]string, 0, len(m.tree))
	for i, it := range m.tree {
		cursor := "  "
		if i == m.cursor {
			cursor = "▶ "
		}
		if it.kind == itemGroup {
			lines = append(lines, cursor+m.groupRow(it.group))
		} else {
			lines = append(lines, cursor+m.treeRow(it.project))
		}
	}
	return lines, m.cursor
}

// groupRow dibuja el header del grupo: ▾ expandido, ▸ colapsado con el
// conteo running/total (R24: "vsocial-backend (3/8)").
func (m Model) groupRow(g string) string {
	glyph := "▾"
	label := g
	if m.collapsed[g] {
		r, n := m.groupStats(g)
		glyph = "▸"
		label = fmt.Sprintf("%s (%d/%d)", g, r, n)
	}
	return styleGroupHeader.Render(glyph + " " + trunc(label, treeWidth-4))
}

// treeRow dibuja la fila del proyecto: punto de estado + nombre. El
// texto del estado vive en el panel de detalles; en el árbol la bolita
// basta (y los sin manifiesto llevan icono de "roto").
func (m Model) treeRow(p scanner.Project) string {
	return treeDot(p, m.services[p.Path]) + " " + trunc(p.Name, treeWidth-4)
}

// treeDot es el glifo de estado: ● running, ○ en tránsito, · stopped,
// ? unknown y ⚠ para sin manifiesto / manifiesto inválido.
func treeDot(p scanner.Project, sv *ServiceState) string {
	if !p.Configured || p.ManifestErr != "" {
		return styleWarn.Render("⚠")
	}
	if sv == nil {
		return styleStopped.Render("·")
	}
	switch sv.Status {
	case statusRunning:
		return styleRunning.Render("●")
	case statusStarting:
		return styleStarting.Render("○")
	case statusStopping:
		return styleStopping.Render("○")
	case statusUnknown:
		return styleUnknown.Render("?")
	default:
		return styleStopped.Render("·")
	}
}

// exampleManifest genera un manifiesto de ejemplo para proyectos sin
// configurar.
func exampleManifest(name string) string {
	return fmt.Sprintf(`name = %q
command_start = "go run main.go"
port = 0`, name)
}
