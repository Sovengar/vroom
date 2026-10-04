//go:build !windows

package tui

import (
	"os/exec"
	"syscall"
)

// Setsid plus Setctty give the shell full job control (per-app ctrl+c, its own child pgids) and a pgid of its own, so shutdown can kill the whole tree.
func setSessionLeader(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true // Ctty defaults to fd 0, the slave side of the PTY
}

// A negative pgid signals the whole group, and errors are ignored: closing the master already HUPs the session and the pgid may be gone by now.
func killSessionGroup(pgid int) {
	if pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}
