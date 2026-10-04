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
		t.Fatal("Register sin binario deberia fallar")
	}
	if !strings.Contains(err.Error(), ReasonPortlessMissing) {
		t.Errorf("el error %q no dice que falta el binario", err)
	}
}

func TestRegisterPropagaElErrorDelBinario(t *testing.T) {
	c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
		return "", 1, errors.New("requires Node >= 24")
	})

	err := c.Register("svc", 8080)

	if err == nil {
		t.Fatal("Register deberia propagar el fallo del binario")
	}
	if !strings.Contains(err.Error(), "Node") {
		t.Errorf("el error perdio el mensaje del binario: %q", err)
	}
}

// MEASURED (M8): `alias` is an unconditional upsert that exits 0, so its exit code proves nothing about a name held on another port.
func TestRegisterConExitDistintoDeCero(t *testing.T) {
	t.Run("exit 1 sin error de transporte", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "Error: requires Node >= 24", 1, nil
		})

		err := c.Register("svc", 8080)

		if err == nil {
			t.Fatal("Register con exit 1 deberia fallar aunque exec no devuelva error")
		}
		if !strings.Contains(err.Error(), ReasonPortlessFailed) {
			t.Errorf("el error %q no clasifica el fallo del binario", err)
		}
	})

	t.Run("exit 0 es exito", func(t *testing.T) {
		var sawArgs []string
		c := newClientFor(t, func(_ context.Context, _ string, args ...string) (string, int, error) {
			sawArgs = args
			return "Alias registered", 0, nil
		})

		if err := c.Register("svc", 8080); err != nil {
			t.Fatalf("Register con exit 0 fallo: %v", err)
		}
		joined := strings.Join(sawArgs, " ")
		if !strings.Contains(joined, "svc") || !strings.Contains(joined, "8080") {
			t.Errorf("Register invoco %q, faltan el nombre o el puerto", joined)
		}
	})
}

// Remove deliberately differs from RemoveAbsent: swallowing absence is its contract, RemoveAbsent must still tell it from a real failure.
func TestRemoveEsBenignoPorContrato(t *testing.T) {
	t.Run("nombre vacio: no hace nada", func(t *testing.T) {
		called := false
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			called = true
			return "", 0, nil
		})
		if err := c.Remove(""); err != nil {
			t.Errorf("Remove(\"\") = %v, want nil", err)
		}
		if called {
			t.Error("Remove invoco el binario con nombre vacio")
		}
	})

	t.Run("sin binario: nada que retirar", func(t *testing.T) {
		c := New(WithBinary(""))
		if err := c.Remove("svc"); err != nil {
			t.Errorf("Remove sin binario = %v, want nil", err)
		}
	})

	t.Run("no existia (M10, exit 1): benigno", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "", 1, errors.New(`Error: No alias found for "svc.localhost".`)
		})
		if err := c.Remove("svc"); err != nil {
			t.Errorf("Remove de una ruta ausente = %v, want nil (benigno por contrato)", err)
		}
	})

	t.Run("retirada efectiva", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "Removed alias: svc.localhost", 0, nil
		})
		if err := c.Remove("svc"); err != nil {
			t.Errorf("Remove = %v, want nil", err)
		}
	})

	t.Run("fallo real con code 0: propaga", func(t *testing.T) {
		// code 0 with an error is the timeout profile: the deadline killed the process, so removing that way could leave the route live.
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "", 0, context.DeadlineExceeded
		})
		err := c.Remove("svc")
		if err == nil {
			t.Fatal("Remove con timeout deberia propagar")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("el error no conserva el DeadlineExceeded: %v", err)
		}
	})
}

func TestRemoveAbsentSoloRevocaEnExitoYAusencia(t *testing.T) {
	t.Run("nombre vacio", func(t *testing.T) {
		c := New(WithBinary(""))
		if err := c.RemoveAbsent(""); err != nil {
			t.Errorf("RemoveAbsent(\"\") = %v, want nil", err)
		}
	})

	t.Run("sin binario", func(t *testing.T) {
		if err := New(WithBinary("")).RemoveAbsent("svc"); err != nil {
			t.Errorf("RemoveAbsent sin binario = %v, want nil", err)
		}
	})

	t.Run("exito: nil", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "Removed", 0, nil
		})
		if err := c.RemoveAbsent("svc"); err != nil {
			t.Errorf("retirada efectiva = %v, want nil", err)
		}
	})

	t.Run("benigno M10: ErrRouteAbsent, distinguible de nil", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "", 1, errors.New(`Error: No alias found for "svc.localhost".`)
		})
		err := c.RemoveAbsent("svc")
		if !errors.Is(err, ErrRouteAbsent) {
			t.Errorf("ruta ausente = %v, want ErrRouteAbsent: quien revoca necesita distinguirlo de un fallo", err)
		}
	})

	t.Run("exit 1 que NO es M10: fallo, no revoca", func(t *testing.T) {
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
				t.Errorf("%q: se devolvio nil, se declararia cerrada una retirada que fallo", msg)
			}
			if errors.Is(err, ErrRouteAbsent) {
				t.Errorf("%q: se confundio un fallo con la ausencia benigna", msg)
			}
		}
	})

	t.Run("exit != 1 sin error: construye el error con el codigo", func(t *testing.T) {
		// Without an error built here the caller gets nil and revokes on an exit 2.
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "", 2, nil
		})
		err := c.RemoveAbsent("svc")
		if err == nil {
			t.Fatal("exit 2 sin error de transporte deberia producir error")
		}
		if !strings.Contains(err.Error(), "2") {
			t.Errorf("el error %q no menciona el codigo de salida", err)
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
		{"el mensaje medido", errors.New(`Error: No alias found for "svc.localhost".`), true},
		{"en minusculas", errors.New(`error: no alias found for "x.localhost".`), true},
		{"envuelto", wrappedRouteAbsentText(), true},
		{"node viejo", errors.New("requires Node >= 24"), false},
		{"permisos", errors.New("EACCES: permission denied"), false},
		{"json corrupto", errors.New("SyntaxError: Unexpected token }"), false},
		{"contexto sin alias", errors.New("cannot resolve alias"), false},
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
	t.Run("sin binario", func(t *testing.T) {
		_, found, err := New(WithBinary("")).Lookup("svc")
		if err == nil {
			t.Fatal("Lookup sin binario deberia fallar")
		}
		if found {
			t.Error("found=true sin binario")
		}
		if !strings.Contains(err.Error(), ReasonPortlessMissing) {
			t.Errorf("el error %q no dice que falta el binario", err)
		}
	})

	t.Run("fallo de transporte", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "", 0, context.DeadlineExceeded
		})
		if _, _, err := c.Lookup("svc"); err == nil {
			t.Error("Lookup deberia propagar el fallo de transporte")
		}
	})

	t.Run("exit != 0 sin error de transporte", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			return "", 1, nil
		})
		_, found, err := c.Lookup("svc")
		if err == nil {
			t.Fatal("exit 1 sin error deberia fallar")
		}
		if found {
			t.Error("found=true con salida fallida")
		}
	})

	t.Run("puerto ilegible en la salida", func(t *testing.T) {
		// Hand-written because real portless never emits it: a host that matches with an unreadable port must not get an invented one.
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			out := "  http://" + Hostname("svc") + ":1355  ->  localhost:99999999999999999999999  (alias)\n"
			return out, 0, nil
		})
		_, found, err := c.Lookup("svc")
		if found {
			t.Error("found=true con un puerto ilegible: se afirmaria un puerto que no es")
		}
		// A 20-digit port overflows Atoi, which is a real error and not a "not found".
		if err == nil {
			t.Error("un puerto desbordado deberia ser error, no ausencia")
		}
	})

	t.Run("la ruta existe con su puerto", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			host := Hostname("svc")
			return "\nActive routes:\n\n  http://" + host + ":1355  ->  localhost:8080  (alias)\n", 0, nil
		})
		port, found, err := c.Lookup("svc")
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Error("found=false para una ruta listada")
		}
		if port != 8080 {
			t.Errorf("port = %d, want 8080", port)
		}
	})

	t.Run("la tabla no la contiene", func(t *testing.T) {
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			other := Hostname("otro")
			return "  http://" + other + ":1355  ->  localhost:8080  (alias)\n", 0, nil
		})
		port, found, err := c.Lookup("svc")
		if err != nil {
			t.Fatal(err)
		}
		if found {
			t.Error("found=true para una ruta que no esta en la tabla")
		}
		if port != 0 {
			t.Errorf("port = %d en una ausencia, want 0", port)
		}
	})
}

// MEASURED (M6): proxy.port exists only while the proxy runs, so its absence IS the no-proxy signal; a corrupt file is indistinguishable from a stopped one.
func TestProxyPort(t *testing.T) {
	t.Run("sin state dir", func(t *testing.T) {
		c := New(WithBinary("/fake/portless"))
		if _, err := c.ProxyPort(); !errors.Is(err, ErrProxyNotRunning) {
			t.Errorf("sin state dir = %v, want ErrProxyNotRunning", err)
		}
	})

	t.Run("sin proxy.port", func(t *testing.T) {
		c := New(WithBinary("/fake/portless"), WithStateDir(t.TempDir()))
		if _, err := c.ProxyPort(); !errors.Is(err, ErrProxyNotRunning) {
			t.Errorf("sin proxy.port = %v, want ErrProxyNotRunning", err)
		}
	})

	t.Run("proxy.port ilegible", func(t *testing.T) {
		for _, content := range []string{"abc", "", "0", "-1", "70000", "1355\n\nbasura"} {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, proxyPortFile), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			c := New(WithBinary("/fake/portless"), WithStateDir(dir))
			if _, err := c.ProxyPort(); !errors.Is(err, ErrProxyNotRunning) {
				t.Errorf("proxy.port=%q dio %v, want ErrProxyNotRunning: degradar es lo seguro", content, err)
			}
		}
	})

	t.Run("el puerto valido se lee", func(t *testing.T) {
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

	t.Run("proxy.port es un directorio", func(t *testing.T) {
		// A read error that is not NotExist must propagate: it is a different cause for whoever debugs it.
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, proxyPortFile), 0o755); err != nil {
			t.Fatal(err)
		}
		c := New(WithBinary("/fake/portless"), WithStateDir(dir))
		_, err := c.ProxyPort()
		if errors.Is(err, ErrProxyNotRunning) {
			t.Error("un proxy.port ilegible se disfrazo de 'no hay proxy'")
		}
		if err == nil {
			t.Error("deberia propagar el error de lectura")
		}
	})
}

// WaitDelay is what bounds wall-clock time: without it a surviving child holding the pipes hangs Wait past the deadline.
func TestExecCommandAcotaConElDeadline(t *testing.T) {
	t.Run("un hijo que ignora la muerte no cuelga Wait", func(t *testing.T) {
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
			t.Fatal("execCommand no volvio: WaitDelay no acota el reloj de verdad")
		}

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want DeadlineExceeded (envuelto)", err)
		}
		if code == 0 {
			t.Error("code = 0 en un proceso que no termino bien")
		}
	})

	t.Run("exito: stdout y codigo 0", func(t *testing.T) {
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

	t.Run("fallo con stderr: el mensaje llega al error", func(t *testing.T) {
		dir := t.TempDir()
		script := filepath.Join(dir, "falla")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'requires Node >= 24' >&2\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		_, code, err := execCommand(context.Background(), script)

		if err == nil {
			t.Fatal("un exit 1 deberia ser error")
		}
		if code != 1 {
			t.Errorf("code = %d, want 1", code)
		}
		if !strings.Contains(err.Error(), "Node") {
			t.Errorf("el error no incluye stderr: %q", err)
		}
	})

	t.Run("fallo sin stderr: el error no queda vacio", func(t *testing.T) {
		dir := t.TempDir()
		script := filepath.Join(dir, "silencioso")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
			t.Fatal(err)
		}

		_, code, err := execCommand(context.Background(), script)

		if err == nil {
			t.Fatal("un exit 3 deberia ser error")
		}
		if code != 3 {
			t.Errorf("code = %d, want 3", code)
		}
		if !strings.Contains(err.Error(), "exit status 3") {
			t.Errorf("sin stderr el error deberia traer el status: %q", err)
		}
	})
}

// A literal path here is the bug of vroom running under a service manager whose env is not the login shell.
func TestMiseShimDirsNoDevuelveLiterales(t *testing.T) {
	t.Run("con HOME, ambos son derivados", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		got := miseShimDirs()

		if len(got) != 2 {
			t.Fatalf("got = %v, want 2 directorios", got)
		}
		for _, dir := range got {
			if !strings.HasPrefix(dir, home) {
				t.Errorf("el shim dir %q no deriva del HOME %q", dir, home)
			}
		}
	})

	t.Run("sin HOME: nil en vez de un path inventado", func(t *testing.T) {
		t.Setenv("HOME", "")
		if got := miseShimDirs(); got != nil {
			t.Errorf("got = %v, want nil: un path de usuario concreto seria un modo de fallo silencioso", got)
		}
	})
}

func wrappedRouteAbsentText() error {
	return fmt.Errorf("retirando la ruta: %s", `Error: No alias found for "svc.localhost".`)
}
