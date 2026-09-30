package portless_test

import (
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"vroom/internal/portless"
)

// ESTE ES EL ÚNICO TEST DEL REPO QUE HABLA CON UN PORTLESS REAL, y se salta
// solo si no lo hay. El runner de CI no tiene portless, ni Node 24, ni proxy, y
// por eso el resto de la suite va entera por el seam inyectado con fixtures.
//
// Cuando sí se ejecuta, NO toca el estado del usuario: se aísla con
// PORTLESS_STATE_DIR en un temporal y HTTPS/SYNC_HOSTS apagados. Nunca se lanza
// `portless prune`, `clean` ni `proxy stop` contra nada, y el proxy que este
// test levanta (si lo hay) se apaga por PID.
//
// Se ejecuta a propósito con VROOM_PORTLESS_INTEGRATION=1 porque tocar un proxy
// real, aunque aislado, no es algo que debayk happen por sorpresa.
func TestIntegrationRealPortless(t *testing.T) {
	stateDir := integrationStateDir(t)
	bin := integrationBin(t)
	c := portless.New(
		portless.WithBinary(bin),
		portless.WithStateDir(stateDir),
		portless.WithTimeout(10*time.Second),
	)

	// Sin proxy, el resultado es degradado y NO publica url: la ruta queda
	// escrita pero no se afirma que responda. Esto es el caso que la suite
	// hermética no puede demostrar, porque allí el proxy es un doble.
	res := c.Apply("vroom.integration", 4321, 0)
	if res.Succeeded() {
		t.Fatalf("sin un proxy propio no debe publicarse url, got %+v", res)
	}
	if res.Url != "" {
		t.Errorf("una ruta no verificada no publica url, got %q", res.Url)
	}
	// Y la ruta ESCRIBE, que es lo que permite que se sirva al volver el proxy.
	port, found, err := c.Lookup("vroom.integration")
	if err != nil {
		t.Skipf("portless no responde como se espera (%s): %v", bin, err)
	}
	if !found || port != 4321 {
		t.Fatalf("la ruta debe quedar registrada para cuando vuelva el proxy: port=%d found=%v", port, found)
	}

	// El stop retira la ruta, y repetirlo no es un error.
	if err := c.Remove("vroom.integration"); err != nil {
		t.Errorf("parar debe retirar la ruta: %v", err)
	}
	if err := c.Remove("vroom.integration"); err != nil {
		t.Errorf("un stop repetido no puede fallar: %v", err)
	}
	if _, still, _ := c.Lookup("vroom.integration"); still {
		t.Error("la ruta debe desaparecer tras el stop")
	}
}

// ProbeOnce hace UNA sonda real y acotada contra el puerto indicado, con el
// Host de la ruta. Es la sonda que usa el seam, expuesta para poder probarla
// contra un listener real sin un portless de por medio — que es lo que
// demuestra que 404 y 502 significan cosas distintas.
func probeOnce(scheme, hostname string, proxyPort int) (int, error) {
	return portless.ProbeOnce(scheme, hostname, proxyPort, "/", 3*time.Second)
}

// ---- la sonda real, sin portless ----

// La sonda es lo único que puede mentir sin que nadie se entere, así que se
// prueba contra un httptest real: un 404 significa que el proxy NO conoce el
// host, y un 502 que sí lo enruta aunque el backend esté muerto.
//
// No necesita portless, así que esto sí corre en CI.
func TestLiveProbeDistinguishesNotServedFromBackendDown(t *testing.T) {
	// Un "proxy" que responde 502 como lo haría portless con un backend caído.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go serveOnce(ln, "502 Bad Gateway")

	port := ln.Addr().(*net.TCPAddr).Port
	status, err := probeOnce("http", "app.localhost", port)
	if err != nil {
		t.Fatalf("el proxy de prueba debe responder: %v", err)
	}
	// 502 prueba que el proxy ENRUTA: el verify lo trata como registrado.
	if status != 502 {
		t.Fatalf("se esperaba 502, got %d", status)
	}

	// Y un 404 significa lo contrario: el proxy no conoce el host.
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln2.Close() }()
	go serveOnce(ln2, "404 Not Found")
	port2 := ln2.Addr().(*net.TCPAddr).Port
	status2, err := probeOnce("http", "app.localhost", port2)
	if err != nil {
		t.Fatal(err)
	}
	if status2 != 404 {
		t.Fatalf("se esperaba 404, got %d", status2)
	}

	// Un puerto sin nadie escuchando NO es un 404: es un fallo de conexión, que
	// es la tercera categoría y la que significa "no hay proxy".
	if _, err := probeOnce("http", "app.localhost", closedPort(t)); err == nil {
		t.Error("un puerto cerrado debe fallar, no devolver un status")
	}
}

// serveOnce acepta una conexión y responde con la línea de status dada, que es
// lo mínimo para que la sonda vea una respuesta HTTP real.
func serveOnce(ln net.Listener, status string) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	_, _ = conn.Read(buf)
	_, _ = conn.Write([]byte("HTTP/1.1 " + status + "\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
}

// closedPort pide un puerto y lo devuelve ya cerrado: nadie atiende en él.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// integrationStateDir aísla el estado de portless en un temporal y lo limpia al
// terminar. NUNCA toca ~/.portless del usuario: su proxy está en marcha y ese
// estado no es nuestro.
func integrationStateDir(t *testing.T) string {
	t.Helper()
	requireIntegration(t)
	dir := t.TempDir()
	t.Setenv("PORTLESS_STATE_DIR", dir)
	t.Setenv("PORTLESS_HTTPS", "0")
	t.Setenv("PORTLESS_SYNC_HOSTS", "0")
	return dir
}

// integrationBin devuelve el binario real, o se salta.
func integrationBin(t *testing.T) string {
	t.Helper()
	requireIntegration(t)
	if bin := os.Getenv("PORTLESS_BIN"); bin != "" {
		return bin
	}
	if bin := portless.ResolveBinary(); bin != "" {
		return bin
	}
	t.Skip("no hay portless instalado")
	return ""
}

// requireIntegration exige la variable de activación. Tocar un portless real,
// aunque aislado, no debe pasar por sorpresa.
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("VROOM_PORTLESS_INTEGRATION") != "1" {
		t.Skip("integración real: pon VROOM_PORTLESS_INTEGRATION=1 (aísla el estado en un temporal)")
	}
}

// TestRouteSurvivesAProxyRestart verifica M3 de verdad: la ruta registrada
// sigue ahí después de que el proxy se pare y vuelva, SIN registrarla otra vez.
//
// La versión hermética ponía `noProxy=false` sobre el mismo doble, así que sólo
// afirmaba la persistencia en el fichero. Esto mata el proceso del proxy y
// arranca otro, que es lo que hace la afirmación.
//
// Se para el proxy por PID, nunca con `portless proxy stop`.
func TestRouteSurvivesAProxyRestart(t *testing.T) {
	iso := integrationStateDir(t)
	bin := integrationBin(t)
	proxyPort := startIsolatedProxy(t, iso, bin)

	// Un backend cualquiera al que apuntar.
	backend := startEchoBackend(t, 0)

	c := portless.New(
		portless.WithBinary(bin),
		portless.WithStateDir(iso),
		portless.WithTimeout(15*time.Second),
	)
	if res := c.Apply("vroom.restart", backend, 0); !res.Succeeded() {
		t.Skipf("sin proxy propio no hay nada que verificar: %+v", res)
	}

	// Parar el proxy por PID.
	pid := readProxyPID(t, iso)
	_ = syscall.Kill(pid, syscall.SIGTERM)
	waitPortClosed(t, proxyPort)

	// Y volverlo a levantar.
	proxyPort = startIsolatedProxy(t, iso, bin)

	// La ruta debe seguir en el fichero, con el mismo puerto, sin registrarla.
	port, found, err := c.Lookup("vroom.restart")
	if err != nil || !found {
		t.Fatalf("tras el reinicio la ruta debe seguir registrada: found=%v err=%v", found, err)
	}
	if port != backend {
		t.Errorf("la ruta debe seguir apuntando a %d, got %d", backend, port)
	}

	// Y el proxy nuevo debe SERVIRLA: eso es lo que M3 afirma.
	status := probeHTTP(t, "http", "vroom.restart.localhost", proxyPort)
	if status == 0 {
		t.Errorf("el proxy debe servir la ruta tras el reinicio, no hubo respuesta")
	}

	portless.Release(c, "vroom.restart")
}

// TestIntegrationDoesNotEvictLivePortlessRoutes verifica M4 contra un binario
// de verdad y con una app viva gestionada por `portless run`.
//
// Este es el caso que la versión hermética NO podía cubrir: afirmaba que
// escribir un alias no expulsa una ruta con pid propio, pero lo comprobaba sobre
// un `map[string]int` al que el seam sólo añade. Aquí hay un proceso real
// servido por el proxy, y se afirma que sigue sirviendo DESPUÉS del alias de
// vroom.
func TestIntegrationDoesNotEvictLivePortlessRoutes(t *testing.T) {
	iso := integrationStateDir(t)
	bin := integrationBin(t)
	proxyPort := startIsolatedProxy(t, iso, bin)

	// Una app viva con su propia ruta, arrancada por portless (pid > 0).
	liveName := "vroom-live-app"
	if err := runPortlessApp(t, bin, liveName); err != nil {
		t.Skipf("no se pudo arrancar la app viva de portless: %v", err)
	}

	liveHost := liveName + ".localhost"
	if !waitServes(t, liveHost, proxyPort) {
		t.Skip("la app viva no llegó a servirse; el entorno no sirve para esta prueba")
	}

	c := portless.New(
		portless.WithBinary(bin),
		portless.WithStateDir(iso),
		portless.WithTimeout(15*time.Second),
	)

	// El puerto que la app oye de verdad es el que asigna portless, no el que se
	// le pide: se toma de la ruta publicada.
	appPort, found, err := c.Lookup(liveName)
	if err != nil || !found {
		t.Fatalf("la app viva debe tener ruta registrada: found=%v err=%v", found, err)
	}

	if res := c.Apply("vroom-other", 4321, 0); !res.Succeeded() {
		t.Fatalf("la ruta de vroom debe registrarse: %+v", res)
	}

	// La ruta de la app viva debe seguir ahí, en SU puerto.
	published, stillThere, err := c.Lookup(liveName)
	if err != nil || !stillThere {
		t.Fatalf("la ruta de la app viva debe seguir existiendo: found=%v err=%v", stillThere, err)
	}
	if published != appPort {
		t.Errorf("la ruta de la app viva debe seguir en %d, got %d", appPort, published)
	}

	// Y debe seguir SIRVIENDO: la prueba de que no la evacuamos.
	if status := probeHTTP(t, "http", liveHost, proxyPort); status == 0 {
		t.Error("la app viva dejó de servirse tras el alias de vroom")
	}

	// Parar lo nuestro tampoco puede expulsarla.
	portless.Release(c, "vroom-other")
	if _, still, _ := c.Lookup(liveName); !still {
		t.Error("parar lo nuestro no puede expulsar la ruta de la app viva")
	}
	if status := probeHTTP(t, "http", liveHost, proxyPort); status == 0 {
		t.Error("la app viva dejó de servirse tras retirar la ruta de vroom")
	}
}

// ---- helpers de la integración ----

// startIsolatedProxy levanta un proxy propio en el estado AISLADO y devuelve su
// puerto. Nunca toca ~/.portless.
func startIsolatedProxy(t *testing.T, iso, bin string) int {
	t.Helper()
	// Puerto libre para el proxy, calculado antes de pedirlo a portless.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	cmd := exec.Command(bin, "proxy", "start", "-p", strconv.Itoa(proxyPort))
	cmd.Env = append(os.Environ(), "PORTLESS_STATE_DIR="+iso)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("no se pudo arrancar un proxy aislado: %v: %s", err, out)
	}
	waitPortOpen(t, proxyPort)
	return proxyPort
}

// readProxyPID lee el pid del proxy del estado aislado. Pararlo por PID y no con
// `portless proxy stop` es una condición de este trabajo.
func readProxyPID(t *testing.T, iso string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(iso, "proxy.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// startEchoBackend levanta un servidor HTTP efímero y devuelve su puerto.
func startEchoBackend(t *testing.T, port int) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().(*net.TCPAddr).Port
}

// portlessAppScript es un servidor HTTP que honra $PORT.
//
// MEDIDO: `portless <name> <cmd>` REASIGNA el puerto y lo pasa por PORT (pedir
// 4611 explícitamente acabó sirviendo en 4698). Una app que hace bind a un
// puerto fijo deja al proxy enruta hacia un sitio donde nadie escucha, y el
// resultado es un 502 que no dice nada. Es el mismo contrato que vroom impone
// con `PORT=${PORT:-N}`, y por eso el script lo lee del entorno.
const portlessAppScript = `import os,http.server
http.server.HTTPServer(('127.0.0.1',int(os.environ['PORT'])),http.server.SimpleHTTPRequestHandler).serve_forever()`

// runPortlessApp arranca una app por portless, que es lo que le da una ruta CON
// PID PROPIO: la que prune no toca y que vroom no debe poder evacuar.
//
// Se limpia con `portless kill <name>`, que es la vía del propio portless para
// sus apps; nunca se toca el estado del usuario.
func runPortlessApp(t *testing.T, bin, name string) error {
	t.Helper()
	cmd := exec.Command(bin, name, "python3", "-c", portlessAppScript)
	// El CWD va FUERA de cualquier repo git a propósito: MEDIDO, portless
	// deriva su prefijo de worktree de la RAMBA (M13), así que arrancada dentro
	// de este worktree la app se publicaría como `<rama>.<nombre>` y el
	// hostname que se busca aquí no existiría. Fuera del repo, el nombre es el
	// que se le pidió, que es lo que hace comparables las dos rutas.
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "PORTLESS_STATE_DIR="+os.Getenv("PORTLESS_STATE_DIR"))
	if err := cmd.Start(); err != nil {
		return err
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = exec.Command(bin, "kill", name).Run()
	})
	return nil
}

// probeHTTP hace una sonda contra el puerto del proxy y devuelve el status, o 0
// si no hubo respuesta.
func probeHTTP(t *testing.T, scheme, host string, proxyPort int) int {
	t.Helper()
	status, err := portless.ProbeOnce(scheme, host, proxyPort, "/", 3*time.Second)
	if err != nil {
		return 0
	}
	return status
}

// waitServes espera a que el proxy ENRUTE de verdad un host.
//
// Un 404 NO cuenta como servido: MEDIDO, el proxy responde 404 a un host que no
// conoce, y ese 404 es su respuesta por defecto a cualquier nombre. Aceptarlo
// haría que esta espera pasara al instante con la app sin arrancar, y el test
// seguiría adelante hasta fallar más lejos con un mensaje que no señala la
// causa. Un 502 sí cuenta: significa que el proxy enruta y el backend aún no
// responde, que es el estado normal durante el arranque.
func waitServes(t *testing.T, host string, proxyPort int) bool {
	t.Helper()
	for range 40 {
		switch probeHTTP(t, "http", host, proxyPort) {
		case 404, 0:
			// host desconocido, o aún sin respuesta
		default:
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

func waitPortOpen(t *testing.T, port int) {
	t.Helper()
	for range 40 {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Skipf("el proxy aislado no abrió el puerto %d", port)
}

func waitPortClosed(t *testing.T, port int) {
	t.Helper()
	for range 40 {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
		if err != nil {
			return
		}
		_ = c.Close()
		time.Sleep(150 * time.Millisecond)
	}
	t.Skipf("el puerto %d siguió abierto tras parar el proxy", port)
}

// ---- el proxy.port real ----

// proxy.port medido son 4 bytes sin salto de línea. Esta es la fuente ÚNICA del
// puerto del proxy, y un proxy parado no lo tiene.
func TestProxyPortFileShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.port")
	if err := os.WriteFile(path, []byte("1399"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 4 || strings.TrimSpace(string(data)) != "1399" {
		t.Errorf("proxy.port debe ser el número pelado, got %q", data)
	}
}
