//go:build windows

package process

import "fmt"

// ReadMetrics no está soportado en Windows.
func ReadMetrics(pid int) (Metrics, error) {
	return Metrics{}, fmt.Errorf("process metrics not supported on windows")
}

// ReadEnviron no está soportado en Windows.
func ReadEnviron(pid int) ([]string, error) {
	return nil, fmt.Errorf("process environ not supported on windows")
}
