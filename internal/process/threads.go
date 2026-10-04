// Package process samples OS threads straight from /proc/<pid>/task, which works for any language without a debugger.
package process

type ThreadInfo struct {
	TID   int
	Name  string // thread comm, truncated to 15 chars by the kernel
	State string // R/S/D/Z/T, stat field 3
	Ticks uint64 // accumulated utime+stime, only comparable as a delta between samples
}

// USER_HZ on Linux, the standard value (see getconf CLK_TCK).
const clockTicksPerSec = 100

func CPUPercent(deltaTicks uint64, elapsedSeconds float64) float64 {
	if elapsedSeconds <= 0 {
		return 0
	}
	return float64(deltaTicks) / (elapsedSeconds * clockTicksPerSec) * 100
}
