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

// highlightConsole aplica resaltado estilo IntelliJ al buffer saneado:
// línea con ERROR→rojo, WARN→amarillo y ruido (debug/trace, stack traces
// de la JVM, prefijo [INFO] de Maven)→gris tenue; el resto queda default.
// Va después de sanitizeConsole para que los escapes ANSI nunca convivan
// con \r y truncANSI siga midiendo bien. Detección por subcadenas en
// orden de prioridad: barata (~ms para el cap de 192KB) y agnóstica del
// lenguaje (Spring Boot, Maven, Go, Python...).
func highlightConsole(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if h := highlightLine(l); h != l {
			lines[i] = h
		}
	}
	return strings.Join(lines, "\n")
}

// highlightLine decide el estilo de una línea: ERROR gana sobre WARN,
// que gana sobre DEBUG/TRACE, y después el ruido de stack/Maven.
func highlightLine(l string) string {
	if l == "" {
		return l
	}
	switch {
	case strings.Contains(l, "ERROR"):
		return styleLineError.Render(l)
	case strings.Contains(l, "WARN"):
		return styleLineWarn.Render(l)
	case strings.Contains(l, "DEBUG"), strings.Contains(l, "TRACE"):
		return styleDim.Render(l)
	case isStackTraceLine(l), strings.HasPrefix(l, "[INFO]"):
		return styleDim.Render(l)
	}
	return l
}

// isStackTraceLine detecta los frames del stack trace de la JVM y su
// cabecera "Caused by:".
func isStackTraceLine(l string) bool {
	return strings.HasPrefix(l, "\tat ") ||
		strings.HasPrefix(l, "\t... ") ||
		strings.HasPrefix(l, "Caused by: ")
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
