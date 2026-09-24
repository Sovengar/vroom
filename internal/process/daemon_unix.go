//go:build unix

package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v3/process"
)

// unixManager implementa Manager para plataformas unix.
type unixManager struct{}

// NewManager devuelve el Manager de la plataforma actual.
func NewManager() Manager { return &unixManager{} }

// Start daemoniza el comando:
//  1. sh -c "{command}" para soportar pipes/redirecciones
//  2. setsid() → nuevo session leader (sobrevive al cierre de la TUI)
//  3. stdout/stderr redirigidos a ficheros de log (truncados en cada start)
//  4. Un goroutine reaper hace Wait() para evitar zombies mientras la TUI vive
func (u *unixManager) Start(spec StartSpec) (StartResult, error) {
	if spec.Command == "" {
		return StartResult{}, fmt.Errorf("empty command")
	}
	if err := os.MkdirAll(filepath.Dir(spec.StdoutPath), 0o755); err != nil {
		return StartResult{}, fmt.Errorf("could not create log directory: %w", err)
	}

	// Limpiar logs anteriores antes de abrir en modo append.
	_ = os.Truncate(spec.StdoutPath, 0)
	_ = os.Truncate(spec.StderrPath, 0)

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

// Stop ejecuta el shutdown gradual: SIGTERM al PGID, espera
// timeout, y si sigue vivo SIGKILL al PGID (mata todo el grupo,
// incluyendo hijos que hayan hecho fork).
// Si tras SIGKILL el puerto sigue abierto, usa fuser como último recurso
// para liberarlo (servicios reiniciados externamente con PID ajeno).
func (u *unixManager) Stop(spec StopSpec) error {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = DefaultStopTimeout
	}
	if spec.Pgid > 0 {
		_ = syscall.Kill(-spec.Pgid, syscall.SIGTERM)
		if waitGroupGone(spec.Pgid, timeout) {
			return nil
		}
		_ = syscall.Kill(-spec.Pgid, syscall.SIGKILL)
		waitGroupGone(spec.Pgid, 2*time.Second)
	}

	// Último recurso: si el puerto sigue abierto, matar lo que lo ocupe.
	if spec.Port > 0 && PortOpen(spec.Port) {
		killPortHolder(spec.Port)
	}
	return nil
}

// Evaluate implementa el orden de confianza:
//  1. PID vivo + creation_time coincide → base de confianza
//  2. Si PID muere, fallback a puerto+pattern para detectar reinicio externo
//  3. Si hay verificaciones configuradas (puerto/pattern) y todas fallan → unknown
//  4. En caso contrario → running
func (u *unixManager) Evaluate(spec EvalSpec) Status {
	pidAlive := spec.Pid > 0 && Alive(spec.Pid, spec.CreationTimeMs)

	checked := false
	ok := false
	portOpen := false
	patternMatch := false
	if spec.Port > 0 {
		checked = true
		portOpen = PortOpen(spec.Port)
		ok = portOpen
	}
	if spec.ProcessPattern != "" {
		checked = true
		patternMatch = PatternMatch(spec.ProcessPattern)
		ok = ok || patternMatch
	}

	if pidAlive {
		if checked && !ok {
			return StatusUnknown
		}
		return StatusRunning
	}

	// PID muerto: fallback externo (servicio reiniciado fuera de vroom).
	// Si pattern matchea → running (el servicio está ahí).
	// Si solo el puerto está abierto → verificar que el proceso escuchando
	// sea el mismo servicio (misma creation_time) para evitar falsos positivos
	// cuando otro servicio usa el mismo puerto.
	if ok {
		if patternMatch {
			return StatusRunning
		}
		if portOpen {
			ownerPID := PortOwnerPID(spec.Port)
			if ownerPID > 0 {
				ownerAlive := Alive(int(ownerPID), spec.CreationTimeMs)
				if !ownerAlive {
					return StatusStopped
				}
			}
			return StatusRunning
		}
		return StatusRunning
	}
	return StatusStopped
}

// PatternMatch verifica si pgrep -f encuentra el patrón (unix).
// Excluye el propio proceso pgrep y sus ancestros para evitar falsos
// positivos (pgrep -f matchea su propio command line).
func PatternMatch(pattern string) bool {
	out, err := exec.Command("pgrep", "-f", pattern).Output()
	if err != nil {
		return false
	}
	myPid := os.Getpid()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		if pid != myPid {
			return true
		}
	}
	return false
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

// killPortHolder mata el proceso que escucha en el puerto dado usando fuser
// y espera a que el puerto se libere.
func killPortHolder(port int) {
	_ = exec.Command("fuser", "-k", fmt.Sprintf("%d/tcp", port)).Run()
	// Esperar a que el puerto se libere (max 3s).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !PortOpen(port) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
