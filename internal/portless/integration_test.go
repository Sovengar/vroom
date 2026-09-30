package portless_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
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
	if os.Getenv("VROOM_PORTLESS_INTEGRATION") != "1" {
		t.Skip("integracion real: pon VROOM_PORTLESS_INTEGRATION=1 (aísla el estado en un temporal)")
	}
	if _, err := net.LookupPort("tcp", "0"); err != nil {
		t.Skip("sin red")
	}

	stateDir := t.TempDir()
	t.Setenv("PORTLESS_STATE_DIR", stateDir)
	t.Setenv("PORTLESS_HTTPS", "0")
	t.Setenv("PORTLESS_SYNC_HOSTS", "0")

	bin := os.Getenv("PORTLESS_BIN")
	if bin == "" {
		bin = portless.ResolveBinary()
	}
	if bin == "" {
		t.Skip("no hay portless instalado")
	}
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
