// Package group agrupa proyectos por el campo group del manifiesto (spec R10).
package group

import "vroom/internal/scanner"

// Entry es una fila renderizable de la lista de proyectos.
// Group no vacío indica que el proyecto pertenece a un grupo: la vista
// dibuja un separador con el nombre del grupo antes del primer miembro.
type Entry struct {
	Group   string // "" = sin grupo
	Project scanner.Project
}

// Arrange ordena los proyectos preservando el orden de aparición y
// agrupando juntos los que comparten el mismo group:
//
//   - Proyectos sin group (""), sin agrupación.
//   - El bloque de un grupo se emite completo en la posición de su
//     primer miembro (orden de aparición); los miembros posteriores se
//     unen al bloque en lugar de abrir uno nuevo.
//   - Un grupo con un único miembro también muestra su header (S10.2).
func Arrange(projects []scanner.Project) []Entry {
	// Miembros por grupo en orden de aparición; groupOrder fija el
	// orden de primera aparición de cada grupo.
	members := make(map[string][]scanner.Project)
	for _, p := range projects {
		if g := groupOf(p); g != "" {
			members[g] = append(members[g], p)
		}
	}

	out := make([]Entry, 0, len(projects))
	emitted := make(map[string]bool, len(members))
	for _, p := range projects {
		g := groupOf(p)
		switch {
		case g == "":
			out = append(out, Entry{Project: p})
		case emitted[g]:
			// ya emitido junto a su primer miembro
		default:
			emitted[g] = true
			for _, m := range members[g] {
				out = append(out, Entry{Group: g, Project: m})
			}
		}
	}
	return out
}

// groupOf devuelve el group del manifiesto ("" si no hay manifiesto).
func groupOf(p scanner.Project) string {
	if p.Manifest == nil {
		return ""
	}
	return p.Manifest.Group
}

// IsGroupHeader reporta si la entrada en i debe renderizarse con el
// separador del grupo (primer miembro del bloque).
func IsGroupHeader(entries []Entry, i int) bool {
	return entries[i].Group != "" && (i == 0 || entries[i-1].Group != entries[i].Group)
}
