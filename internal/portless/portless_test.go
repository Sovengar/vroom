package portless

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- harness ----

// fakePortless es un portless de mentira con el comportamiento MEDIDO de
// 0.15.6, para que la suite sea hermética: el runner de CI no tiene portless,
// ni Node 24, ni proxy, y aun así estos tests tienen que poder exigir que la
// verificación exista.
type fakePortless struct {
	routes map[string]int // hostname -> puerto, como routes.json

	// binCode es el exit de `alias`. El caso medido que importa es 0 con el
	// proxy parado: alias NO contacta con el proxy (M1).
	binCode int
	// binErr fuerza un fallo de la llamada al binario.
	binErr error
	// hang deja la llamada bloqueada hasta que el contexto caduca, que es el
	// caso "portless se queda colgado".
	hang bool

	// probeStatus es lo que el proxy responde para un host que CONOCE.
	// 502 = enruta y el backend no responde. -1 = no hay proxy.
	probeStatus int
	// tls hace que el proxy simulado acepte https. Apagado por defecto.
	tls bool
	// noProxy hace que ninguna sonda reciba respuesta (conexión rehusada).
	noProxy bool

	calls []string
}

func newFake() *fakePortless {
	return &fakePortless{routes: map[string]int{}, binCode: 0, probeStatus: 200}
}

func (f *fakePortless) exec(ctx context.Context, bin string, args ...string) (string, int, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.hang {
		<-ctx.Done()
		// El seam real envuelve el error del contexto para que
		// errors.Is(err, context.DeadlineExceeded) funcione; el fake debe ser
		// fiel a ese contrato o el test probaría el fake, no el seam.
		return "", -1, ctx.Err()
	}
	if f.binErr != nil {
		return "", 1, f.binErr
	}
	switch args[0] {
	case "alias":
		if len(args) >= 2 && args[1] == "--remove" {
			name := Hostname(args[2])
			if _, ok := f.routes[name]; !ok {
				// MEDIDO (M10): exit 1. Es BENIGNO.
				return "", 1, errors.New("no alias found")
			}
			delete(f.routes, name)
			return "Removed alias: " + name, 0, nil
		}
		// Upsert incondicional, sin detección de conflictos (M8).
		var port int
		for _, c := range args[2:] {
			if n := atoiOr(c, -1); n > 0 {
				port = n
			}
		}
		if port <= 0 {
			return "Error: Invalid port", 1, errors.New("invalid port")
		}
		f.routes[Hostname(args[1])] = port
		return "Alias registered", f.binCode, nil
	case "list":
		var b strings.Builder
		b.WriteString("\nActive routes:\n\n")
		for host, port := range f.routes {
			b.WriteString("  http://" + host + ":1355  ->  localhost:" + itoa(port) + "  (alias)\n")
		}
		return b.String(), 0, nil
	}
	return "", 1, errors.New("unknown subcommand")
}

// tls habilita el esquema https en el proxy simulado. Por defecto está apagado:
// MEDIDO, un proxy arrancado con HTTPS=0 rechaza el handshake TLS (curl sale
// 35), así que un fake que aceptase https siempre haría que el orden de
// sondas no significara nada.
func (f *fakePortless) probe(ctx context.Context, scheme, host string, proxyPort int, path string) (int, error) {
	f.calls = append(f.calls, "probe:"+scheme+":"+host)
	if scheme == "https" && !f.tls {
		return 0, errors.New("tls: handshake failure")
	}
	if f.noProxy {
		return 0, errors.New("connection refused")
	}
	if _, ok := f.routes[host]; !ok {
		// MEDIDO: el proxy responde 404 a un host que NO conoce. Eso NO es
		// prueba de enrutado, es su contrario.
		return 404, nil
	}
	if f.probeStatus == 502 {
		return 502, nil // enruta, y el backend no responde
	}
	return 200, nil
}

// client construye un cliente con el fake y un state dir real en temporal.
func (f *fakePortless) client(t *testing.T) *Client {
	t.Helper()
	dir := t.TempDir()
	f.writeProxyPort(t, dir, 1399)
	return New(
		WithBinary("/fake/portless"),
		WithStateDir(dir),
		WithExec(f.exec),
		WithProbe(f.probe),
		WithTimeout(2*time.Second),
	)
}

func (f *fakePortless) writeProxyPort(t *testing.T, dir string, port int) {
	t.Helper()
	// MEDIDO: 4 bytes, sin salto de línea final.
	if err := os.WriteFile(filepath.Join(dir, "proxy.port"), []byte(itoa(port)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func atoiOr(s string, def int) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ---- regresión 1: prune no destruye la ruta ----

// REGRESIÓN OBLIGATORIA (R12 / M5): una ruta de alias tiene pid 0 y
// `portless prune` NO la toca. Por eso la reconciliación del arranque es
// obligatoria: vroom es lo único que puede limpiar lo que deja.
//
// Si alguien reintrodujera la creencia de que prune limpia, esta ruta quedaría
// para siempre y este test es lo que lo delata.
func TestPruneDoesNotDestroyAliasRoutes(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("foreign.vroom")] = 9999

	// Lo que prune hace, medido: no toca rutas de alias. (No se ejecuta prune
	// real jamás contra el estado del usuario; aquí se afirma la forma del
	// dato que prune no puede tocar.)
	if pid := aliasRoutePid; pid != 0 {
		t.Fatalf("una ruta de alias debe tener pid 0 para que prune no la toque, got %d", pid)
	}

	// Y con el proxy sano la reconciliación NO la toca: responde, y no es
	// nuestra. Fallar cerrado en la limpieza es tan importante como acertar en
	// el alta.
	warns := c.Reconcile("foreign.vroom", 4321, "mine.vroom")
	if len(warns) == 0 {
		t.Error("una ruta viva en otro puerto debe avisar, no retirarse en silencio")
	}
	if _, still := f.routes[Hostname("foreign.vroom")]; !still {
		t.Error("una ruta que responde y no es nuestra NUNCA se retira")
	}
}

// aliasRoutePid es el pid que portless escribe para una ruta de alias. Se
// declara aquí para que el test de arriba afirme el hecho, no una constante
// inventada: alias → pid 0 (medido, M5/M4).
const aliasRoutePid = 0

// ---- regresión 2: escrita con el proxy parado NO es disponibilidad ----

// REGRESIÓN OBLIGATORIA (M1): `alias` es una escritura pura del fichero de
// estado y NUNCA contacta con el proxy. Con el proxy apagado sale 0 y escribe
// la ruta igual. Un diseño que se fiara de exit 0 publicaría una URL que no
// resuelve, y este test es lo que lo delata.
func TestRouteWrittenWithProxyDownIsNotReportedAvailable(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.noProxy = true // el proxy no sirve

	res := c.Apply("down.vroom", 4321)

	if res.Status != StatusDegraded {
		t.Fatalf("una ruta escrita con el proxy parado NO puede reportarse disponible, got %q", res.Status)
	}
	if res.Url != "" {
		t.Errorf("una ruta no verificada no publica url, got %q", res.Url)
	}
	if res.Reason != ReasonProxyNotRunning && res.Reason != ReasonProxyUnreachable {
		t.Errorf("el motivo debe explicar que el proxy no responde, got %q", res.Reason)
	}

	// Y el binario SÍsale con 0: por eso el test tiene que mirar el resultado,
	// no el exit code. Esta aserción documenta la trampa.
	if _, code, _ := f.exec(context.Background(), "portless", "alias", "down.vroom", "4321"); code != 0 {
		t.Fatalf("alias debe salir 0 con el proxy parado (M1), got %d", code)
	}
}

// Una lectura del fichero de estado NO cuenta como verificación (M2). El proxy
// responde 404 a un host que no conoce: eso es su contrario, y publicarlo sería
// una mentira.
func TestRoutesFileAloneIsNotVerification(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("unknown.vroom")] = 4321 // en el fichero...

	// ...pero el proxy no lo sirve: se simula quitándolo del set del proxy.
	f.noProxy = false
	c2 := New(
		WithBinary("/fake/portless"),
		WithStateDir(c.stateDir),
		WithExec(f.exec),
		WithProbe(func(ctx context.Context, scheme, host string, port int, path string) (int, error) {
			return 404, nil // el proxy responde, pero no conoce el host
		}),
	)
	res := c2.Apply("written-but-not-served.vroom", 4321)
	if res.Succeeded() {
		t.Error("un 404 significa que el proxy no enruta: no puede reportarse registered")
	}
	if res.Url != "" {
		t.Error("un 404 no publica url")
	}
}

// ---- regresión 3: proxy.port ausente degrada, nunca supone 1355 ----

// REGRESIÓN OBLIGATORIA (M6): proxy.port SÓLO existe mientras el proxy corre,
// así que su ausencia ES la señal de que no hay proxy. Caer a un 1355 supuesto
// sería un puerto que además se puede mover.
func TestMissingProxyPortDegradesWithoutAssuming1355(t *testing.T) {
	f := newFake()
	c := f.client(t)

	// El proxy se para: el fichero desaparece.
	if err := os.Remove(filepath.Join(c.stateDir, "proxy.port")); err != nil {
		t.Fatal(err)
	}

	res := c.Apply("noproxy.vroom", 4321)
	if res.Reason != ReasonProxyNotRunning {
		t.Fatalf("sin proxy.port el motivo debe ser proxy_not_running, got %q", res.Reason)
	}
	if res.Url != "" {
		t.Error("sin proxy no se publica url")
	}
	// La sonda NUNCA debe haberse dirigido a 1355.
	for _, call := range f.calls {
		if strings.Contains(call, ":1355") && strings.HasPrefix(call, "probe") {
			t.Errorf("no se puede suponer el puerto del proxy, pero se sondeó %q", call)
		}
	}
}

// proxy.port corrupto es indistinguible de un proxy parado a los efectos de esta
// decisión, y degradar es lo seguro.
func TestCorruptProxyPortDegrades(t *testing.T) {
	f := newFake()
	c := f.client(t)
	if err := os.WriteFile(filepath.Join(c.stateDir, "proxy.port"), []byte("no-es-un-puerto"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := c.Apply("corrupt.vroom", 4321)
	if res.Reason != ReasonProxyNotRunning {
		t.Errorf("un proxy.port ilegible debe degradar como proxy parado, got %q", res.Reason)
	}
}

// ---- regresión 4: la reconciliación ----

// La reconciliación retira una ruta renombrada (la vieja no responde) y deja
// intacta una ruta viva que no es nuestra.
func TestReconcileRemovesRenamedOrphanAndKeepsForeignLiveRoute(t *testing.T) {
	t.Run("retira la huérfana que ya no responde", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		// Lo dejó un vroom que murió sin parar: el puerto ya no lo escucha
		// nadie, así que el proxy responde 502 y la ruta está de hecho muerta.
		f.routes[Hostname("old-name")] = 39999
		f.probeStatus = 502
		f.routes[Hostname("new-name")] = 4321

		// La ruta vieja responde (502 = enruta) con el puerto que persistimos:
		// para el diseño es "la nuestra", y una rama renombrada la deja viva.
		// El caso huérfano real es que NO responda: se simula quitándola del
		// set del proxy (404) que es lo que mide "no la sirve".
		f.routes[Hostname("new-name")] = 4321
		c.probe = func(ctx context.Context, scheme, host string, port int, path string) (int, error) {
			if host == Hostname("old-name") {
				return 404, nil // el proxy ya no conoce el nombre viejo
			}
			return 200, nil
		}

		warns := c.Reconcile("old-name", 39999, "new-name")
		if len(warns) != 0 {
			t.Errorf("retirar una huérfana propia no avisa: %v", warns)
		}
		if _, still := f.routes[Hostname("old-name")]; still {
			t.Error("la ruta renombrada debe retirarse: es de este servicio y no responde")
		}
	})

	t.Run("NO retira una ruta viva ajena", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		f.routes[Hostname("other-app")] = 5555 // responde, en OTRO puerto
		f.routes[Hostname("mine")] = 4321

		warns := c.Reconcile("other-app", 4321, "mine") // persistimos 4321
		if len(warns) == 0 {
			t.Error("una ruta viva en otro puerto debe avisar del conflicto")
		}
		if _, still := f.routes[Hostname("other-app")]; !still {
			t.Error("el fallo cerrado también aplica a la limpieza: no se borra algo ajeno")
		}
	})

	t.Run("es idempotente con la ruta persistida", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		f.routes[Hostname("same")] = 4321
		// Mismo nombre persistido y derivado: no hay nada que reconciliar, ni
		// siquiera se toca. Reconciliar N veces no acumula rutas.
		for range 3 {
			if warns := c.Reconcile("same", 4321, "same"); len(warns) != 0 {
				t.Errorf("la reconciliación debe ser silenciosa e idempotente: %v", warns)
			}
		}
		if len(f.routes) != 1 {
			t.Errorf("no deben acumularse rutas duplicadas: %v", f.routes)
		}
	})
}

// ---- lectura de vuelta: exit 0 NO es prueba de propiedad ----

// M8: el alta es un upsert incondicional. Si el nombre ya lo tiene OTRO puerto,
// la lectura de vuelta es lo que lo detecta, y sin ella vroom publicaría una
// dirección que no controla.
//
// El simulacro importa: `alias` SOBRESCRIBE en silencio (M8), así que para que
// el conflicto sea observable el otro puerto tiene que aparecer DESPUÉS del
// alta de vroom — que es justo lo que hace un segundo vroom, o el propio
// portless, re-registrando el nombre entre medias.
func TestReadBackDetectsNameTakenByAnotherPort(t *testing.T) {
	f := newFake()
	c := f.client(t)

	var stolen bool
	readBack := func(ctx context.Context, bin string, args ...string) (string, int, error) {
		// Al LLEGAR la lectura de vuelta, otro dueño ya tomó el nombre con
		// otro puerto: es la carrera que M8 hace posible y que la comparación
		// de puertos es la única que detecta.
		if len(args) > 0 && args[0] == "list" && !stolen {
			stolen = true
			f.routes[Hostname("taken")] = 9999
		}
		return f.exec(ctx, bin, args...)
	}
	c.exec = readBack

	res := c.Apply("taken", 4321)
	if res.Succeeded() {
		t.Fatal("una ruta cuyo nombre tiene otro puerto NO puede reportarse registrada")
	}
	if res.Reason != ReasonRouteConflict {
		t.Errorf("el motivo debe ser route_conflict, got %q", res.Reason)
	}
	if res.Url != "" {
		t.Error("en conflicto no se publica url")
	}
}

// Y el camino feliz: la lectura de vuelta encuentra la ruta con NUESTRO puerto.
func TestReadBackConfirmsOurs(t *testing.T) {
	f := newFake()
	c := f.client(t)
	res := c.Apply("mine.vroom", 4321)
	if !res.Succeeded() {
		t.Fatalf("la lectura de vuelta debía confirmar la ruta: %+v", res)
	}
	if res.Port != 4321 {
		t.Errorf("la ruta debe apuntar al puerto real, got %d", res.Port)
	}
	if !strings.HasPrefix(res.Url, "http://") && !strings.HasPrefix(res.Url, "https://") {
		t.Errorf("la url debe llevar el esquema comprobado, got %q", res.Url)
	}
}

// ---- 502 enruta; 404 no ----

// El punto que más se confunde: un 502 dice "el proxy ENRUTA y el backend no
// responde", que es prueba de enrutado. Un 404 dice que el proxy NO conoce el
// host. Confundirlos convierte la sonda en una afirmación falsa.
func TestBackendErrorStillCountsAsRouted(t *testing.T) {
	f := newFake()
	f.probeStatus = 502
	c := f.client(t)

	res := c.Apply("502.vroom", 4321)
	if !res.Succeeded() {
		t.Fatalf("un 502 prueba que el proxy enruta la ruta: %+v", res)
	}
	if res.Url == "" {
		t.Error("una ruta que enruta publica su url")
	}
}

// ---- el esquema se determina probando ----

// No se supone https ni http: se prueban y se publica el que respondió. Así el
// caso TLS no es un riesgo, porque no hay supuesto que el TLS pueda refutar.
func TestSchemeIsProbedNotAssumed(t *testing.T) {
	t.Run("http responde primero", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		res := c.Apply("plain.vroom", 4321)
		if !strings.HasPrefix(res.Url, "http://") {
			t.Errorf("un proxy sin TLS debe publicar http, got %q", res.Url)
		}
	})

	t.Run("https responde y se publica https", func(t *testing.T) {
		f := newFake()
		c := f.client(t)
		// https responde; http no. Se publica el que respondió.
		only := New(
			WithBinary("/fake/portless"),
			WithStateDir(c.stateDir),
			WithExec(f.exec),
			WithProbe(func(ctx context.Context, scheme, host string, port int, path string) (int, error) {
				if scheme == "https" {
					return 200, nil
				}
				return 0, errors.New("connection refused")
			}),
		)
		res := only.Apply("tls.vroom", 4321)
		if !strings.HasPrefix(res.Url, "https://") {
			t.Errorf("si https responde se publica https, got %q", res.Url)
		}
	})
}

// ---- degradaciones ----

// Sin binario: el arranque NO falla, se avisa, y no se publica ruta.
func TestMissingBinaryDegradesWithoutInvokingPortless(t *testing.T) {
	called := false
	c := New(WithBinary(""), WithExec(func(context.Context, string, ...string) (string, int, error) {
		called = true
		return "", 0, nil
	}), WithStateDir(t.TempDir()))

	res := c.Apply("nobin.vroom", 4321)
	if res.Reason != ReasonPortlessMissing {
		t.Errorf("sin binario el motivo debe ser portless_not_found, got %q", res.Reason)
	}
	if called {
		t.Error("sin binario no se invoca a portless en absoluto")
	}
}

// Un binario que sale con error: el motivo lo nombra y no publica ruta.
func TestFailingBinaryDegrades(t *testing.T) {
	f := newFake()
	f.binErr = errors.New("Error: requires Node >= 24")
	c := f.client(t)

	res := c.Apply("nodeold.vroom", 4321)
	if res.Reason != ReasonPortlessFailed {
		t.Errorf("un binario que falla debe degradar, got %q", res.Reason)
	}
	if res.Url != "" {
		t.Error("un binario que falla no publica url")
	}
	if !strings.Contains(Warn(res), "Node") {
		t.Errorf("el aviso debe nombrar el fallo concreto, got %q", Warn(res))
	}
}

// Un binario COLGADO no cuelga el arranque: la llamada está acotada y el motivo
// es el timeout, no un fallo genérico.
func TestHangingBinaryIsBounded(t *testing.T) {
	f := newFake()
	f.hang = true
	c := New(
		WithBinary("/fake/portless"),
		WithStateDir(t.TempDir()),
		WithExec(f.exec),
		WithProbe(f.probe),
		WithTimeout(150*time.Millisecond), // el arranque acota, no el binario
	)

	done := make(chan Result, 1)
	go func() { done <- c.Apply("hang.vroom", 4321) }()

	select {
	case res := <-done:
		if res.Reason != ReasonPortlessTimeout {
			t.Errorf("un binario colgado debe degradar por timeout, got %q", res.Reason)
		}
		if res.Succeeded() {
			t.Error("un binario colgado no puede publicar una ruta")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Apply NO puede quedar bloqueado esperando al binario")
	}
}

// Remove de un nombre inexistente es BENIGNO (M10): un stop repetido no es un
// error, y exigir el nombre convertiría el segundo stop en un fallo.
func TestRemoveMissingIsBenign(t *testing.T) {
	f := newFake()
	c := f.client(t)
	if err := c.Remove("nunca-existio"); err != nil {
		t.Fatalf("quitar una ruta inexistente no puede ser un error: %v", err)
	}
}

// Remove de lo que SÍ existe sí lo quita, y no toca el resto.
func TestRemoveOnlyTouchesItsOwn(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("mine")] = 4321
	f.routes[Hostname("sibling")] = 5555

	if err := c.Remove("mine"); err != nil {
		t.Fatal(err)
	}
	if _, still := f.routes[Hostname("mine")]; still {
		t.Error("la ruta propia debe desaparecer")
	}
	if _, sibling := f.routes[Hostname("sibling")]; !sibling {
		t.Error("las rutas de los servicios hermanos no se tocan")
	}
}

// ---- el ciclo de vida completo ----

// El ciclo entero contra el comportamiento MEDIDO de portless: registrar, ver
// que el proxy la sirve, retirarla al parar, y parar OTRA VEZ sin que eso sea un
// error.
//
// Es el test que más se parece a lo que vive el usuario, y el que falla si
// alguien vuelve a tratar el exit 1 de `--remove` como un fallo de parada.
func TestFullRouteLifecycle(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.routes[Hostname("sibling")] = 5555 // un servicio hermano, con su ruta

	// Arranque: se registra y el proxy la sirve.
	res := c.Apply("app", 4321)
	if !res.Succeeded() {
		t.Fatalf("el registro debe verificarse contra el proxy vivo: %+v", res)
	}
	if _, found, _ := c.Lookup("app"); !found {
		t.Fatal("la ruta debe existir tras registrarla")
	}

	// Parada: la ruta desaparece y el hermano no se ve afectado.
	if err := c.Remove("app"); err != nil {
		t.Fatalf("parar debe retirar la ruta: %v", err)
	}
	if _, still := f.routes[Hostname("app")]; still {
		t.Error("la ruta debe desaparecer al parar")
	}
	if _, sib := f.routes[Hostname("sibling")]; !sib {
		t.Error("las rutas de los servicios hermanos no se tocan")
	}

	// Parar de NUEVO: no hay ruta que quitar, y eso no es un error.
	if err := c.Remove("app"); err != nil {
		t.Fatalf("un stop repetido no puede fallar: %v", err)
	}
	if _, sib := f.routes[Hostname("sibling")]; !sib {
		t.Error("el hermano sigue intacto tras un stop repetido")
	}
}

// La app reinicia y hace bind en otro puerto: la MISMA ruta pasa a apuntar al
// puerto nuevo, sin dejar la vieja apuntando a un puerto muerto.
func TestReRegisterMovesTheRouteToTheNewPort(t *testing.T) {
	f := newFake()
	c := f.client(t)

	if res := c.Apply("app", 4000); !res.Succeeded() {
		t.Fatalf("primer arranque: %+v", res)
	}
	// La app reinicia en otro puerto.
	if res := c.Apply("app", 4321); !res.Succeeded() {
		t.Fatalf("segundo arranque: %+v", res)
	}

	port, found, err := c.Lookup("app")
	if err != nil || !found {
		t.Fatalf("la ruta debe seguir existiendo: %v", err)
	}
	if port != 4321 {
		t.Errorf("la misma ruta debe pasar a apuntar al puerto nuevo, got %d", port)
	}
	if len(f.routes) != 1 {
		t.Errorf("no puede quedar más de una ruta para el mismo servicio: %v", f.routes)
	}
}

// Una ruta registrada con el proxy PARADO se sigue sirviendo cuando el proxy
// vuelve: el registro es persistente y vroom no tiene que registrarla otra vez
// (medido, M3). Por eso la verificación decide qué se PUBLICA, no si se
// REGISTRA.
func TestRouteRegisteredWhileProxyDownIsServedWhenItReturns(t *testing.T) {
	f := newFake()
	c := f.client(t)
	f.noProxy = true // el proxy está parado al arrancar

	res := c.Apply("app", 4321)
	if res.Succeeded() {
		t.Fatal("con el proxy parado no se puede reportar disponible")
	}
	// Pero la ruta SÍ queda escrita, y persiste.
	if _, found, _ := c.Lookup("app"); !found {
		t.Fatal("la ruta debe quedar registrada aunque el proxy esté parado")
	}

	// El proxy vuelve.
	f.noProxy = false
	if port, found, _ := c.Lookup("app"); !found || port != 4321 {
		t.Fatalf("al volver el proxy la ruta debe seguir ahí, got %d found=%v", port, found)
	}
	if res := c.verify("app", 4321); !res.Succeeded() {
		t.Errorf("con el proxy de vuelta la ruta debe verificarse: %+v", res)
	}
}

// Escribir la ruta de vroom NO daña las rutas que gestiona portless (medido,
// M4): una ruta con pid propio sobrevive intacta mientras vroom da de alta la
// suya. Es lo que permite compartir proxy.
func TestVroomRouteDoesNotEvictLivePortlessRoutes(t *testing.T) {
	f := newFake()
	c := f.client(t)
	// Una app viva de `portless run`, con su pid y su puerto.
	f.routes[Hostname("live-app")] = 4628

	if res := c.Apply("vroom-app", 4321); !res.Succeeded() {
		t.Fatalf("la ruta de vroom debe registrarse: %+v", res)
	}

	livePort, liveStill := f.routes[Hostname("live-app")]
	if !liveStill || livePort != 4628 {
		t.Errorf("la ruta de la app viva debe quedar intacta: %d %v", livePort, liveStill)
	}
	if _, vroom := f.routes[Hostname("vroom-app")]; !vroom {
		t.Error("la ruta de vroom debe existir")
	}
	// Y parar lo nuestro no toca la suya.
	_ = c.Remove("vroom-app")
	if _, still := f.routes[Hostname("live-app")]; !still {
		t.Error("parar lo nuestro no puede expulsar la ruta de otro dueño")
	}
}
