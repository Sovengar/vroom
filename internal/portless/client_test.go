package portless

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newClientFor(t *testing.T, exec func(context.Context, string, ...string) (string, int, error)) *Client {
	t.Helper()
	return New(WithBinary("/fake/portless"), WithStateDir(t.TempDir()), WithExec(exec))
}

func TestRegisterSinBinarioFalla(t *testing.T) {
	err := New(WithBinary("")).Register("svc", 8080)

	if err == nil {
		t.Fatal("Register without binary should fail")
	}
	if !strings.Contains(err.Error(), ReasonPortlessMissing) {
		t.Errorf("error %q does not say the binary is missing", err)
	}
}

func TestRegisterPropagaElErrorDelBinario(t *testing.T) {
	c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
		return "", 1, errors.New("requires Node >= 24")
	})

	err := c.Register("svc", 8080)

	if err == nil {
		t.Fatal("Register should propagate the binary failure")
	}
	if !strings.Contains(err.Error(), "Node") {
		t.Errorf("error lost the binary message: %q", err)
	}
}

// MEASURED (M8): `alias` is an unconditional upsert that exits 0, so its exit code proves nothing about a name held on another port.
func TestRegisterConExitDistintoDeCero(t *testing.T) {
	t.Run("exit 1 without transport error", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "Error: requires Node >= 24", 1, nil
		})

		err := c.Register("svc", 8080)

		if err == nil {
			t.Fatal("Register with exit 1 should fail even if exec returns no error")
		}
		if !strings.Contains(err.Error(), ReasonPortlessFailed) {
			t.Errorf("error %q does not classify the binary failure", err)
		}
	})

	t.Run("exit 0 is success", func(t *testing.T) {
		var sawArgs []string
		c := newClientFor(t, func(_ context.Context, _ string, args ...string) (string, int, error) {
			sawArgs = args
			return "Alias registered", 0, nil
		})

		if err := c.Register("svc", 8080); err != nil {
			t.Fatalf("Register with exit 0 failed: %v", err)
		}
		joined := strings.Join(sawArgs, " ")
		if !strings.Contains(joined, "svc") || !strings.Contains(joined, "8080") {
			t.Errorf("Register invoked %q, missing name or port", joined)
		}
	})
}

// Remove deliberately differs from RemoveAbsent: swallowing absence is its contract, RemoveAbsent must still tell it from a real failure.
func TestRemoveEsBenignoPorContrato(t *testing.T) {
	t.Run("empty name: does nothing", func(t *testing.T) {
		called := false
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			called = true
			return "", 0, nil
		})
		if err := c.Remove(""); err != nil {
			t.Errorf("Remove(\"\") = %v, want nil", err)
		}
		if called {
			t.Error("Remove invoked the binary with empty name")
		}
	})

	t.Run("without binary: nothing to remove", func(t *testing.T) {
		c := New(WithBinary(""))
		if err := c.Remove("svc"); err != nil {
			t.Errorf("Remove without binary = %v, want nil", err)
		}
	})

	t.Run("did not exist (M10, exit 1): benign", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "", 1, errors.New(`Error: No alias found for "svc.localhost".`)
		})
		if err := c.Remove("svc"); err != nil {
			t.Errorf("Remove of an absent route = %v, want nil (benign by contract)", err)
		}
	})

	t.Run("effective removal", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "Removed alias: svc.localhost", 0, nil
		})
		if err := c.Remove("svc"); err != nil {
			t.Errorf("Remove = %v, want nil", err)
		}
	})

	t.Run("real failure with code 0: propagates", func(t *testing.T) {
		// code 0 with an error is the timeout profile: the deadline killed the process, so removing that way could leave the route live.
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "", 0, context.DeadlineExceeded
		})
		err := c.Remove("svc")
		if err == nil {
			t.Fatal("Remove with timeout should propagate")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error does not preserve DeadlineExceeded: %v", err)
		}
	})
}

func TestRemoveAbsentSoloRevocaEnExitoYAusencia(t *testing.T) {
	t.Run("empty name", func(t *testing.T) {
		c := New(WithBinary(""))
		if err := c.RemoveAbsent(""); err != nil {
			t.Errorf("RemoveAbsent(\"\") = %v, want nil", err)
		}
	})

	t.Run("without binary", func(t *testing.T) {
		if err := New(WithBinary("")).RemoveAbsent("svc"); err != nil {
			t.Errorf("RemoveAbsent without binary = %v, want nil", err)
		}
	})

	t.Run("success: nil", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "Removed", 0, nil
		})
		if err := c.RemoveAbsent("svc"); err != nil {
			t.Errorf("effective removal = %v, want nil", err)
		}
	})

	t.Run("benign M10: ErrRouteAbsent, distinguishable from nil", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "", 1, errors.New(`Error: No alias found for "svc.localhost".`)
		})
		err := c.RemoveAbsent("svc")
		if !errors.Is(err, ErrRouteAbsent) {
			t.Errorf("absent route = %v, want ErrRouteAbsent: whoever revokes needs to distinguish it from a failure", err)
		}
	})

	t.Run("exit 1 that is NOT M10: failure, does not revoke", func(t *testing.T) {
		// All exit 1 but real failures: the original default->nil revoked every one of them.
		for _, msg := range []string{
			"requires Node >= 24",
			"EACCES: permission denied",
			"SyntaxError: Unexpected token } in JSON",
		} {
			c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
				return "", 1, errors.New(msg)
			})
			err := c.RemoveAbsent("svc")
			if err == nil {
				t.Errorf("%q: nil was returned, a failed removal would be declared closed", msg)
			}
			if errors.Is(err, ErrRouteAbsent) {
				t.Errorf("%q: a failure was confused with benign absence", msg)
			}
		}
	})

	t.Run("exit != 1 without error: builds the error with the code", func(t *testing.T) {
		// Without an error built here the caller gets nil and revokes on an exit 2.
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "", 2, nil
		})
		err := c.RemoveAbsent("svc")
		if err == nil {
			t.Fatal("exit 2 without transport error should produce error")
		}
		if !strings.Contains(err.Error(), "2") {
			t.Errorf("error %q does not mention the exit code", err)
		}
	})
}

// isRouteAbsent matches on text, not errors.Is, since there is no other signal, so what must be pinned is that nothing else matches, wrapping included.
func TestIsRouteAbsentNoConfundeLosSimilares(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"the measured message", errors.New(`Error: No alias found for "svc.localhost".`), true},
		{"in lowercase", errors.New(`error: no alias found for "x.localhost".`), true},
		{"wrapped", wrappedRouteAbsentText(), true},
		{"old node", errors.New("requires Node >= 24"), false},
		{"permissions", errors.New("EACCES: permission denied"), false},
		{"corrupt json", errors.New("SyntaxError: Unexpected token }"), false},
		{"context without alias", errors.New("cannot resolve alias"), false},
		{"nil", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRouteAbsent(tt.err); got != tt.want {
				t.Errorf("isRouteAbsent(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// Its three failures are all errors: a "not found" disguised as a failure makes Apply write over another owner's route.
func TestLookup(t *testing.T) {
	t.Run("without binary", func(t *testing.T) {
		_, found, err := New(WithBinary("")).Lookup("svc")
		if err == nil {
			t.Fatal("Lookup without binary should fail")
		}
		if found {
			t.Error("found=true without binary")
		}
		if !strings.Contains(err.Error(), ReasonPortlessMissing) {
			t.Errorf("error %q does not say the binary is missing", err)
		}
	})

	t.Run("transport failure", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "", 0, context.DeadlineExceeded
		})
		if _, _, err := c.Lookup("svc"); err == nil {
			t.Error("Lookup should propagate the transport failure")
		}
	})

	t.Run("exit != 0 without transport error", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "", 1, nil
		})
		_, found, err := c.Lookup("svc")
		if err == nil {
			t.Fatal("exit 1 without error should fail")
		}
		if found {
			t.Error("found=true with failed exit")
		}
	})

	t.Run("unreadable port in output", func(t *testing.T) {
		// Hand-written because real portless never emits it: a host that matches with an unreadable port must not get an invented one.
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			out := "  http://" + Hostname("svc") + ":1355  ->  localhost:99999999999999999999999  (alias)\n"
			return out, 0, nil
		})
		_, found, err := c.Lookup("svc")
		if found {
			t.Error("found=true with an unreadable port: a port that is not one would be asserted")
		}
		// A 20-digit port overflows Atoi, which is a real error and not a "not found".
		if err == nil {
			t.Error("an overflowed port should be an error, not absence")
		}
	})

	t.Run("the route exists with its port", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			host := Hostname("svc")
			return "\nActive routes:\n\n  http://" + host + ":1355  ->  localhost:8080  (alias)\n", 0, nil
		})
		port, found, err := c.Lookup("svc")
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Error("found=false for a listed route")
		}
		if port != 8080 {
			t.Errorf("port = %d, want 8080", port)
		}
	})

	t.Run("the table does not contain it", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			other := Hostname("otro")
			return "  http://" + other + ":1355  ->  localhost:8080  (alias)\n", 0, nil
		})
		port, found, err := c.Lookup("svc")
		if err != nil {
			t.Fatal(err)
		}
		if found {
			t.Error("found=true for a route that is not in the table")
		}
		if port != 0 {
			t.Errorf("port = %d in an absence, want 0", port)
		}
	})
}

// MEASURED (M6): proxy.port exists only while the proxy runs, so its absence IS the no-proxy signal; a corrupt file is indistinguishable from a stopped one.
func TestProxyPort(t *testing.T) {
	t.Run("without state dir", func(t *testing.T) {
		c := New(WithBinary("/fake/portless"))
		if _, err := c.ProxyPort(); !errors.Is(err, ErrProxyNotRunning) {
			t.Errorf("without state dir = %v, want ErrProxyNotRunning", err)
		}
	})

	t.Run("without proxy.port", func(t *testing.T) {
		c := New(WithBinary("/fake/portless"), WithStateDir(t.TempDir()))
		if _, err := c.ProxyPort(); !errors.Is(err, ErrProxyNotRunning) {
			t.Errorf("without proxy.port = %v, want ErrProxyNotRunning", err)
		}
	})

	t.Run("unreadable proxy.port", func(t *testing.T) {
		for _, content := range []string{"abc", "", "0", "-1", "70000", "1355\n\nbasura"} {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, proxyPortFile), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			c := New(WithBinary("/fake/portless"), WithStateDir(dir))
			if _, err := c.ProxyPort(); !errors.Is(err, ErrProxyNotRunning) {
				t.Errorf("proxy.port=%q gave %v, want ErrProxyNotRunning: degrading is the safe option", content, err)
			}
		}
	})

	t.Run("the valid port is read", func(t *testing.T) {
		dir := t.TempDir()
		// MEASURED: 4 bytes with no trailing newline.
		if err := os.WriteFile(filepath.Join(dir, proxyPortFile), []byte("1399"), 0o644); err != nil {
			t.Fatal(err)
		}
		c := New(WithBinary("/fake/portless"), WithStateDir(dir))
		port, err := c.ProxyPort()
		if err != nil {
			t.Fatal(err)
		}
		if port != 1399 {
			t.Errorf("port = %d, want 1399", port)
		}
	})

	t.Run("proxy.port is a directory", func(t *testing.T) {
		// A read error that is not NotExist must propagate: it is a different cause for whoever debugs it.
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, proxyPortFile), 0o755); err != nil {
			t.Fatal(err)
		}
		c := New(WithBinary("/fake/portless"), WithStateDir(dir))
		_, err := c.ProxyPort()
		if errors.Is(err, ErrProxyNotRunning) {
			t.Error("an unreadable proxy.port disguised itself as 'no proxy'")
		}
		if err == nil {
			t.Error("should propagate the read error")
		}
	})
}

// WaitDelay is what bounds wall-clock time: without it a surviving child holding the pipes hangs Wait past the deadline.
func TestExecCommandAcotaConElDeadline(t *testing.T) {
	t.Run("a child that ignores death does not hang Wait", func(t *testing.T) {
		dir := t.TempDir()
		script := filepath.Join(dir, "colgado")
		if err := os.WriteFile(script, []byte("#!/bin/sh\ntrap '' TERM\nsleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		done := make(chan struct{})
		var (
			code int
			err  error
		)
		go func() {
			_, code, err = execCommand(ctx, script)
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("execCommand did not return: WaitDelay does not bound the real clock")
		}

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want DeadlineExceeded (wrapped)", err)
		}
		if code == 0 {
			t.Error("code = 0 in a process that did not terminate properly")
		}
	})

	t.Run("success: stdout and code 0", func(t *testing.T) {
		dir := t.TempDir()
		script := filepath.Join(dir, "ok")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho hola\necho salida-err >&2\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		out, code, err := execCommand(context.Background(), script)

		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if code != 0 {
			t.Errorf("code = %d, want 0", code)
		}
		if strings.TrimSpace(out) != "hola" {
			t.Errorf("stdout = %q, want hola", out)
		}
	})

	t.Run("failure with stderr: the message reaches the error", func(t *testing.T) {
		dir := t.TempDir()
		script := filepath.Join(dir, "falla")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'requires Node >= 24' >&2\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		_, code, err := execCommand(context.Background(), script)

		if err == nil {
			t.Fatal("an exit 1 should be an error")
		}
		if code != 1 {
			t.Errorf("code = %d, want 1", code)
		}
		if !strings.Contains(err.Error(), "Node") {
			t.Errorf("error does not include stderr: %q", err)
		}
	})

	t.Run("failure without stderr: the error is not left empty", func(t *testing.T) {
		dir := t.TempDir()
		script := filepath.Join(dir, "silencioso")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		_, code, err := execCommand(context.Background(), script)

		if err == nil {
			t.Fatal("an exit 3 should be an error")
		}
		if code != 3 {
			t.Errorf("code = %d, want 3", code)
		}
		if !strings.Contains(err.Error(), "exit status 3") {
			t.Errorf("without stderr the error should carry the status: %q", err)
		}
	})
}

// A literal path here is the bug of vroom running under a service manager whose env is not the login shell.
func TestMiseShimDirsNoDevuelveLiterales(t *testing.T) {
	t.Run("with HOME, both are derived", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		got := miseShimDirs()

		if len(got) != 2 {
			t.Fatalf("got = %v, want 2 directories", got)
		}
		for _, dir := range got {
			if !strings.HasPrefix(dir, home) {
				t.Errorf("the shim dir %q does not derive from HOME %q", dir, home)
			}
		}
	})

	t.Run("without HOME: nil instead of an invented path", func(t *testing.T) {
		t.Setenv("HOME", "")
		if got := miseShimDirs(); got != nil {
			t.Errorf("got = %v, want nil: a concrete user path would be a silent failure mode", got)
		}
	})
}

func wrappedRouteAbsentText() error {
	return fmt.Errorf("removing the route: %s", `Error: No alias found for "svc.localhost".`)
}
