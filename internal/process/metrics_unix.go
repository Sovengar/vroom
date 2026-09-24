//go:build unix

package process

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ReadMetrics lee métricas del proceso desde /proc: memoria residente
// (VmRSS), nº de hilos, descriptores abiertos y ticks de CPU acumulados.
func ReadMetrics(pid int) (Metrics, error) {
	return readMetricsAt(procRoot, pid)
}

// readMetricsAt permite inyectar la raíz de /proc para los tests.
func readMetricsAt(root string, pid int) (Metrics, error) {
	dir := filepath.Join(root, strconv.Itoa(pid))
	var m Metrics
	if _, ticks, err := parseThreadStat(filepath.Join(dir, "stat")); err == nil {
		m.Ticks = ticks
	} else {
		return m, fmt.Errorf("process metrics: %w", err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "status")); err == nil {
		m.RSSKB = parseVmRSS(string(raw))
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "fd")); err == nil {
		m.FDs = len(entries)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "task")); err == nil {
		m.Threads = len(entries)
	}
	return m, nil
}

// parseVmRSS extrae el campo VmRSS (en kB) de /proc/<pid>/status.
func parseVmRSS(status string) int64 {
	for _, line := range strings.Split(status, "\n") {
		v, ok := strings.CutPrefix(line, "VmRSS:")
		if !ok {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			return 0
		}
		n, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// ReadEnviron devuelve el entorno del proceso (variables "KEY=value").
// Solo unix: en Windows no hay equivalente directo.
func ReadEnviron(pid int) ([]string, error) {
	return readEnvironAt(procRoot, pid)
}

func readEnvironAt(root string, pid int) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "environ"))
	if err != nil {
		return nil, fmt.Errorf("process environ: %w", err)
	}
	parts := strings.Split(string(raw), "\x00")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out, nil
}
