package tui

import (
	"fmt"
	"sort"
	"time"
)

// threadRow es una fila de la tabla de hilos (spec 0002 R20).
type threadRow struct {
	Name  string
	TID   int
	State string
	CPU   float64
}

// sortRows ordena por CPU% descendente con desempate por TID (S20.3).
func sortRows(rows []threadRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CPU != rows[j].CPU {
			return rows[i].CPU > rows[j].CPU
		}
		return rows[i].TID < rows[j].TID
	})
}

// threadSample es la muestra previa para calcular el delta de CPU.
type threadSample struct {
	at    time.Time
	ticks map[int]uint64
}

// threadsLines renderiza la tabla de hilos del servicio seleccionado
// (S20.1-S20.4): nombre, TID, estado y CPU% ordenados por CPU.
func (m Model) threadsLines(w, h int) []string {
	p := m.selected()
	if p == nil {
		if m.selectedGroup() != "" { // R24: grupo sin proceso propio
			return []string{styleDim.Render(trunc("group selected — pick a service to inspect threads", w))}
		}
		return nil
	}
	if !p.Configured {
		return []string{styleDim.Render(trunc("No manifest — create a .vroom.toml to enable", w))}
	}
	if !m.isRunning(p.Path) { // S20.4
		return []string{styleDim.Render("service not running")}
	}
	rows := m.threads[p.Path]
	if rows == nil { // primera muestra en curso o proceso recién muerto
		return []string{styleDim.Render("sampling threads…")}
	}

	nameW := w - 20 // TID(7) + STATE(4) + CPU%(6) + separadores(3)
	if nameW < 10 {
		nameW = 10
	}
	header := fmt.Sprintf("%-*s %7s %-4s %6s", nameW, "NAME", "TID", "STATE", "CPU%")
	out := []string{styleLabel.Render(header)}

	maxRows := h - 3 // header + margen
	if maxRows < 1 {
		maxRows = 1
	}
	shown := rows
	if len(shown) > maxRows-1 {
		shown = shown[:maxRows-1]
	}
	for _, r := range shown {
		out = append(out, fmt.Sprintf("%-*s %7d %-4s %5.1f%%",
			nameW, trunc(r.Name, nameW), r.TID, r.State, r.CPU))
	}
	if remaining := len(rows) - len(shown); remaining > 0 {
		out = append(out, styleDim.Render(fmt.Sprintf("+%d more threads", remaining)))
	}
	return out
}
