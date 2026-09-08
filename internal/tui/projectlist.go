package tui

import (
	"fmt"
	"strings"

	"vroom/internal/group"
	"vroom/internal/scanner"
)

type treeItemKind int

const (
	itemPrimary   treeItemKind = iota // header de primario
	itemSecondary                     // header de secundario (dentro de un primario)
	itemProject                       // proyecto
)

// treeItem es una fila navegable del árbol: header primario, header
// secundario o proyecto. primary/secondary viajan en todas las filas
// para conocer el contenedor de un proyecto al plegarlo (S38.4).
type treeItem struct {
	kind      treeItemKind
	primary   string          // primario del bloque ("" solo en proyecto inline)
	secondary string          // secundario del bloque ("" = directo bajo el primario)
	project   scanner.Project // válido en itemProject
}

// buildTree compone las filas del árbol (0006 R38): header primario antes
// de su bloque; dentro, header de secundario antes de sus miembros. El
// plegado omite dos niveles: un primario colapsado oculta sus proyectos Y
// sus headers secundarios; un secundario colapsado solo sus proyectos.
// El header conserva su índice al colapsar/expander (los miembros están
// después).
func (m Model) buildTree() []treeItem {
	items := make([]treeItem, 0, len(m.entries)+1)
	skipPrimary := ""   // primario colapsado cuyos miembros y headers se omiten
	skipSecondary := "" // clave compuesta "primario/secundario" colapsada
	for i, e := range m.entries {
		if group.IsPrimaryHeader(m.entries, i) {
			items = append(items, treeItem{kind: itemPrimary, primary: e.Primary})
			skipPrimary, skipSecondary = "", "" // nuevo bloque: reset
			if m.collapsed[e.Primary] {
				skipPrimary = e.Primary
				continue
			}
		}
		if skipPrimary != "" && e.Primary == skipPrimary {
			continue
		}
		key := m.secondaryKey(e.Primary, e.Secondary)
		if e.Secondary != "" && group.IsSecondaryHeader(m.entries, i) {
			items = append(items, treeItem{kind: itemSecondary, primary: e.Primary, secondary: e.Secondary})
			if m.collapsed[key] {
				skipSecondary = key
				continue
			}
			skipSecondary = ""
		}
		if skipSecondary != "" && key == skipSecondary {
			continue
		}
		items = append(items, treeItem{kind: itemProject, primary: e.Primary, secondary: e.Secondary, project: e.Project})
	}
	return items
}

// treeLines genera las líneas de la columna de árbol; la posición de
// línea del cursor es su propio índice (una fila por ítem). El header
// secundario se dibuja indentado 2 espacios extra (S38.1).
func (m Model) treeLines() ([]string, int) {
	lines := make([]string, 0, len(m.tree))
	for i, it := range m.tree {
		cursor := "  "
		if i == m.cursor {
			cursor = "▶ "
		}
		switch it.kind {
		case itemPrimary:
			lines = append(lines, cursor+m.primaryRow(it.primary))
		case itemSecondary:
			lines = append(lines, cursor+"  "+m.secondaryRow(it.primary, it.secondary))
		default:
			lines = append(lines, cursor+m.treeRow(it.project))
		}
	}
	return lines, m.cursor
}

// primaryRow dibuja el header del primario (columna 0): ▾ expandido,
// ▸ colapsado con conteo running/total (R24); el total incluye todos
// sus secundarios (S38.5).
func (m Model) primaryRow(primary string) string {
	r, n := m.nodeStats(primary, "")
	return m.groupHeaderRow(primary, primary, r, n)
}

// secondaryRow dibuja el header del secundario; su conteo es el de sus
// propios miembros (la indentación la aplica treeLines).
func (m Model) secondaryRow(primary, secondary string) string {
	r, n := m.nodeStats(primary, secondary)
	return m.groupHeaderRow(m.secondaryKey(primary, secondary), secondary, r, n)
}

// groupHeaderRow compone un header de grupo: ▾ nombre expandido, ▸
// nombre (r/t) colapsado.
func (m Model) groupHeaderRow(key, label string, running, total int) string {
	glyph := "▾"
	text := label
	if m.collapsed[key] {
		glyph = "▸"
		text = fmt.Sprintf("%s (%d/%d)", label, running, total)
	}
	return styleGroupHeader.Render(glyph + " " + trunc(text, treeWidth-4))
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

// filterMatch reporta si el proyecto matchea la query del filtro
// (0007 R41): substring case-insensitive contra el nombre y los nombres
// de grupo (primary/secondary); filtrar un grupo trae a todos sus
// miembros aunque el texto no aparezca en ningún nombre de proyecto.
func filterMatch(p scanner.Project, q string) bool {
	q = strings.ToLower(q)
	if strings.Contains(strings.ToLower(p.Name), q) {
		return true
	}
	if strings.Contains(strings.ToLower(group.PrimaryOf(p)), q) {
		return true
	}
	return strings.Contains(strings.ToLower(group.SecondaryOf(p)), q)
}

// exampleManifest genera un manifiesto de ejemplo para proyectos sin
// configurar.
func exampleManifest(name string) string {
	return fmt.Sprintf(`name = %q
command_start = "go run main.go"
port = 0`, name)
}
