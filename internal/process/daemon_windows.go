//go:build windows

package process

import "fmt"

// windowsManager es un placeholder documentado para v2 (el diseño no
// bloquea Windows, ver proposal "Consideraciones cross-platform").
//
// Plan de implementación v2:
//   - Spawn: exec.Command con SysProcAttr{CreationFlags: CREATE_NEW_PROCESS_GROUP |
//     DETACHED_PROCESS | CREATE_NO_WINDOW} para desacoplar del padre.
//   - Graceful stop: CTRL_BREAK_EVENT al grupo (no hay SIGTERM en Windows),
//     con timeout.
//   - Forzado: taskkill /T /F o TerminateJobObject (Job Objects para árboles).
//   - Liveness/creation-time: gopsutil ya funciona igual en Windows (portable).
type windowsManager struct{}

// NewManager devuelve el Manager de la plataforma actual.
func NewManager() Manager { return &windowsManager{} }

func (w *windowsManager) Start(spec StartSpec) (StartResult, error) {
	return StartResult{}, fmt.Errorf("process: soporte Windows no implementado en v1 (ver daemon_windows.go)")
}

func (w *windowsManager) Stop(spec StopSpec) error {
	return fmt.Errorf("process: soporte Windows no implementado en v1 (ver daemon_windows.go)")
}

func (w *windowsManager) Evaluate(spec EvalSpec) Status {
	return StatusUnknown
}
