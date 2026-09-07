//go:build unix

package process

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const procRoot = "/proc"

// ListThreads lista los hilos del PID leyendo /proc/<pid>/task.
// Si el proceso ya no existe devuelve error (la UI muestra placeholder).
func ListThreads(pid int) ([]ThreadInfo, error) {
	return listThreadsAt(procRoot, pid)
}

// listThreadsAt permite inyectar la raíz de /proc para los tests.
func listThreadsAt(root string, pid int) ([]ThreadInfo, error) {
	taskDir := filepath.Join(root, strconv.Itoa(pid), "task")
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		return nil, fmt.Errorf("thread sampling: %w", err)
	}

	out := make([]ThreadInfo, 0, len(entries))
	for _, e := range entries {
		tid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // no es un tid numérico
		}
		ti := ThreadInfo{TID: tid}
		if comm, err := os.ReadFile(filepath.Join(taskDir, e.Name(), "comm")); err == nil {
			ti.Name = strings.TrimRight(string(comm), "\n")
		}
		if ti.Name == "" {
			if name, err := threadNameFromStat(filepath.Join(taskDir, e.Name(), "stat")); err == nil {
				ti.Name = name
			}
		}
		if state, ticks, err := parseThreadStat(filepath.Join(taskDir, e.Name(), "stat")); err == nil {
			ti.State = state
			ti.Ticks = ticks
		} else {
			continue // sin stat legible el hilo está muriendo; omitir
		}
		out = append(out, ti)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TID < out[j].TID })
	return out, nil
}

// parseThreadStat extrae el estado (campo 3) y utime+stime (campos 14+15)
// de /proc/<pid>/task/<tid>/stat. El campo comm (2) puede contener espacios
// y paréntesis, por lo que se recorta desde el último ')'.
func parseThreadStat(path string) (state string, ticks uint64, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	s := string(raw)
	rparen := strings.LastIndexByte(s, ')')
	if rparen < 0 || rparen+2 > len(s) {
		return "", 0, fmt.Errorf("stat malformado: %s", path)
	}
	rest := strings.Fields(s[rparen+2:]) // rest[0] = campo 3 (state)
	if len(rest) < 13 {
		return "", 0, fmt.Errorf("stat incompleto: %s", path)
	}
	utime, err := strconv.ParseUint(rest[11], 10, 64) // campo 14
	if err != nil {
		return "", 0, err
	}
	stime, err := strconv.ParseUint(rest[12], 10, 64) // campo 15
	if err != nil {
		return "", 0, err
	}
	return rest[0], utime + stime, nil
}

// threadNameFromStat recupera el nombre del hilo desde stat (fallback si
// comm no está disponible).
func threadNameFromStat(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := string(raw)
	lparen := strings.IndexByte(s, '(')
	rparen := strings.LastIndexByte(s, ')')
	if lparen < 0 || rparen <= lparen {
		return "", fmt.Errorf("stat sin comm: %s", path)
	}
	return s[lparen+1 : rparen], nil
}
