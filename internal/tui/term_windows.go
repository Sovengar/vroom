//go:build windows

package tui

import (
	"os/exec"
)

// setSessionLeader es no-op en Windows: ConPTY maneja la consola del
// proceso y Close del ConPTY lo termina.
func setSessionLeader(cmd *exec.Cmd) {}

// killSessionGroup es no-op en Windows: cerrar el ConPTY mata el
// proceso adjunto.
func killSessionGroup(pgid int) {}
