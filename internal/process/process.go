// Package process gestiona servicios daemonizados de forma portable
// (Linux-first, Windows-ready).
//
// La interfaz Manager es la frontera cross-platform: la implementación
// concreta vive en ficheros con build tags:
//
//	daemon_unix.go    //go:build unix    → setsid, SIGTERM/SIGKILL por PGID
//	daemon_windows.go //go:build windows → placeholder documentado para v2
package process

import "time"

// Status es el estado de un servicio evaluado.
type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	StatusUnknown Status = "unknown"
)

// DefaultStopTimeout es el timeout de SIGTERM antes de SIGKILL.
const DefaultStopTimeout = 5 * time.Second

// StartSpec describe el arranque de un servicio daemonizado.
type StartSpec struct {
	Command    string // comando a ejecutar via sh -c
	WorkDir    string // directorio de trabajo del proceso
	StdoutPath string // fichero donde capturar stdout
	StderrPath string // fichero donde capturar stderr
}

// StartResult contiene las credenciales del proceso arrancado.
type StartResult struct {
	Pid            int
	Pgid           int // con setsid, Pgid == Pid
	CreationTimeMs int64
}

// StopSpec describe la parada de un process group.
type StopSpec struct {
	Pgid    int
	Port    int           // 0 = no verificar puerto tras stop
	Timeout time.Duration
}

// EvalSpec contiene las credenciales registradas para evaluar el estado.
type EvalSpec struct {
	Pid            int
	CreationTimeMs int64
	Port           int    // 0 = no verificar
	ProcessPattern string // "" = no verificar
}

// Manager es la abstracción de gestión de procesos portable a Windows.
type Manager interface {
	// Start daemoniza el comando: nuevo session leader, stdout/stderr a
	// ficheros, y sobrevive al cierre del padre.
	Start(spec StartSpec) (StartResult, error)

	// Stop envía SIGTERM al PGID y SIGKILL tras timeout (unix).
	Stop(spec StopSpec) error

	// Evaluate determina el estado del servicio con protección anti
	// PID-reuse vía creation_time.
	Evaluate(spec EvalSpec) Status
}
