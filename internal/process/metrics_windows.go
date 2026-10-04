//go:build windows

package process

import "fmt"

func ReadMetrics(pid int) (Metrics, error) {
	return Metrics{}, fmt.Errorf("process metrics not supported on windows")
}

func ReadEnviron(pid int) ([]string, error) {
	return nil, fmt.Errorf("process environ not supported on windows")
}
