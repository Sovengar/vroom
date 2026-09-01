//go:build unix

package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v3/process"
)

// unixManager implementa Manager para plataformas unix.
type unixManager struct{}

// NewManager devuelve el Manager de la plataforma actual.
func NewManager() Manager { return &unixManager{} }

// Start daemoniza el comando (spec R7):
//  1. sh -c "{command}" para soportar pipes/redirecciones
//  2. setsid() → nuevo session leader (sobrevive al cierre de la TUI)
//  3. stdout/stderr redirigidos a ficheros de log (append, preserva histórico)
//  4. Un goroutine reaper hace Wait() para evitar zombies mientras la TUI vive
func (u *unixManager) Start(spec StartSpec) (StartResult, error) {
	if spec.Command == "" {
		return StartResult{}, fmt.Errorf("empty command")
	}
	if err := os.MkdirAll(filepath.Dir(spec.StdoutPath), 0o755); err != nil {
		return StartResult{}, fmt.Errorf("could not create log directory: %w", err)
	}

	stdout, err := os.OpenFile(spec.StdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return StartResult{}, fmt.Errorf("could not open stdout.log: %w", err)
	}
	defer stdout.Close()

	stderr, err := os.OpenFile(spec.StderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return StartResult{}, fmt.Errorf("could not open stderr.log: %w", err)
	}
	defer stderr.Close()

	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return StartResult{}, fmt.Errorf("could not open /dev/null: %w", err)
	}
	defer devNull.Close()

	cmd := exec.Command("sh", "-c", spec.Command)
	cmd.Dir = spec.WorkDir
	cmd.Stdin = devNull
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return StartResult{}, fmt.Errorf("failed to start %q: %w", spec.Command, err)
	}
	pid := cmd.Process.Pid

	// Reaper: el hijo sigue siendo hijo de este proceso hasta que muere;
	// sin Wait() quedaría zombie mientras la TUI esté abierta.
	go func() { _ = cmd.Wait() }()

	result := StartResult{Pid: pid, Pgid: pid} // con setsid, pgid == pid
	if p, err := gopsprocess.NewProcess(int32(pid)); err == nil {
		if ct, err := p.CreateTime(); err == nil {
			result.CreationTimeMs = ct
		}
	}
	return result, nil
}

// Stop ejecuta el shutdown gradual (spec R8): SIGTERM al PGID, espera
// timeout, y si sigue vivo SIGKILL al PGID (mata todo el grupo,
// incluyendo hijos que hayan hecho fork).
func (u *unixManager) Stop(spec StopSpec) error {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	if spec.Pgid <= 0 {
		return nil // servicio sin proceso registrado: nada que parar
	}

	_ = syscall.Kill(-spec.Pgid, syscall.SIGTERM)
	if waitGroupGone(spec.Pgid, timeout) {
		return nil
	}
	_ = syscall.Kill(-spec.Pgid, syscall.SIGKILL)
	waitGroupGone(spec.Pgid, 2*time.Second)
	return nil
}

// Evaluate implementa el orden de confianza (spec R9):
//  1. PID vivo + creation_time coincide → si no: stopped (PID reuse)
//  2. Si hay verificaciones configuradas (puerto/pattern) y todas fallan → unknown
//  3. En caso contrario → running
func (u *unixManager) Evaluate(spec EvalSpec) Status {
	if spec.Pid <= 0 || !Alive(spec.Pid, spec.CreationTimeMs) {
		return StatusStopped
	}
	checked := false
	ok := false
	if spec.Port > 0 {
		checked = true
		ok = PortOpen(spec.Port)
	}
	if spec.ProcessPattern != "" {
		checked = true
		ok = ok || PatternMatch(spec.ProcessPattern)
	}
	if checked && !ok {
		return StatusUnknown
	}
	return StatusRunning
}

// PatternMatch verifica si pgrep -f encuentra el patrón (unix).
func PatternMatch(pattern string) bool {
	return exec.Command("pgrep", "-f", pattern).Run() == nil
}

// waitGroupGone sondea hasta que el process group desaparece o expira.
func waitGroupGone(pgid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !pgidAlive(pgid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// pgidAlive comprueba si queda algún proceso en el grupo via kill(-pgid, 0).
func pgidAlive(pgid int) bool {
	err := syscall.Kill(-pgid, syscall.Signal(0))
	switch err {
	case nil:
		return true
	case syscall.ESRCH:
		return false
	case syscall.EPERM:
		return true // existe pero no es nuestro
	default:
		return false
	}
}
