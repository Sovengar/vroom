// Package group agrupa proyectos jerárquicamente por primary_group/
// secondary_group del manifiesto.
package group

import "vroom/internal/scanner"

// Entry es una fila renderizable de la lista de proyectos. Primary no
// vacío indica pertenencia a un grupo primario; Secondary, a un bloque
// interno dentro del primario (solo tiene sentido con Primary != "").
// El bloque primario se emite completo en la posición de su primer
// miembro; dentro de él, cada secundario forma su propio bloque contiguo
// en la posición de su primer miembro.
type Entry struct {
	Primary   string // "" = sin primario (inline)
	Secondary string // "" = directo bajo el primario
	Project   scanner.Project
}

// Arrange ordena los proyectos preservando el orden de aparición y
// agrupando jerárquicamente:
//
//   - Proyectos sin primary_group ("") quedan inline, sin agrupar.
//   - El bloque de un primario se emite completo en la posición de su
//     primer miembro (orden de aparición); los miembros posteriores se
//     unen al bloque en lugar de abrir uno nuevo.
//   - Dentro de un primario, el bloque de cada secundario se emite
//     contiguo en la posición de su primer miembro.
//   - Los miembros con primario pero sin secundario conservan su
//     posición dentro del primario, sin header propio (sin pseudo-header
//     "(general)").
//   - Un secondary_group sin primary_group se ignora.
func Arrange(projects []scanner.Project) []Entry {
	// Miembros por primario en orden de aparición; el orden de
	// primera aparición de cada primario sale del walk de abajo.
	primaries := make(map[string][]scanner.Project)
	for _, p := range projects {
		if pr := PrimaryOf(p); pr != "" {
			primaries[pr] = append(primaries[pr], p)
		}
	}

	out := make([]Entry, 0, len(projects))
	emitted := make(map[string]bool, len(primaries))
	for _, p := range projects {
		pr := PrimaryOf(p)
		switch {
		case pr == "":
			out = append(out, Entry{Project: p})
		case emitted[pr]:
			// ya emitido junto a su primer miembro
		default:
			emitted[pr] = true
			out = append(out, arrangeSecondary(primaries[pr])...)
		}
	}
	return out
}

// arrangeSecondary aplica el mismo patrón de bloque contiguo al interior
// de un primario, con Secondary como clave.
func arrangeSecondary(members []scanner.Project) []Entry {
	prim := PrimaryOf(members[0])
	secs := make(map[string][]scanner.Project)
	for _, p := range members {
		if s := SecondaryOf(p); s != "" {
			secs[s] = append(secs[s], p)
		}
	}

	out := make([]Entry, 0, len(members))
	emitted := make(map[string]bool, len(secs))
	for _, p := range members {
		s := SecondaryOf(p)
		switch {
		case s == "":
			out = append(out, Entry{Primary: prim, Project: p})
		case emitted[s]:
			// ya emitido junto a su primer miembro
		default:
			emitted[s] = true
			for _, m := range secs[s] {
				out = append(out, Entry{Primary: prim, Secondary: s, Project: m})
			}
		}
	}
	return out
}

// PrimaryOf devuelve el primary_group del manifiesto ("" si no hay
// manifiesto).
func PrimaryOf(p scanner.Project) string {
	if p.Manifest == nil {
		return ""
	}
	return p.Manifest.PrimaryGroup
}

// SecondaryOf devuelve el secondary_group del manifiesto ("" si no hay
// manifiesto).
func SecondaryOf(p scanner.Project) string {
	if p.Manifest == nil {
		return ""
	}
	return p.Manifest.SecondaryGroup
}

// IsPrimaryHeader reporta si la entrada en i abre el bloque de su
// primario (primer miembro tras el orden de bloques contiguos).
func IsPrimaryHeader(entries []Entry, i int) bool {
	return entries[i].Primary != "" && (i == 0 || entries[i-1].Primary != entries[i].Primary)
}

// IsSecondaryHeader reporta si la entrada en i abre un bloque de
// secundario: Secondary no vacío y (abre primario o cambia el secundario
// respecto de i-1).
func IsSecondaryHeader(entries []Entry, i int) bool {
	return entries[i].Secondary != "" && (i == 0 ||
		entries[i-1].Primary != entries[i].Primary ||
		entries[i-1].Secondary != entries[i].Secondary)
}
