//go:build windows

package process

import "errors"

// ListThreads es un placeholder documentado para v2 (igual que el resto
// del paquete process): el muestreo de hilos en Windows requeriría
// NtQuerySystemInformation o wmic, fuera del alcance Linux-first de v1.
func ListThreads(pid int) ([]ThreadInfo, error) {
	return nil, errors.New("thread sampling: not supported on windows yet")
}
