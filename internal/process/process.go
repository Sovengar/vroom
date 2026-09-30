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
	// StatusPortPending: el proceso está vivo y el puerto ya está reservado
	// pero aún no escuchando. NO es "unknown": es un estado con nombre, para
	// que "arrancando" no se disfrace de "sano" ni de "roto".
	StatusPortPending Status = "port_pending"
	// StatusNoPort: el proceso está vivo y no tiene puerto TCP, y eso es su
	// estado, no una espera pendiente.
	StatusNoPort Status = "no_port"
	// StatusPortUnresolved: el proceso está vivo y el discovery se agotó
	// sin decidir el puerto principal. Distinto de StatusNoPort a propósito:
	// "no tiene puerto" y "no lo sabemos todavía" no admiten el mismo
	// tratamiento en la UI.
	StatusPortUnresolved Status = "port_unresolved"
)

// DefaultStopTimeout es el timeout de SIGTERM antes de SIGKILL.
const DefaultStopTimeout = 5 * time.Second

// StartSpec describe el arranque de un servicio daemonizado.
type StartSpec struct {
	Command    string // comando a ejecutar via sh -c
	WorkDir    string // directorio de trabajo del proceso
	StdoutPath string // fichero donde capturar stdout
	StderrPath string // fichero donde capturar stderr

	// Env son variables "KEY=VALUE" a INYECTAR (p.ej. PORT=41501). Se
	// fusionan con el entorno del padre; nunca lo reemplazan. Nil = el
	// hijo hereda el entorno tal cual, como hasta ahora.
	Env []string
}

// StartResult contiene las credenciales del proceso arrancado.
type StartResult struct {
	Pid            int
	Pgid           int // con setsid, Pgid == Pid
	CreationTimeMs int64
}

// StopSpec describe la parada de un process group.
type StopSpec struct {
	Pid     int // raíz del linaje; 0 = deducirla del PGID
	Pgid    int
	Port    int // 0 = no verificar puerto tras stop
	Timeout time.Duration

	// Warn recibe los avisos no fatales del stop (p.ej. no poder probar
	// que el dueño del puerto es propio). Nil = descartarlos.
	Warn func(format string, args ...any)
}

// warnf emite un aviso por el Warn del spec, si lo hay.
func (s StopSpec) warnf(format string, args ...any) {
	if s.Warn != nil {
		s.Warn(format, args...)
	}
}

// EvalSpec contiene las credenciales registradas para evaluar el estado.
type EvalSpec struct {
	Pid            int
	CreationTimeMs int64
	Port           int    // 0 = no verificar
	ProcessPattern string // "" = no verificar
	// PortPending marca que el puerto está reservado y el discovery sigue en
	// vuelo: si el proceso vive y el puerto aún no abre, el estado es
	// "pending", no "unknown". Sólo lo fija el arranque en modo dynamic.
	PortPending bool
	// PortUnresolved marca que el discovery se agotó sin decidir puerto.
	// El proceso vive y puede que aún no haya hecho bind; no es "no tiene
	// puerto" ni "todo bien".
	PortUnresolved bool
	// NoPort marca que el proceso vive y no expone puerto TCP, y que eso es
	// su estado. Sin él un worker solo-UDP se reportaría como "running", que
	// no lo distingue de un servicio sano.
	NoPort bool
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
