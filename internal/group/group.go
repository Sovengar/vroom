// Package group arranges projects into primary/secondary blocks while preserving appearance order; ungrouped projects stay inline.
package group

import "vroom/internal/scanner"

type Entry struct {
	Primary   string
	Secondary string
	Project   scanner.Project
}

// Each block is contiguous and anchored at its first member in appearance order; a secondary_group without a primary_group is ignored.
func Arrange(projects []scanner.Project) []Entry {
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
		default:
			emitted[pr] = true
			out = append(out, arrangeSecondary(primaries[pr])...)
		}
	}
	return out
}

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
		default:
			emitted[s] = true
			for _, m := range secs[s] {
				out = append(out, Entry{Primary: prim, Secondary: s, Project: m})
			}
		}
	}
	return out
}

func PrimaryOf(p scanner.Project) string {
	if p.Manifest == nil {
		return ""
	}
	return p.Manifest.PrimaryGroup
}

func SecondaryOf(p scanner.Project) string {
	if p.Manifest == nil {
		return ""
	}
	return p.Manifest.SecondaryGroup
}

func IsPrimaryHeader(entries []Entry, i int) bool {
	return entries[i].Primary != "" && (i == 0 || entries[i-1].Primary != entries[i].Primary)
}

func IsSecondaryHeader(entries []Entry, i int) bool {
	return entries[i].Secondary != "" && (i == 0 ||
		entries[i-1].Primary != entries[i].Primary ||
		entries[i-1].Secondary != entries[i].Secondary)
}
