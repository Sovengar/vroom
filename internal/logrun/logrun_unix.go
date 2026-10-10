//go:build unix

package logrun

import (
	"os/exec"
	"syscall"
)

// isolate puts the hook's shell in its own process group so a cancelled start can terminate the whole hook tree, not only the sh that the default Cancel would signal.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup terminates the hook's process group (the negative pid is the group created by isolate), so descendants holding the service log fds die with it. It is wired as exec's Cancel, which runs only after the process exists.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
