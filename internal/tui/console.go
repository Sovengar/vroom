package tui

import "strings"

// consoleState mantiene el buffer de logs de un servicio mientras corre
// la TUI: offsets de lectura por stream y buffers con cap (spec 0002
// R19). Navegar entre servicios conserva el buffer (S19.2).
type consoleState struct {
	off    [2]int64 // 0=stdout, 1=stderr
	stdout string
	stderr string
	merged string // intercalado aproximado: stdout delta antes que stderr (R23)
}

// view devuelve el buffer correspondiente al modo de stream activo.
func (cs *consoleState) view(mode streamMode) string {
	switch mode {
	case streamStdout:
		return cs.stdout
	case streamStderr:
		return cs.stderr
	default:
		return cs.merged
	}
}

// sanitizeConsole prepara el buffer para el viewport (S19.8): normaliza
// CRLF a LF y emula la semántica de terminal de los CR sueltos. Sin esto,
// el renderer pinta desde la columna 0 al ver un \r (progreso de Maven,
// spinners) e invade el panel del árbol; además truncANSI mide con
// semántica de texto plano, así que la truncación no lo impide.
func sanitizeConsole(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = emulateCarriageReturns(l)
	}
	return strings.Join(lines, "\n")
}

// emulateCarriageReturns emula un terminal dentro de una línea: cada
// segmento tras \r reescribe desde la columna 0, sobrescribiendo lo
// previo (y extendiéndolo si es más largo). Un \r colgante al final no
// borra nada: en un terminal nada lo ha sobrescrito aún.
func emulateCarriageReturns(line string) string {
	segs := strings.Split(line, "\r")
	if len(segs) == 1 {
		return line
	}
	out := []rune(segs[0])
	for _, seg := range segs[1:] {
		i := 0
		for _, r := range seg {
			if i < len(out) {
				out[i] = r
			} else {
				out = append(out, r)
			}
			i++
		}
	}
	return string(out)
}
