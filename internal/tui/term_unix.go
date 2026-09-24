//go:build !windows

package tui

import (
	"os/exec"
	"syscall"
)

// setSessionLeader hace del shell un líder de sesión con el PTY como
// terminal de control: job control completo dentro de la terminal
// (ctrl+c por app, propios pgid de los hijos) y un pgid propio para
// poder matar el árbol entero al cerrar.
func setSessionLeader(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true // Ctty por defecto = fd 0 (el slave)
}

// killSessionGroup remata el grupo del shell (pgid negativo = grupo
// completo); errores ignorados: el HUP del close del master ya suele
// bastar y el pgid puede no existir ya.
func killSessionGroup(pgid int) {
	if pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}
