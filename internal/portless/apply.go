package portless

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"

	"vroom/internal/manifest"
)

// RouteMode son los tres estados del contrato de ruta. El default es off, de
// modo que un manifiesto que no declara nada se comporta exactamente como
// antes — y con off vroom NI SIQUIERA busca el binario.
const (
	// RouteModeOff: sin ruta. vroom no toca portless en absoluto.
	RouteModeOff = "off"
	// RouteModeAuto: nombre derivado del worktree, sin escribir nada.
	RouteModeAuto = "auto"
	// RouteModeNamed: nombre estable y explícito (route_name).
	RouteModeNamed = "named"
)

// RouteModeEnabled dice si un modo implica trabajo de ruta. Con off no se
// resuelve ni el binario: es la puerta de compatibilidad hacia atrás.
func RouteModeEnabled(mode string) bool { return mode != "" && mode != RouteModeOff }

// ClientFor devuelve el seam ya resuelto para un manifiesto, o nil si no hay
// contrato de ruta.
//
// Existe en este paquete, y no duplicado en cada llamador, por una razón que no
// es de estilo: con route_mode = "off" hay que devolver nil ANTES de resolver
// nada, y ese "antes" es la puerta de compatibilidad hacia atrás. Tres copias
// de esa comprobación son tres sitios donde un día alguien la mueve y la puerta
// se abre sin que nada falle.
func ClientFor(m *manifest.Manifest) *Client {
	if m == nil || !RouteModeEnabled(m.EffectiveRouteMode()) {
		return nil
	}
	return Default()
}

// Release retira la ruta de un servicio parado. El fallo es BENIGNO por diseño:
// quitar una ruta que no existe sale con 1 (medido, M10) y un stop repetido no
// es un error, porque el servicio ya está parado y parar no puede fallar por una
// dirección.
//
// Recibe el NOMBRE, no el Meta: este paquete es un seam y no debe depender del
// tipo de persistencia. Y no relee el manifiesto, que pudo cambiar desde el
// arranque; se retira lo que se registró, que es lo único que se puede demostrar
// como propio.
func Release(name string) {
	if name == "" {
		return
	}
	_ = Default().Remove(name)
}

// Result es el resultado de aplicar una ruta. Tri-estado y honesto por
// construcción: Name está siempre (es el nombre PRETENDIDO, haya éxito o no),
// Status siempre, Url SÓLO cuando se ha visto funcionar y Reason sólo al
// degradar. Es la lección de port_verified aplicada entera: una URL que nadie
// verificó no se publica, porque un agente que la lea se conecta a otra cosa.
type Result struct {
	Name   string // nombre pretendido, sin el TLD
	Host   string // hostname completo, con el TLD
	Status string // registered | degraded
	Url    string // sólo si registered, y con el esquema que se comprobó
	Reason string // sólo si degraded
	Port   int    // puerto real al que apunta la ruta, si se registró
}

// Succeeded dice si la ruta quedó registrada y verificada en el proxy vivo.
func (r Result) Succeeded() bool { return r.Status == StatusRegistered }

// Degraded construye un resultado degradado con su motivo. No lleva Url: un
// resultado degradado no publica dirección.
func Degraded(name, reason string) Result {
	return Result{Name: name, Host: Hostname(name), Status: StatusDegraded, Reason: reason}
}

// Apply registra la ruta de un servicio y devuelve lo que se ha podido
// PROBAR de ella, no lo que se ha pedido.
//
// El orden de los pasos es el contrato, y cada uno responde a un hecho medido:
//
//  1. ¿Hay binario? No → degrada. No es error: la ausencia de portless es la
//     condición normal (limitación 6 de adr-0012, cerrada por adr-0013).
//  2. ¿El puerto real está resuelto? No → degrada. Registrar contra un puerto
//     sin resolver sólo compra una ventana de error (M9), y publicar una
//     dirección a un puerto que nadie escucha es una mentira.
//  3. Se REGISTRA contra el puerto real. Por M9 el puerto destino no necesita
//     estar escuchando, y por M3 la ruta sobrevive a un reinicio del proxy: el
//     registro es persistente y correcto aunque el proxy esté parado, así que
//     la verificación decide qué se PUBLICA, no si se REGISTRA.
//  4. Se LEE DE VUELTA y se compara el puerto. Por M8 el alta es un upsert
//     incondicional sin detección de conflictos: sin esta comparación, dos
//     vrooms con el mismo nombre se pisan en silencio y ambos reportan éxito.
//     Esta verificación contesta "¿la ruta es MÍA?".
//  5. Se SONDA EL PROXY VIVO. Por M1 el binario nunca contacta con el proxy,
//     así que exit 0 no prueba nada sobre ahora mismo. Ésta contesta
//     "¿responde AHORA?".
//
// Ningún paso puede hacer fallar el arranque: todos devuelven un Result
// degradado. La salud del servicio no depende de su ruta.
func (c *Client) Apply(name string, port int) Result {
	if !c.HasBinary() {
		return Degraded(name, ReasonPortlessMissing)
	}
	if port <= 0 {
		// Nada honesto que apuntar: no se inventa una dirección a un puerto
		// que nadie confirmó.
		return Degraded(name, ReasonPortUnresolved)
	}

	if err := c.Register(name, port); err != nil {
		return Degraded(name, classify(err))
	}

	// Lectura de vuelta: propiedad de la ruta, no disponibilidad.
	published, found, err := c.Lookup(name)
	switch {
	case err != nil:
		// Se registró pero no se puede ni leer de vuelta: no se afirma nada.
		return Degraded(name, classify(err))
	case !found:
		return Degraded(name, ReasonRouteNotServed)
	case published != port:
		// M8 en vivo: el nombre lo tiene otro puerto. vroom no reporta éxito
		// sobre una dirección que no controla.
		return Degraded(name, ReasonRouteConflict)
	}

	return c.verify(name, port)
}

// verify sondea el proxy vivo para la ruta y decide si se publica su URL.
//
// El esquema se determina PROBANDO, no suponiendo: se intenta https y, si no
// responde, http, y se publica el que respondió. Así el caso TLS / puerto 443
// no es un riesgo — no hay ningún supuesto que el TLS pueda refutar — y el
// precio es una sonda.
func (c *Client) verify(name string, port int) Result {
	host := Hostname(name)

	// M6: la ausencia de proxy.port ES la señal de que no hay proxy. Se
	// degrada aquí y no se cae a un puerto supuesto como 1355.
	proxyPort, err := c.ProxyPort()
	if err != nil {
		return Degraded(name, ReasonProxyNotRunning)
	}

	for _, scheme := range []string{"https", "http"} {
		status, err := c.probeWithTimeout(scheme, host, proxyPort, probePath)
		if err != nil {
			// Fallo de conexión o timeout: nadie está sirviendo por aquí.
			continue
		}
		if status == 404 {
			// MEDIDO: el proxy responde 404 cuando NO conoce el host, y 502
			// cuando lo enruta y el backend no responde. Tratar 404 como
			// prueba de enrutado afirmaría una dirección que el proxy no
			// sirve, que es justo el fallo que este diseño existe para evitar.
			return Degraded(name, ReasonRouteNotServed)
		}
		// Cualquier otra respuesta —incluido 502— prueba que el proxy ENRUTA
		// esta ruta. Un 502 dice "enruta y el servicio de detrás no responde",
		// que es información distinta de "no la sirve".
		return Result{
			Name:   name,
			Host:   host,
			Status: StatusRegistered,
			Url:    scheme + "://" + host,
			Port:   port,
		}
	}

	// Ningún esquema respondió. No hay proxy sirviendo, o el puerto declarado no
	// atiende: en ambos casos no se publica url.
	if !c.acceptsConnections(proxyPort) {
		return Degraded(name, ReasonProxyUnreachable)
	}
	return Degraded(name, ReasonRouteNotServed)
}

// probePath es la ruta que se pide al proxy. "/" basta: la verificación es
// "¿el proxy enruta este host?", y hasta un 404 de la app de detrás es prueba
// de enrutado. No se usa health_path porque ése es un contrato de la TUI y una
// ruta puede no tener ninguno.
const probePath = "/"

// probeWithTimeout acota la sonda. Una sonda sin cota contra un proxy colgado
// colgaría el arranque, que es la única pérdida de disponibilidad real de este
// seam.
func (c *Client) probeWithTimeout(scheme, host string, proxyPort int, path string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	return c.probe(ctx, scheme, host, proxyPort, path)
}

// acceptsConnections dice si el puerto declarado del proxy acepta alguna
// conexión. Distingue "el proxy declarado no responde" de "el proxy responde y
// no conoce la ruta", que degradan con motivos distintos.
func (c *Client) acceptsConnections(proxyPort int) bool {
	conn, err := net.DialTimeout("tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort)), c.timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// classify traduce un error del binario al motivo de degradación que se
// publica. El timeout tiene su propio motivo porque su aviso al usuario es
// distinto: no es que portless falle, es que no respondió.
func classify(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded):
		return ReasonPortlessTimeout
	case errors.Is(err, ErrProxyNotRunning):
		return ReasonProxyNotRunning
	case isMissingBinary(err):
		return ReasonPortlessMissing
	default:
		return ReasonPortlessFailed
	}
}

// isMissingBinary distingue "no hay binario" de "el binario falló".
//
// No basta con errors.Is(err, exec.ErrNotFound): con una RUTA que no existe,
// exec.Command no devuelve ErrNotFound sino el error de fork/exec ("no such file
// or directory"), así que un binario ausente y uno roto darían el mismo aviso —
// y el usuario acabaría depurando un portless roto que no existe, o un Node
// viejo que no es la causa. Los dos motivos se distinguen por el texto porque
// no hay más señal disponible.
func isMissingBinary(err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	return err != nil && strings.Contains(err.Error(), "no such file or directory")
}

// Warn devuelve el aviso que el usuario ve para un resultado dado, o "" si no
// hay nada que avisar. Va al canal de avisos que YA existe (Result.Warnings de
// startsvc → log del servicio en CLI, notificación visible en la TUI): no se
// inventa un canal nuevo.
//
// El texto nombra el fallo CONCRETO, porque un aviso que no dice qué pasa no
// es un aviso: es ruido. Y todos dicen lo mismo en lo importante — el servicio
// sigue vivo en su puerto — para que la ausencia de ruta nunca se lea como una
// caída.
func Warn(r Result) string {
	switch r.Reason {
	case "":
		return ""
	case ReasonPortlessMissing:
		return "no portless route published: the portless binary is not available in this environment; " +
			"the service is running on its own port as usual"
	case ReasonPortlessTimeout:
		return "portless did not respond in time and the route was not verified; " +
			"the service is running on its own port as usual"
	case ReasonPortlessFailed:
		return "the portless binary failed (an older Node is the usual cause) and no route was published; " +
			"the service is running on its own port as usual"
	case ReasonProxyNotRunning:
		return "portless has no proxy running, so no stable URL was published; " +
			"the service is running on its own port as usual"
	case ReasonProxyUnreachable:
		return "portless declares a proxy port that does not accept connections; " +
			"the service is running on its own port as usual"
	case ReasonRouteConflict:
		return fmt.Sprintf(
			"the portless route name %q is already taken by another port and was left untouched; "+
				"the service is running on its own port as usual", r.Host)
	case ReasonPortUnresolved:
		return "the service port is not resolved yet, so no route was published"
	case ReasonRouteNotServed:
		return "the portless proxy does not serve this route, so no URL was published; " +
			"the service is running on its own port as usual"
	default:
		return "no portless route published (" + r.Reason + "); " +
			"the service is running on its own port as usual"
	}
}

// Reconcile limpia las rutas que este servicio se dejó a sí mismo en un
// arranque anterior. Es OBLIGATORIA en cada arranque, no una mejora pendiente.
//
// Por qué no puede delegarse: `portless prune` NO toca las rutas de alias
// (`pid: 0`, contadas como activas, M5) — medido, no supuesto. Luego vroom es
// lo ÚNICO que puede limpiarlas, y una ruta que dejó un vroom que murió sin
// parar su servicio sería permanente sin esto.
//
// La regla que gobierna la limpieza es el fallo cerrado que ya gobierna el
// cambio de puertos: una ruta que RESPONDE y no es nuestra no se retira nunca,
// se avisa. Borrar algo ajeno es peor que dejar una ruta de más.
//
// prev es el nombre que este servicio persistió en su Meta anterior ("" si no
// registró ninguno) y prevPort el puerto al que apuntaba, que es lo que
// permite distinguir "la mía, y además muerta" de "viva, y no es mía".
func (c *Client) Reconcile(prev string, prevPort int, current string) []string {
	if !c.HasBinary() || prev == "" || prev == current {
		return nil // nada que reconciliar, o ya es la misma ruta
	}

	published, served := c.liveRoute(prev)
	switch {
	case !served:
		// No responde: es una ruta huérfana de este servicio —rama renombrada,
		// o un vroom que murió sin pararla—. Retirarla es lo único que
		// corresponde, y lo único que `portless prune` no va a hacer.
		_ = c.Remove(prev)
		return nil
	case published != prevPort:
		// Responde, pero en un puerto que no es el que persistimos: no es
		// nuestra. Se avisa y NO se toca. Nadie puede probar lo contrario.
		return []string{fmt.Sprintf(
			"a portless route named %q is already serving another port (%d); it was left untouched",
			Hostname(prev), published)}
	default:
		return nil // responde con nuestro puerto: es nuestra y está viva
	}
}

// liveRoute sondea un nombre de ruta contra el proxy vivo y devuelve el puerto
// que portless publica para él, y si el proxy lo está sirviendo.
//
// Las dos cosas se necesitan juntas: que responda no basta (podría estar
// sirviendo OTRO nombre) y el puerto del fichero tampoco basta (M2: sólo
// prueba que escribimos). Por eso se cruzan.
func (c *Client) liveRoute(name string) (port int, served bool) {
	proxyPort, err := c.ProxyPort()
	if err != nil {
		return 0, false // no hay proxy: nada responde, y no se puede distinguir más
	}
	for _, scheme := range []string{"https", "http"} {
		status, err := c.probeWithTimeout(scheme, Hostname(name), proxyPort, probePath)
		if err != nil {
			continue
		}
		if status == 404 {
			continue // el proxy no conoce el host: no lo está sirviendo
		}
		published, found, lookupErr := c.Lookup(name)
		if lookupErr != nil || !found {
			continue
		}
		return published, true
	}
	return 0, false
}
