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

// ---------------------------------------------------------------------------
// El cliente (Register, Remove, RemoveAbsent, Lookup, ProxyPort, execCommand) se
// ejercitaba hasta ahora solo a través de Apply, y Apply nunca llega a varias de
// estas ramas: sin binario resuelve antes, y con exito todo sale por el camino
// feliz.
//
// Estos tests van directos al cliente. Cada uno falla por una razon DISTINTA que
// el usuario ve de forma distinta, y confundirlas es lo que hace que una
// retirada que fallo se decLARE cerrada (MEDIUM-C), o que una lectura de estado
// se tome por una confirmacion.
//
// El exec se inyecta con el seam real (WithExec), igual que en portless_test.go:
// un doble que devuelve lo que quiere probaría el doble.
// ---------------------------------------------------------------------------

// newClientFor construye un cliente con el exec inyectado, sin state dir real
// (para lo que no toca disco).
func newClientFor(t *testing.T, exec func(context.Context, string, ...string) (string, int, error)) *Client {
	t.Helper()
	return New(WithBinary("/fake/portless"), WithStateDir(t.TempDir()), WithExec(exec))
}

// TestRegisterSinBinarioFalla: no hay portless, luego no hay ruta. Distinguir
// "no hay binario" de "el binario fallo" importa porque el primero no se arregla
// tocando vroom.
func TestRegisterSinBinarioFalla(t *testing.T) {
	err := New(WithBinary("")).Register("svc", 8080)

	if err == nil {
		t.Fatal("Register sin binario deberia fallar")
	}
	if !strings.Contains(err.Error(), ReasonPortlessMissing) {
		t.Errorf("el error %q no dice que falta el binario", err)
	}
}

// TestRegisterPropagaElErrorDelBinario: si el binario falla, el error tiene que
// llegar al llamador con su mensaje. Apply lo degrada, pero quien llama
// directamente necesita saber que fallo.
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

// TestRegisterConExitDistintoDeCero: el caso medido M8 — upsert incondicional
// que sale 0 y ejecuta con exito. Y el contrario: sale con codigo sin error de
// transporte, que es un fallo que hay que distinguir del exito.
func TestRegisterConExitDistintoDeCero(t *testing.T) {
	t.Run("exit 1 sin error de transporte", func(t *testing.T) {
		// El fake de portless_test devuelve (out, code, err); un binario real
		// sale con 1 y stderr, sin que exec devuelva error.
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
		// Los argumentos importan: es un UPSERT incondicional, asi que el puerto
		// tiene que viajar o se registraria otra ruta.
		joined := strings.Join(sawArgs, " ")
		if !strings.Contains(joined, "svc") || !strings.Contains(joined, "8080") {
			t.Errorf("Register invoco %q, faltan el nombre o el puerto", joined)
		}
	})
}

// TestRemoveEsBenignoPorContrato: Remove NO puede propagar "no existia" porque su
// contrato es "no romper el stop", y un stop repetido no es un error. Es
// deliberadamente distinto de RemoveAbsent, y confundirlos seria reintroducir
// MEDIUM-C.
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
		// code 0 con err es el perfil de un timeout: el deadline mato el proceso
		// y exec no vio un exit propio. Retirar asi podria dejar la ruta viva,
		// asi que Remove lo dice.
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

// TestRemoveAbsentSoloRevocaEnExitoYAusencia: la misma matriz que ya se prueba
// en removeabsent_test.go, pero MIRANDO LAS TRES SALIDAS DE LA FUNCION y no el
// efecto en Release. Los tres casos tienen que ser distinguibles entre si:
// nil (retirada efectiva), ErrRouteAbsent (no estaba) y el fallo (propaga).
//
// Es la funcion que decide si la propiedad se revoca, asi que la distincion
// entre los tres es lo que separa "cerrado" de "fallo abierto disfrazado".
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
		// Estos cuatro son exit 1 y SI son fallo real. El `default -> nil`
		// original los revocaba todos.
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
		// El fallo sin mensaje de exec: si no se construyera un error aqui, el
		// llamador recibiria nil y revocaria sobre un exit 2.
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

// TestIsRouteAbsentNoConfundeLosSimilares: la funcion que decide si un exit 1 es
// el benigno o un fallo. Reconoce por texto, porque no hay mas señal — asi que
// lo que hay que fijar es que NO reconoce lo que no es.
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

// TestLookupSinBinarioYFalloDeLectura: Lookup es la lectura de vuelta que hace
// alcanzable el estado de conflicto. Sus tres fallos significan cosas distintas y
// los tres tienen que ser errores: un "no encontrado" disfrazado de fallo
// haria que Apply creyera que la ruta no esta y escribiera encima de la de otro.
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
		// La regexp solo captura digitos, asi que esta rama necesita una linea
		// que SI casee por el host pero traiga un puerto no numerico. Se
		// construye a mano porque portless real no la emite: el punto es que si
		// la encontrara, no debe inventarse un puerto.
		c := newClientFor(t, func(context.Context, string, ...string) (string, int, error) {
			out := "  http://" + Hostname("svc") + ":1355  ->  localhost:99999999999999999999999  (alias)\n"
			return out, 0, nil
		})
		_, found, err := c.Lookup("svc")
		if found {
			t.Error("found=true con un puerto ilegible: se afirmaria un puerto que no es")
		}
		// Con un puerto de 20 digitos, Atoi falla por desbordamiento: eso es un
		// error real y no un "no encontrado".
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

// TestProxyPort: la ausencia de proxy.port ES la señal de que no hay proxy (M6),
// y por eso devuelve ErrProxyNotRunning en vez de un puerto supuesto. Un
// proxy.port corrupto es indistinguible de un proxy parado a efectos de esta
// decision, y degradar es lo seguro.
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
		// MEDIDO: 4 bytes, sin salto de linea final.
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
		// Un error de lectura que NO es NotExist tiene que propagarse como
		// error de lectura, no disfrazarse de "no hay proxy": son causas
		// distintas para quien depura.
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

// TestExecCommandAcotaConElDeadline: el WaitDelay es lo que hace que el timeout
// acote el tiempo de RELOJ de verdad; sin el, un descendiente vivo con los pipes
// abiertos cuelga Wait() mas alla del deadline, y un arranque se queda colgado.
func TestExecCommandAcotaConElDeadline(t *testing.T) {
	t.Run("un hijo que ignora la muerte no cuelga Wait", func(t *testing.T) {
		// El hijo ignora SIGTERM y sigue vivo. Sin WaitDelay, Wait() no
		// volveria nunca aunque el contexto caducara, porque los pipes siguen
		// abiertos. Este test es el que demuestra que WaitDelay esta.
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
		// El stderr es lo que distingue "Node viejo" de "no existe el binario",
		// y sin el el aviso seria generico.
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

// TestMiseShimDirsNoDevuelveLiterales: los shims se derivan del HOME del proceso
// que corre, nunca de un literal. Un literal seria el bug de "vroom corre bajo un
// gestor de servicios cuyo entorno no es el shell de login".
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

// wrappedRouteAbsentText envuelve el mensaje benigno de M10. El reconocimiento
// de isRouteAbsent es por TEXTO, no por errors.Is, asi que hay que comprobar
// que sobrevive al envoltorio.
func wrappedRouteAbsentText() error {
	return fmt.Errorf("retirando la ruta: %s", `Error: No alias found for "svc.localhost".`)
}
