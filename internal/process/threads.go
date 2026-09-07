// Package process — muestreo de hilos a nivel OS (spec 0002 R20).
//
// ListThreads lee /proc/<pid>/task/ directamente: funciona para cualquier
// lenguaje (Java expone nombres de hilo, Go goroutines del runtime) sin
// debugger. La implementación Linux vive en threads_unix.go con build tag;
// threads_windows.go documenta el placeholder para v2.
package process

// ThreadInfo es una muestra puntual de un hilo del proceso.
type ThreadInfo struct {
	TID   int
	Name  string // comm del hilo (truncado a 15 chars por el kernel)
	State string // R/S/D/Z/T/... (campo 3 de stat)
	Ticks uint64 // utime+stime en clock ticks (acumulado desde el arranque)
}

// clockTicksPerSec es USER_HZ en Linux (estándar, ver getconf CLK_TCK).
const clockTicksPerSec = 100

// CPUPercent convierte un delta de ticks y el tiempo transcurrido entre
// muestras en porcentaje de CPU (0-100+, spec S20.2).
func CPUPercent(deltaTicks uint64, elapsedSeconds float64) float64 {
	if elapsedSeconds <= 0 {
		return 0
	}
	return float64(deltaTicks) / (elapsedSeconds * clockTicksPerSec) * 100
}
