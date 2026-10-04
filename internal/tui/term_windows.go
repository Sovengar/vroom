//go:build windows

package tui

import (
	"os/exec"
)

// No-op on Windows: ConPTY owns the process console and closing it terminates the shell.
func setSessionLeader(cmd *exec.Cmd) {}

func killSessionGroup(pgid int) {}
