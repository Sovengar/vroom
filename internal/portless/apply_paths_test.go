package portless

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Los ultimos huecos de Apply y verify, que son los caminos donde la RUTA se
// escribe pero no se puede publicar.
//
// verify es donde se decide si se afirma una URL, y esa afirmacion es lo que el
// usuario copia al navegador. Un "degradado" aqui no es un detalle: es la
// diferencia entre una direccion que funciona y una que no. Por eso estos tests
// distinguen los cuatro finales posibles de verify, que comparten el mismo
// codigo de entrada y solo se separan en lo que se midio del proxy.
// ---------------------------------------------------------------------------

// newHTTPServer levanta un servidor HTTP de verdad en un puerto libre de loopback
// y devuelve el manejador con su puerto. Se usa el httpProbe REAL (no un doble)
// porque lo que estos tests verifican es si verify DISTINGUE 404 de 502, y eso
// depende de leer el status de verdad.
func newHTTPServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// writeFileIn escribe un fichero dentro de dir.
func writeFileIn(t *testing.T, dir, name, content string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// newClientWithProxy construye un cliente con un state dir que declara
// proxyPort, un exec inyectado y la sonda real (httpProbe): se quiere el
// COMPORTAMIENTO de verify, no un doble que lo decida por el test.
func newClientWithProxy(t *testing.T, proxyPort int, exec func(context.Context, string, ...string) (string, int, error)) *Client {
	t.Helper()
	dir := t.TempDir()
	if proxyPort > 0 {
		if err := writeFileIn(t, dir, proxyPortFile, itoa(proxyPort)); err != nil {
			t.Fatal(err)
		}
	}
	return New(
		WithBinary("/fake/portless"),
		WithStateDir(dir),
		WithExec(exec),
		WithTimeout(2*time.Second),
	)
}

// okExec responde como lo haria un portless sano: alias sale 0 y list devuelve
// la tabla con la ruta ya escrita.
func okExec(t *testing.T) func(context.Context, string, ...string) (string, int, error) {
	t.Helper()
	f := newFake()
	return f.exec
}

// TestVerifyPublicaCuandoElProxyEnrutaDeVerdad: el camino feliz de verify con la
// sonda REAL contra un servidor HTTP de verdad. El https falla (no hay TLS), el
// http responde, y lo que se publica es el que respondio.
//
// Importa que el esquema se determine PROBANDO: con solo http en la URL, un
// proxy TLS se declararia inalcanzable cuando si lo es.
// srvPort extrae el puerto de un httptest.Server.
func srvPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("puerto de %q no numerico: %v", srv.URL, err)
	}
	return n
}

func TestVerifyPublicaCuandoElProxyEnrutaDeVerdad(t *testing.T) {
	srv := newHTTPServer(t, http.StatusOK)
	c := newClientWithProxy(t, srvPort(t, srv), okExec(t))

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusRegistered {
		t.Fatalf("Status = %q (motivo %q): con el proxy enrutando deberia publicar. %s", r.Status, r.Reason, srvCalls(r))
	}
	if !strings.HasPrefix(r.Url, "http://") {
		t.Errorf("Url = %q: sin TLS el esquema publicado tiene que ser http", r.Url)
	}
	if !strings.Contains(r.Url, Hostname("svc")) {
		t.Errorf("Url = %q no contiene el hostname de la ruta", r.Url)
	}
}

// TestVerifyNoSondeaConProxyParadoSinProxyPort: si proxy.port no existe, verify
// degrada ANTES de sondear (M6). Sondear sin proxy seria una peticion a un
// puerto supuesto, que es justo la constante que este seam no debe introducir.
func TestVerifyNoSondeaConProxyParado(t *testing.T) {
	f := newFake()
	c := New(
		WithBinary("/fake/portless"),
		WithStateDir(t.TempDir()), // sin proxy.port
		WithExec(f.exec),
		WithProbe(f.probe),
		WithTimeout(2*time.Second),
	)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Reason != ReasonProxyNotRunning {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonProxyNotRunning)
	}
	if r.Url != "" {
		t.Errorf("Url = %q sin proxy en marcha", r.Url)
	}
	if !r.Registered {
		t.Error("Registered se perdio: el alta ocurrio igual (M1)")
	}
	for _, call := range f.calls {
		if strings.HasPrefix(call, "probe:") {
			t.Errorf("se sondeo sin saber que hay proxy: %v", f.calls)
			break
		}
	}
}

// TestVerifyDegradaSinPublicarURLCuandoElProxyNoSirveLaRuta: el proxy responde
// 404 al host. Es lo MEDIDO: 404 significa "no conozco este host", que es lo
// CONTRARIO de enrutado. Publicar la URL ahí afirmaria una direccion que el proxy
// no sirve, que es justo el fallo que este diseno existe para evitar.
func TestVerifyDegradaSinPublicarURLCuandoElProxyNoSirveLaRuta(t *testing.T) {
	srv := newHTTPServer(t, http.StatusNotFound)
	c := newClientWithProxy(t, srvPort(t, srv), okExec(t))

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado con un proxy que responde 404", r.Status)
	}
	if r.Reason != ReasonRouteNotServed {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonRouteNotServed)
	}
	if r.Url != "" {
		t.Errorf("Url = %q: 404 prueba que el proxy NO enruta la ruta", r.Url)
	}
}

// TestVerifyAceptaEl502ComoPruebaDeEnrutado: el caso inverso y el que mas
// confunde. Un 502 dice "el proxy ENRUTA la ruta y el servicio de detras no
// responde". Eso PRUEBA el enrutado, y el servicio puede estar arrancando. Un
// 502 no es un fallo de la ruta: es informacion de que la ruta existe.
func TestVerifyAceptaEl502ComoPruebaDeEnrutado(t *testing.T) {
	srv := newHTTPServer(t, http.StatusBadGateway)
	c := newClientWithProxy(t, srvPort(t, srv), okExec(t))

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusRegistered {
		t.Errorf("Status = %q (motivo %q): un 502 prueba que el proxy enruta. %s", r.Status, r.Reason, srvCalls(r))
	}
	if r.Url == "" {
		t.Error("un 502 no debe impedir publicar la URL: la ruta esta enrutada")
	}
}

// TestVerifyDegradaCuandoElPuertoDeclaradoNoAceptaConexiones: proxy.port
// declara un puerto donde no hay nadie. verify distingue ese caso del de "el
// proxy responde y no conoce la ruta", y degradan con motivos distintos: el
// primero es un puerto muerto, el segundo una ruta que no existe.
func TestVerifyDegradaCuandoElPuertoDeclaradoNoAceptaConexiones(t *testing.T) {
	// Puerto que se abre y se cierra: nadie escucha.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	f := newFake()
	f.noProxy = true // ninguna sonda recibe respuesta
	c := newClientWithProxy(t, deadPort, f.exec)

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", r.Status)
	}
	if r.Reason != ReasonProxyUnreachable {
		t.Errorf("Reason = %q, want %q: el puerto declarado no acepta conexiones", r.Reason, ReasonProxyUnreachable)
	}
	if !r.Registered {
		t.Error("Registered se perdio: la escritura ocurrio antes de sondear")
	}
}

// TestVerifyDistingueProxyVivoQueNoSirveDePuertoMuerto: los dos casos comparten
// el mismo "ningun esquema respondio" y se separan SOLO por acceptsConnections.
// Si esa comprobacion se quitara, ambos degradarian con el mismo motivo y el
// usuario perderia la pista de si portless esta parado o la ruta no existe.
func TestVerifyDistingueProxyVivoQueNoSirveDePuertoMuerto(t *testing.T) {
	// Proxy vivo que responde 404 a todo: acceptsConnections da true, asi que
	// el motivo es "no sirve la ruta", no "puerto muerto".
	srv := newHTTPServer(t, http.StatusNotFound)

	f := newFake()
	f.serve404[Hostname("svc")] = true // el proxy no enruta este host
	c := newClientWithProxy(t, srvPort(t, srv), f.exec)

	r := c.Apply("svc", 8080, Ownership{})

	// Con 404 la ruta no se enruta, y por eso el motivo es RouteNotServed: no
	// hace falta llegar a acceptsConnections, que solo se consulta cuando NINGUN
	// esquema respondio.
	if r.Reason != ReasonRouteNotServed {
		t.Errorf("Reason = %q, want %q", r.Reason, ReasonRouteNotServed)
	}
	if r.Url != "" {
		t.Errorf("Url = %q sin enrutado probado", r.Url)
	}
}

// TestApplyReportaConflictoCuandoElNombreLoTieneOtroPuerto: el estado que hace
// alcanzable M8 en silencio. Dos vrooms, o un vroom y otra app, con el mismo
// nombre en puertos distintos: escribir encima seria perder la ruta del otro.
//
// La condicion es que prev NO autorice ese puerto: si lo autorizara, es que la
// ruta es nuestra y se puede reescribir (M1).
func TestApplyReportaConflictoCuandoElNombreLoTieneOtroPuerto(t *testing.T) {
	t.Run("sin autorizacion previa: conflicto y no se escribe", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		// Otro dueño ya tiene el nombre en el puerto 9999.
		if err := c.Register("svc", 9999); err != nil {
			t.Fatal(err)
		}
		before := f.routes[Hostname("svc")]

		r := c.Apply("svc", 8080, Ownership{})

		if r.Reason != ReasonRouteConflict {
			t.Errorf("Reason = %q, want %q", r.Reason, ReasonRouteConflict)
		}
		if r.Registered {
			t.Error("Registered=true en un conflicto: se afirmaria una escritura que no ocurrio")
		}
		if got := f.routes[Hostname("svc")]; got != before {
			t.Errorf("el puerto ajeno cambio de %d a %d: no se debe tocar la ruta de otro", before, got)
		}
	})

	t.Run("con autorizacion previa del mismo puerto: se reescribe", func(t *testing.T) {
		// prev.Owned con prev.Port == 8080: es nuestra, se puede reaffirmar. Sin
		// esta rama, un reinicio legitimo del servicio se declararia conflicto
		// consigo mismo.
		f := newFake()
		c := f.client(t)
		if err := c.Register("svc", 8080); err != nil {
			t.Fatal(err)
		}

		r := c.Apply("svc", 8080, Ownership{Owned: true, Port: 8080})

		if r.Reason == ReasonRouteConflict {
			t.Error("una ruta que ya es nuestra no es un conflicto consigo misma")
		}
		if !r.Registered {
			t.Errorf("Reason = %q y no se publico: %v", r.Reason, srvCalls(r))
		}
	})
}

// TestApplyNoEscribeSiNoSePuedeLeerElEstadoPrevio: la consulta PREVIA es la que
// decide si el alta es legitima. Si no se puede ni leer, escribir seria
// exactamente el dano que M8 permite: tomar el nombre de otro sin saberlo.
//
// La propiedad de esto es la que se prueba: el estado del otro queda intacto.
func TestApplyNoEscribeSiNoSePuedeLeerElEstadoPrevio(t *testing.T) {
	f := newFake()
	c := f.client(t)
	// El otro dueño tiene el nombre.
	if err := c.Register("svc", 9999); err != nil {
		t.Fatal(err)
	}
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			return "", 0, errors.New("portless list: routes.json unreadable")
		}
		return f.exec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Registered {
		t.Error("Registered=true: se escribio sin poder leer contra que")
	}
	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", r.Status)
	}
	if got := f.routes[Hostname("svc")]; got != 9999 {
		t.Errorf("el puerto ajeno quedo en %d, want 9999 sin tocar: no se debia escribir", got)
	}
}

// TestApplyDetectaElCambioDeDueñoEntreLaConsultaYElAlta: M8 en vivo. La consulta
// previa ve un puerto libre, y entre medias OTRO actor toma el nombre. La lectura
// de vuelta lo ve distinto, y es la unica que lo detecta.
//
// Es la ventana que obliga a la lectura de vuelta: consultar despues de
// escribir seria una tautologia.
// TestApplyDegradaSiLaLecturaDeVueltaNoLaVe: el alta sale 0 pero la ruta no
// aparece en la tabla. Es un portless que acepto la escritura y no la publico:
// M8 otra vez, y la lectura de vuelta es lo unico que lo detecta.
//
// No se puede con el fake fiel, porque su list SI ve lo que su alias escribio
// —que es la conducta medida. Se fuerza con un exec que escribe pero lista otra
// cosa, que es exactamente el fallo real que se quiere cazar.
func TestApplyDegradaSiLaLecturaDeVueltaNoLaVe(t *testing.T) {
	f := newFake()
	c := f.client(t)
	realExec := f.exec
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			return "\nActive routes:\n\n", 0, nil // libre antes y despues del alta
		}
		return realExec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Reason != ReasonRouteNotServed {
		t.Errorf("Reason = %q, want %q: el alta salio bien pero la ruta no aparece", r.Reason, ReasonRouteNotServed)
	}
	if r.Url != "" {
		t.Errorf("Url = %q: no se puede publicar una ruta que no se ve", r.Url)
	}
	if !r.Registered {
		t.Error("Registered se perdio: el alias salio 0, luego la escritura ocurrio")
	}
}

func TestApplyDetectaElCambioDeDueñoEntreLaConsultaYElAlta(t *testing.T) {
	f := newFake()
	c := f.client(t)
	realExec := f.exec
	calls := 0
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "list" {
			calls++
			if calls == 1 {
				// Consulta previa: el nombre esta libre.
				return "\nActive routes:\n\n", 0, nil
			}
			// Lectura de vuelta: otro actor lo tomo.
			host := Hostname("svc")
			return "  http://" + host + ":1355  ->  localhost:9999  (alias)\n", 0, nil
		}
		return realExec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Reason != ReasonRouteConflict {
		t.Errorf("Reason = %q, want %q: el nombre cambio de dueño entre medias", r.Reason, ReasonRouteConflict)
	}
	if r.Url != "" {
		t.Errorf("Url = %q con un conflicto sin resolver", r.Url)
	}
	// El hecho se conserva: la escritura SI ocurrio, y sin ese dato la
	// reconciliacion no sabria que limpiar.
	if !r.Registered {
		t.Error("Registered se perdio: el alta ocurrio (upsert incondicional, M8)")
	}
}

// TestApplyPropagaElFalloDelAltaSinAfirmarNada: Register falla, y no hay
// escritura que conservar. Degradar aqui tiene que ser honesto en las dos
// direcciones: Registered false, porque no se escribio.
func TestApplyPropagaElFalloDelAltaSinAfirmarNada(t *testing.T) {
	f := newFake()
	c := f.client(t)
	realExec := f.exec
	// list pasa (el nombre esta libre) y alias es lo que falla: si el exec
	// fallara en todo, el fallo seria de la consulta previa y no se llegaria al
	// alta, y este test no probaria lo que dice probar.
	c.exec = func(ctx context.Context, bin string, args ...string) (string, int, error) {
		if args[0] == "alias" {
			return "", 1, errors.New("Error: requires Node >= 24")
		}
		return realExec(ctx, bin, args...)
	}

	r := c.Apply("svc", 8080, Ownership{})

	if r.Status != StatusDegraded {
		t.Errorf("Status = %q, want degradado", r.Status)
	}
	if r.Registered {
		t.Error("Registered=true con el alta fallida: se afirmaria una escritura que no ocurrio")
	}
	if r.Reason == "" {
		t.Error("Reason vacio tras un fallo del alta: el usuario no sabria que paso")
	}
	if r.Url != "" {
		t.Errorf("Url = %q con el alta fallida", r.Url)
	}
}

// TestReleaseConReleaserNilConstruyeElClienteReal: r nil significa "construye el
// cliente real", que es lo que usan los caminos de stop en produccion. La rama
// tiene que existir para que esos caminos no hagan panic, y no se puede ejercitar
// contra portless real, asi que se verifica con un entorno sin binario: es lo
// que un runner de CI ve, y tiene que devolver sin tocar nada.
func TestReleaseConReleaserNilConstruyeElClienteReal(t *testing.T) {
	// Sin portless resoluble: la rama construye un cliente degradado y RemoveAbsent
	// sale bien sin haber invocado nada.
	t.Setenv("PORTLESS_BIN", "")
	t.Setenv("PATH", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PORTLESS_STATE_DIR", t.TempDir())

	// Con nombre vacio no llega a construir nada: sale por el guard de antes.
	if !Release(nil, "") {
		t.Error("nombre vacio debe revocar sin construir cliente")
	}

	// Con nombre y sin portless, RemoveAbsent del cliente real dice nil (no hay
	// ruta que retirar), luego revoca. Lo que se verifica es que NO revienta.
	got := Release(nil, "mi-ruta")
	if !got {
		t.Log("sin portless no se puede afirmar nada sobre la ruta: no revoca, y el handle se conserva para reconciliar")
	}
}

// srvCalls devuelve una descripcion de un Result para el mensaje de error: los
// tests de este archivo fallan con el motivo, no con un "want registered".
func srvCalls(r Result) string {
	return "motivo=" + r.Reason + " url=" + r.Url
}
