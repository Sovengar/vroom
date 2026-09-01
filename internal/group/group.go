// Package group agrupa proyectos por el campo group del manifiesto (spec R10).
package group

import "svc/internal/scanner"

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
//   - El primer proyecto de un grupo abre el bloque; los siguientes se
//     insertan inmediatamente después del último miembro del grupo.
//   - Un grupo con un único miembro también muestra su header (S10.2).
func Arrange(projects []scanner.Project) []Entry {
	out := make([]Entry, 0, len(projects))
	lastIndexOf := make(map[string]int) // group → índice del último miembro en out

	for _, p := range projects {
		g := ""
		if p.Manifest != nil {
			g = p.Manifest.Group
		}
		if g == "" {
			out = append(out, Entry{Project: p})
			continue
		}
		if idx, seen := lastIndexOf[g]; seen {
			out = insertAfter(out, idx, Entry{Group: g, Project: p})
			lastIndexOf[g] = idx + 1
		} else {
			out = append(out, Entry{Group: g, Project: p})
			lastIndexOf[g] = len(out) - 1
		}
	}
	return out
}

// IsGroupHeader reporta si la entrada en i debe renderizarse con el
// separador del grupo (primer miembro del bloque).
func IsGroupHeader(entries []Entry, i int) bool {
	return entries[i].Group != "" && (i == 0 || entries[i-1].Group != entries[i].Group)
}

func insertAfter(s []Entry, i int, v Entry) []Entry {
	s = append(s, Entry{})
	copy(s[i+2:], s[i+1:])
	s[i+1] = v
	return s
}
