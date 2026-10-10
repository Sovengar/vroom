package logrun

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The banner is the only thing that tells an `echo` from a build apart from the service's own echo in one merged log.
func TestRunWritesBannerOutputAndFooter(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.log")
	errLog := filepath.Join(dir, "err.log")

	elapsed, code, err := Run("build", "echo hola; echo mal >&2; exit 3", dir, out, errLog)
	if err == nil {
		t.Fatal("a command exiting 3 must report a failure")
	}
	if code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	if elapsed < 0 {
		t.Errorf("elapsed = %v, must not be negative", elapsed)
	}

	body := readFile(t, out)
	for _, want := range []string{"── vroom ▶ build: echo hola; echo mal >&2; exit 3 ──", "hola", "── vroom ✗ build failed (exit 3, "} {
		if !strings.Contains(body, want) {
			t.Errorf("stdout log = %q, want it to contain %q", body, want)
		}
	}
	if got := readFile(t, errLog); !strings.Contains(got, "mal") {
		t.Errorf("stderr log = %q, want the command's stderr", got)
	}
}

func TestRunReportsSuccessAndCreatesTheLogDirectory(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "nested", "out.log") // no directory yet: creating it is the runner's job
	errLog := filepath.Join(dir, "err.log")

	elapsed, code, err := Run("pre_run", "echo listo", dir, out, errLog)
	if err != nil {
		t.Fatalf("unexpected failure: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if elapsed < 0 {
		t.Errorf("elapsed = %v, must not be negative", elapsed)
	}
	if got := readFile(t, out); !strings.Contains(got, "── vroom ✓ pre_run ok (") {
		t.Errorf("stdout log = %q, want the ok footer", got)
	}
}

// The errno has to reach the caller unwrapped: whoever reads it is diagnosing permissions or a full disk.
func TestRunFailsBeforeLaunchingWhenTheLogDirIsUnusable(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocking")
	if err := os.WriteFile(blocker, []byte("I am a file"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, err := runNoElapsed(t, filepath.Join(blocker, "out.log"), "")
	if err == nil {
		t.Fatal("with the log directory unusable the command cannot be launched")
	}
	if code != 0 {
		t.Errorf("exit code = %d with a launch failure, want 0: nothing got to execute", code)
	}
	if !strings.Contains(err.Error(), blocker) {
		t.Errorf("err = %q, want it to name the path it could not create", err)
	}
}

// A directory where the log file goes passes mkdir and fails the open: the job must not run, because its output would be lost entirely.
func TestRunFailsWhenStdoutCannotBeOpened(t *testing.T) {
	dir := t.TempDir()

	code, err := runNoElapsed(t, makeDir(t, filepath.Join(dir, "stdout.log")), "")
	if err == nil {
		t.Fatal("a stdout.log that is a directory cannot receive the output")
	}
	if code != 0 {
		t.Errorf("exit code = %d with a launch failure, want 0: nothing got to execute", code)
	}
}

// MEASURED: /dev/full opens, writes and returns ENOSPC, so the banner-write branch is provoked for real instead of with an unreproducible permission.
func TestRunFailsWhenTheBannerCannotBeWritten(t *testing.T) {
	const lleno = "/dev/full"
	if _, err := os.Stat(lleno); err != nil {
		t.Skipf("this machine does not have %s, and without it there is no way to open a log that "+
			"accepts the OpenFile and rejects writing", lleno)
	}

	code, err := runNoElapsed(t, lleno, "")
	if err == nil {
		t.Fatal("with a log that does not accept writes the command cannot be launched")
	}
	if code != 0 {
		t.Errorf("exit code = %d with a launch failure, want 0: nothing got to execute", code)
	}
}

// The banner goes down before stderr is opened, so a log that cannot open stderr still names the job that never ran.
func TestRunWritesTheBannerEvenWhenStderrCannotBeOpened(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.log")

	_, code, err := Run("build", "echo hola", dir, out, filepath.Join(dir, "no-existe", "err.log"))
	if err == nil {
		t.Fatal("with stderr impossible to open the command cannot be launched")
	}
	if code != 0 {
		t.Errorf("exit code = %d with a launch failure, want 0", code)
	}
	if got := readFile(t, out); !strings.Contains(got, "build") {
		t.Errorf("stdout log = %q, want it to name the job", got)
	}
}

// A shell that never runs (a workDir that does not exist) is not a non-zero exit: exit code 0 with an error is how the caller tells "could not run" from "ran and failed".
func TestRunALaunchFailureIsNotANonZeroExit(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.log")

	elapsed, code, err := Run("build", "echo hola", filepath.Join(dir, "no-existe"), out, filepath.Join(dir, "err.log"))
	if err == nil {
		t.Fatal("a workDir that does not exist cannot run a command")
	}
	if code != 0 {
		t.Errorf("exit code = %d with a launch failure, want 0", code)
	}
	if elapsed < 0 {
		t.Errorf("elapsed = %v, must not be negative", elapsed)
	}
	if got := readFile(t, out); !strings.Contains(got, "── vroom ✗ build failed (exit 0, ") {
		t.Errorf("stdout log = %q, want the footer the caller distinguishes by exit code 0", got)
	}
}

func runNoElapsed(t *testing.T, stdoutPath, stderrPath string) (int, error) {
	t.Helper()
	_, code, err := Run("build", "echo hola", t.TempDir(), stdoutPath, stderrPath)
	return code, err
}

func makeDir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
