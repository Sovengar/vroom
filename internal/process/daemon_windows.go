//go:build windows

package process

import "fmt"

// Deliberate v1 placeholder: Windows detachment needs CREATE_NEW_PROCESS_GROUP plus DETACHED_PROCESS and graceful stop has no SIGTERM, out of scope for a Linux-first v1.
type windowsManager struct{}

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
