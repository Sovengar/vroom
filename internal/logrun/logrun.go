// Package logrun runs one synchronous shell job and records it in the service logs — banner, output and footer — so a build, a stop and a pre_start hook are the same kind of entry in the Console and in `vroom logs`.
package logrun

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Run executes command through sh -c in workDir and appends everything to the two log paths. The exit code is 0 unless the command really ran and failed, which keeps "the runner could not even start it" (unwritable log, no sh) apart from "the command returned non-zero": only the second one says something about the manifest.
func Run(kind, command, workDir, stdoutPath, stderrPath string) (time.Duration, int, error) {
	if dir := filepath.Dir(stdoutPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, 0, err
		}
	}

	banner := fmt.Sprintf("── vroom ▶ %s: %s ──", kind, command)

	// One descriptor serves both the banner and the command: two OpenFile calls made the second error unreachable, hiding a full disk (/dev/full) behind a check that could never fail.
	out, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = out.Close() }()
	// Banner before stderr opens on purpose: if stderr fails, the log still says which command never ran.
	if _, err := fmt.Fprintf(out, "%s\n", banner); err != nil {
		return 0, 0, err
	}

	start := time.Now()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workDir
	errF, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = errF.Close() }()
	cmd.Stdout = out
	cmd.Stderr = errF
	runErr := cmd.Run()
	elapsed := time.Since(start).Round(10 * time.Millisecond)
	if runErr != nil {
		exitCode := 0
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		// Footer reuses the open descriptor and drops its error: the command already ran, so reporting a write failure would lie about whether the job ran.
		_, _ = fmt.Fprintf(out, "── vroom ✗ %s failed (exit %d, %s) ──\n", kind, exitCode, elapsed)
		return elapsed, exitCode, runErr
	}
	_, _ = fmt.Fprintf(out, "── vroom ✓ %s ok (%s) ──\n", kind, elapsed)
	return elapsed, 0, nil
}
