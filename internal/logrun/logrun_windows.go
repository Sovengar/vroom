//go:build windows

package logrun

import "os/exec"

// isolate is a no-op on Windows: process groups need CREATE_NEW_PROCESS_GROUP plus DETACHED_PROCESS, out of scope for this Linux-first v1 (see internal/process/daemon_windows.go).
func isolate(cmd *exec.Cmd) {}

// killGroup kills only the direct shell on Windows, the best effort available without job objects.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
