package process

// Metrics son las métricas de recursos de un proceso: memoria residente,
// hilos, descriptores abiertos y ticks de CPU acumulados (utime+stime) para
// calcular el porcentaje por delta entre muestras.
type Metrics struct {
	RSSKB   int64
	Threads int
	FDs     int
	Ticks   uint64
}
