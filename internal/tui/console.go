package tui

import "strings"

type consoleState struct {
	off    [2]int64 // 0=stdout, 1=stderr
	stdout string
	stderr string
	merged string // approximate interleave: each read appends the stdout delta before the stderr delta
}

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

// A lone \r makes the renderer repaint from column 0 (Maven progress, spinners) and spill into the tree panel, and truncANSI measures plain text so it cannot stop it.
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

// Must run after sanitizeConsole so ANSI escapes never mix with \r and truncANSI keeps measuring correctly.
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

func isStackTraceLine(l string) bool {
	return strings.HasPrefix(l, "\tat ") ||
		strings.HasPrefix(l, "\t... ") ||
		strings.HasPrefix(l, "Caused by: ")
}

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
