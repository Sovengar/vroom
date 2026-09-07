package tui

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
