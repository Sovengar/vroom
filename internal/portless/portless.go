// Package portless es el ÚNICO punto del proyecto autorizado a hablar con el
// binario `portless` y con su proxy. Igual que internal/worktree posee el
// spawn de git, este paquete posee el de portless: nadie más lo invoca.
//
// # Qué decide este paquete y qué NO
//
// vroom REGISTRA rutas. No arranca, no gestiona, no supervisa ni muestra el
// proxy. Si no hay proxy alcanzable, Apply degrada con un motivo y devuelve:
// el servicio que llama sigue vivo en su puerto. La salud de un servicio
// NUNCA depende de que exista su ruta — una ruta es una dirección, no una
// dependencia. Ver docs/adr/adr-0013-vroom-registers-portless-routes.md.
//
// # Por qué hay DOS verificaciones y no una
//
// `portless alias` es una escritura pura del fichero de estado: NUNCA contacta
// con el proxy y sale con exit 0 incluso con el proxy parado (medido, M1). Por
// tanto exit 0 NO prueba que la URL resuelva ahora mismo, y leer routes.json
// de vuelta sólo prueba que escribimos (M2). De ahí las dos verificaciones,
// que contestan preguntas distintas y ambas son obligatorias:
//
//   - la LECTURA DE VUELTA contesta "¿esta ruta es MÍA?". Sin ella, dos vrooms
//     con el mismo nombre se pisan en silencio porque el alta es un upsert
//     incondicional sin detección de conflictos (M8), y ambos reportan éxito.
//   - la SONDA EN VIVO contesta "¿responde esto AHORA?". Un fallo de conexión o
//     un timeout significa que no hay proxy sirviendo; cualquier respuesta HTTP
//     significa que el proxy enruta la ruta.
//
// Y un 502 cuenta como "el proxy enruta esta ruta, el servicio de detrás no
// responde", que es información DISTINTA de "el proxy no la sirve". Un 404, en
// cambio, sí significa que el proxy no conoce ese host. Confundir los dos
// convierte la sonda en una afirmación falsa, que es peor que no sondear.
//
// # Rutas y entorno nunca hardcodeados
//
// vroom corre bajo un gestor de servicios cuyo entorno no es el shell de login
// del usuario: un path que funciona en la terminal y falla en el daemon es un
// bug, no una configuración. State dir: $PORTLESS_STATE_DIR → $XDG_STATE_HOME/
// portless → $HOME/.portless (medido: el CLI honra $PORTLESS_STATE_DIR y
// $PORTLESS_HOME no existe para él; ver ResolveStateDir). Binario:
// $PORTLESS_BIN → exec.LookPath →
// directorios de shim de mise (medido: `env -i PATH=/usr/bin:/bin` NO resuelve
// portless, M14). Y el puerto del proxy se lee SIEMPRE de `proxy.port`, que
// sólo existe mientras el proxy corre (M6): su ausencia ES la señal de que no
// hay proxy, y nunca se cae a un 1355 supuesto.
//
// Toda llamada está acotada por timeout: un exec sin cota contra un binario
// colgado colgaría el arranque, y eso sí sería una pérdida de disponibilidad.
package portless

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout acota cada invocación del binario y cada sonda HTTP. La
// degradación nunca puede ser peor que no tener la feature.
const DefaultTimeout = 5 * time.Second

// Status es el resultado de la ruta, tal como lo publica el JSON.
// registered exige que la URL se haya visto funcionar; nada más.
const (
	// StatusRegistered: la ruta existe con NUESTRO puerto y el proxy la enruta.
	StatusRegistered = "registered"
	// StatusDegraded: hay una ruta pretendida pero no se puede afirmar que
	// funcione. Motivo obligatorio, url prohibida.
	StatusDegraded = "degraded"
)

// Motivos de degradación. Son texto legible por máquina: un agente decide con
// ellos, y por eso son estables y no prosaicos.
const (
	// ReasonPortlessMissing: el binario no se puede resolver.
	ReasonPortlessMissing = "portless_not_found"
	// ReasonPortlessFailed: el binario existe y falla (Node viejo included).
	ReasonPortlessFailed = "portless_failed"
	// ReasonPortlessTimeout: el binario no terminó dentro del timeout.
	ReasonPortlessTimeout = "portless_timeout"
	// ReasonProxyNotRunning: no existe proxy.port. ES la señal de que no hay
	// proxy en marcha (M6); nunca se asume un puerto por defecto.
	ReasonProxyNotRunning = "proxy_not_running"
	// ReasonProxyUnreachable: proxy.port existe pero nadie atiende en él.
	ReasonProxyUnreachable = "proxy_unreachable"
	// ReasonRouteConflict: el nombre ya lo tiene otro puerto (M8).
	ReasonRouteConflict = "route_conflict"
	// ReasonPortUnresolved: el puerto real del servicio aún no se ha resuelto,
	// así que no hay nada honesto que apuntar.
	ReasonPortUnresolved = "port_unresolved"
	// ReasonInvalidName: el nombre derivado no es un hostname válido.
	ReasonInvalidName = "route_invalid_name"
	// ReasonRouteNotServed: el proxy no conoce el host de la ruta.
	ReasonRouteNotServed = "route_not_served"
)

// ErrProxyNotRunning indica que no hay proxy en marcha: proxy.port no existe.
// Se distingue de un fallo de conexión porque son hechos distintos — el proxy
// estuvo y se paró, frente a un proxy que se declara y no atiende — y ambos
// degradan sin publicar url.
var ErrProxyNotRunning = errors.New("portless: no proxy running (proxy.port absent)")

// ProbeOnce hace una sonda real y acotada contra un puerto, con el Host (y el
// SNI TLS) puesto al hostname. Es la sonda que usa el seam, expuesta para poder
// probarla contra un listener de verdad: es lo que demuestra que un 404 y un
// 502 no son la misma cosa, y que un puerto cerrado no devuelve ningún status
// sino un error.
func ProbeOnce(scheme, hostname string, proxyPort int, path string, timeout time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return httpProbe(ctx, scheme, hostname, proxyPort, path)
}

// ExecFunc ejecuta el binario con los argumentos dados y devuelve stdout y el
// código de salida. Es el seam de hermeticidad: los tests nunca necesitan un
// portless real.
//
// El error devuelto es el de cmd.Run; el código de salida va aparte porque
// portless usa exit 1 de forma BENIGNA (`alias --remove` de un nombre que no
// existe, M10) y ese caso no puede distinguirse por el error.
type ExecFunc func(ctx context.Context, bin string, args ...string) (stdout string, exitCode int, err error)

// ProbeFunc hace UN request al puerto del proxy con el Host (y el SNI TLS)
// puesto al hostname de la ruta, sobre la dirección de loopback para no
// depender de la resolución de nombres de .localhost. Devuelve el status HTTP,
// o error si no hubo respuesta (conexión rechazada o timeout).
type ProbeFunc func(ctx context.Context, scheme, hostname string, proxyPort int, path string) (status int, err error)

// Client es el seam completo hacia portless. Todas las rutas y binarios son
// inyectables para que la suite sea hermética: el runner de CI no tiene
// portless, ni Node 24, ni proxy.
type Client struct {
	bin      string
	stateDir string
	exec     ExecFunc
	probe    ProbeFunc
	timeout  time.Duration
}

// ClientOption configura un Client. Todas son opcionales; sin ninguna,
// Default() da un cliente con los valores por defecto.
type ClientOption func(*Client)

// WithBinary inyecta el binario ya resuelto.
func WithBinary(bin string) ClientOption { return func(c *Client) { c.bin = bin } }

// WithStateDir inyecta el directorio de estado ya resuelto.
func WithStateDir(dir string) ClientOption { return func(c *Client) { c.stateDir = dir } }

// WithExec inyecta el ejecutor. Es el seam principal de los tests.
func WithExec(fn ExecFunc) ClientOption { return func(c *Client) { c.exec = fn } }

// WithProbe inyecta la sonda en vivo.
func WithProbe(fn ProbeFunc) ClientOption { return func(c *Client) { c.probe = fn } }

// WithTimeout acota las llamadas. Los tests lo acortan.
func WithTimeout(d time.Duration) ClientOption { return func(c *Client) { c.timeout = d } }

// New construye un cliente con el binario y el state dir ya resueltos por el
// llamador, más los seams quecyber el llamador quiera.
func New(opts ...ClientOption) *Client {
	c := &Client{timeout: DefaultTimeout}
	for _, o := range opts {
		o(c)
	}
	if c.exec == nil {
		c.exec = execCommand
	}
	if c.probe == nil {
		c.probe = httpProbe
	}
	return c
}

// Default resuelve binario y state dir del entorno y devuelve un cliente listo.
// Devuelve un cliente SIN binario si no se puede resolver: no es un error, es
// una degradación que Apply traduce en aviso.
func Default(opts ...ClientOption) *Client {
	opts = append([]ClientOption{
		WithBinary(ResolveBinary()),
		WithStateDir(ResolveStateDir()),
	}, opts...)
	return New(opts...)
}

// HasBinary reporta si se resolvió un binario. Con route_mode = "off" ni
// siquiera se llama: no se busca el binario si no hay contrato de ruta.
func (c *Client) HasBinary() bool { return c.bin != "" }

// Binary devuelve el binario resuelto ("" si no hay ninguno).
func (c *Client) Binary() string { return c.bin }

// StateDir devuelve el directorio de estado resuelto.
func (c *Client) StateDir() string { return c.stateDir }

// ResolveStateDir resuelve el directorio de estado de portless.
//
// MEDIDO contra portless 0.15.6, y corregido sobre lo que decía el plan: la
// variable que el CLI honra es $PORTLESS_STATE_DIR, y $PORTLESS_HOME **no la
// honra en absoluto**. Implementar el orden del plan producía dos vistas
// distintas del mismo estado —vroom leía proxy.port de un directorio y el
// binario escribía routes.json en otro— y el síntoma era una ruta que se
// registraba y luego no se podía quitar. Un seam que resuelve una ruta que la
// herramienta no resuelve no es una ventaja: es un modo de fallo silencioso.
//
// El orden es entonces: $PORTLESS_STATE_DIR → $XDG_STATE_HOME/portless →
// $HOME/.portless. Los dos últimos son el default del propio CLI.
//
// Nunca devuelve un path de un usuario concreto: vroom corre bajo un gestor de
// servicios cuyo entorno no es el shell de login.
func ResolveStateDir() string {
	if v := os.Getenv("PORTLESS_STATE_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "portless")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".portless")
	}
	return ""
}

// proxyPortFile es el fichero que declara el puerto del proxy DENTRO del state
// dir. Sólo existe mientras el proxy corre.
const proxyPortFile = "proxy.port"

// ResolveBinary resuelve el binario de portless, en orden: $PORTLESS_BIN →
// exec.LookPath → directorios de shim de mise conocidos.
//
// El último paso no es decorativo: medido, `env -i PATH=/usr/bin:/bin` NO
// resuelve portless porque vive tras los shims de mise (M14). Un `portless`
// desnudo no se puede asumir.
func ResolveBinary() string {
	if v := os.Getenv("PORTLESS_BIN"); v != "" {
		return v
	}
	if p, err := exec.LookPath("portless"); err == nil {
		return p
	}
	for _, dir := range miseShimDirs() {
		p := filepath.Join(dir, "portless")
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// miseShimDirs son los directorios de shim conocidos de mise, derivados del
// HOME del proceso que corre (nunca de un literal).
func miseShimDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".local", "share", "mise", "shims"),
		filepath.Join(home, ".local", "bin"),
	}
}

// execCommand ejecuta portless acotado por el timeout del contexto. El
// WaitDelay es lo que hace que el timeout acote el tiempo de reloj de verdad:
// sin él, un descendiente vivo con los pipes abiertos puede colgar Wait() más
// allá del deadline.
func execCommand(ctx context.Context, bin string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	if err != nil && ctx.Err() != nil {
		// El deadline es lo que mató al proceso: se distingue de un fallo
		// propio del binario porque el motivo que se publica es otro.
		return stdout.String(), code, fmt.Errorf("portless %s: %w", args[0], ctx.Err())
	}
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.String(), code, fmt.Errorf("portless %s: %w: %s", args[0], err, msg)
		}
		return stdout.String(), code, fmt.Errorf("portless %s: %w", args[0], err)
	}
	return stdout.String(), code, nil
}

// run invoca el binario acotado por el timeout del cliente.
func (c *Client) run(args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	return c.exec(ctx, c.bin, args...)
}

// Register da de alta la ruta name → port. Es un UPSERT INCONDICIONAL: mismo
// nombre con otro puerto sobrescribe en silencio y sale 0 (M8). Por eso su
// exit 0 no prueba nada: quien llama DEBE leer de vuelta y sondear en vivo.
func (c *Client) Register(name string, port int) error {
	if c.bin == "" {
		return errors.New(ReasonPortlessMissing)
	}
	_, code, err := c.run("alias", name, strconv.Itoa(port))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s: portless alias exited %d", ReasonPortlessFailed, code)
	}
	return nil
}

// ErrRouteAbsent es el caso BENIGNO de `--remove`: el nombre no existe.
//
// MEDIDO (M10): sale con exit 1 y el mensaje "No alias found for ...". No es un
// fallo —la ruta no está, que es justo lo que se quería— pero conviene
// distinguirlo de un fallo real, porque los dos significan cosas opuestas para
// la propiedad: si la ruta NO está, ya no es nuestra y se revoca; si el binario
// falló o tardó, la ruta puede seguir ahí y hay que conservar el handle.
var ErrRouteAbsent = errors.New("portless: no route with that name")

// Remove retira la ruta name.
//
// `alias --remove` de un nombre inexistente sale con 1 (M10) y eso es BENIGNO:
// un stop repetido no es un error. Remove lo refleja devolviendo nil, porque su
// contrato es "no Romper el stop".
//
// Quien necesita distinguir "no estaba" de "falló de verdad" es Release, que
// revoca la propiedad, y para eso está RemoveAbsent. Esa distinción NO puede
// vivir aquí sin romper el contrato benigno de Remove.
func (c *Client) Remove(name string) error {
	if name == "" {
		return nil
	}
	if c.bin == "" {
		return nil // sin portless no hay ruta que retirar
	}
	_, code, err := c.run("alias", "--remove", name)
	if code == 1 && isRouteAbsent(err) {
		return nil // estaba y ya no está: para Remove, bien
	}
	if err != nil && code == 0 {
		return err // timeout o fallo real
	}
	return nil
}

// RemoveAbsent hace lo mismo que Remove y además distingue el caso benigno de
// M10, que es el único que revoca la propiedad sin haber escrito nada.
func (c *Client) RemoveAbsent(name string) error {
	if name == "" {
		return nil
	}
	if c.bin == "" {
		return nil
	}
	_, code, err := c.run("alias", "--remove", name)
	switch {
	case code == 1 && isRouteAbsent(err):
		return ErrRouteAbsent
	case err != nil && code == 0:
		return err
	default:
		return nil
	}
}

// isRouteAbsent reconoce el mensaje benigno de M10 frente a un fallo real, que
// dejaría la ruta en su sitio.
func isRouteAbsent(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no alias found")
}

// routeLineRE extrae (hostname, puerto destino) de cada línea de la tabla de
// `portless list`, cuya forma medida es:
//
//	http://ow.localhost:1399  ->  localhost:2222  (alias)
//
// El TLD y el puerto de la izquierda son los del PROXY, no de la ruta: no se
// leen porque no son lo que hace falta, y porque el puerto que importa es el de
// la derecha, al que la ruta apunta de verdad.
var routeLineRE = regexp.MustCompile(`http://([^\s:]+):\d+\s+->\s+localhost:(\d+)`)

// Lookup lee de vuelta la ruta name y devuelve el puerto que portless tiene
// publicado para ella. found=false significa que el nombre no existe.
//
// Se parsea `list` y NO routes.json: el fichero de estado es interno del proxy
// sin contrato versionado, y la CLI sí es el contrato. La comparación de puerto
// que hace Apply sobre lo devuelto aquí es la que hace alcanzable el estado de
// conflicto: sin ella, M8 haría que dos vrooms se pisen en silencio.
func (c *Client) Lookup(name string) (port int, found bool, err error) {
	if c.bin == "" {
		return 0, false, errors.New(ReasonPortlessMissing)
	}
	out, code, err := c.run("list")
	if err != nil {
		return 0, false, err
	}
	if code != 0 {
		return 0, false, fmt.Errorf("%s: portless list exited %d", ReasonPortlessFailed, code)
	}
	host := Hostname(name)
	for _, m := range routeLineRE.FindAllStringSubmatch(out, -1) {
		if m[1] == host {
			p, convErr := strconv.Atoi(m[2])
			if convErr != nil {
				return 0, false, fmt.Errorf("puerto ilegible %q en portless list: %w", m[2], convErr)
			}
			return p, true, nil
		}
	}
	return 0, false, nil
}

// ProxyPort lee el puerto del proxy de proxy.port.
//
// La ausencia del fichero ES la señal de que no hay proxy en marcha (M6), y
// por eso devuelve ErrProxyNotRunning en vez de un puerto supuesto. Nunca se
// cae a 1355: un puerto que además se puede mover es exactamente el tipo de
// constante que este seam no debe introducir.
func (c *Client) ProxyPort() (int, error) {
	if c.stateDir == "" {
		return 0, ErrProxyNotRunning
	}
	raw, err := os.ReadFile(filepath.Join(c.stateDir, proxyPortFile))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrProxyNotRunning
		}
		return 0, fmt.Errorf("could not read portless %s: %w", proxyPortFile, err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || port <= 0 || port > 65535 {
		// Un proxy.port corrupto es indistinguible de un proxy parado a los
		// efectos de esta decisión, y degradar es lo seguro.
		return 0, ErrProxyNotRunning
	}
	return port, nil
}

// httpProbe hace UN request al puerto del proxy con el Host de la ruta, sobre
// loopback. No depende de la resolución de nombres de .localhost, que no está
// garantizada bajo un gestor de servicios.
//
// Devuelve el status HTTP sea cual sea, incluido 404 y 502: quien llama decide
// qué significa cada uno, porque 502 ("el proxy enruta y el backend no
// responde") prueba el enrutado y 404 ("el proxy no conoce este host") lo
// contradice. Sólo un error significa "no hubo respuesta".
func httpProbe(ctx context.Context, scheme, hostname string, proxyPort int, path string) (int, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort))
	dialer := &net.Dialer{}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", addr)
		},
		TLSClientConfig: &tls.Config{
			// El certificado de un proxy local no está en ninguna CA del
			// sistema. InsecureSkipVerify + ServerName NO se anulan entre sí:
			// el SNI sigue saliendo, que es lo que el proxy necesita para
			// elegir la ruta. Lo que se renuncia es la cadena, irrelevante
			// para un proxy de loopback.
			InsecureSkipVerify: true,
			ServerName:         hostname,
		},
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   2 * time.Second,
		ResponseHeaderTimeout: 3 * time.Second,
	}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+hostname+path, nil)
	if err != nil {
		return 0, err
	}
	req.Host = hostname
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}
