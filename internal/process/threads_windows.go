//go:build windows

package process

import "errors"

// Deliberate placeholder: Windows thread sampling would need NtQuerySystemInformation or wmic, out of scope for a Linux-first v1.
func ListThreads(pid int) ([]ThreadInfo, error) {
	return nil, errors.New("thread sampling: not supported on windows yet")
}
