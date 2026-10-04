// Package startsvc orquesta el arranque de un servicio y, en modo dynamic,
// resuelve su puerto real.
//
// Antes de existir, cada llamador (TUI, CLI, motor de stacks) repetía el
// mismo bloque: Start → Meta{Port: manifest.Port} → SaveMeta. Con puertos
// dinámicos ese bloque se rompe por partida triple, así que vive aquí una
// sola vez.
//
// El orden importa y es el contrato:
//
//  1. reservar el puerto (dynamic) — falla antes de tocar el sistema
//  2. arrancar el hijo con PORT inyectado y el entorno del padre intacto
//  3. persistir el intento ANTES de descubrir: la ventana spawn→SaveMeta
//     pasa de sub-ms a segundos, y un tick que leyera el meta viejo
//     reportaría "stopped" con el proceso vivo
//  4. descubrir y verificar el puerto real acotado por la liveness
//  5. reconciliar y registrar la ruta de portless, si el manifiesto la pide
//  6. persistir el puerto real antes de devolver el control
//
// El paso 5 va DESPUÉS del discovery y no antes por un hecho medido: el puerto
// destino no necesita estar escuchando, así que registrar antes sólo compra una
// ventana de error visible, mientras que registrar tarde garantiza que nunca
// se publica una ruta para un servicio cuyo puerto sigue sin resolver.
//
// Y ese paso no puede hacer fallar el arranque: la ausencia de portless, de su
// proxy, o un binario colgado degradan a un aviso. La salud de un servicio
// NUNCA depende de que exista su ruta. Ver docs/adr/adr-0013.
package startsvc

import (
	"fmt"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/state"
)

// Request describe un arranque.
type Request struct {
	Manifest   *manifest.Manifest
	Path       string
	Store      *state.Store
	Manager    process.Manager
	StdoutPath string
	StderrPath string

	// DiscoveryTimeout acota la espera del puerto real. 0 = default.
	// Un linaje que muere antes se reporta de inmediato, sin gastarlo.
	DiscoveryTimeout time.Duration

	// Routes es el seam hacia portless. Nil = este servicio no registra
	// rutas, y es lo que pasa con route_mode = "off": no se busca el binario,
	// no se shellea a nada y el arranque es idéntico al de antes de este
	// campo. Es inyectable para que la suite sea hermética —el runner de CI no
	// tiene portless, ni Node 24, ni proxy— igual que procRoot lo es para las
	// lecturas de /proc.
	Routes RouteRegistrar

	// Branch es la rama git del proyecto, usada para derivar el nombre de ruta
	// en route_mode = "auto".
	Branch string
}

// RegistrarFor resuelve el seam de rutas de un manifiesto y lo devuelve YA
// NORMALIZADO como interfaz.
//
// MEDIDO (bug): los tres llamadores pasaban `portless.ClientFor(m)` directamente al
// campo `Routes`, que es de tipo `RouteRegistrar`. `ClientFor` devuelve un
// `*portless.Client`, y un puntero nil dentro de una interfaz NO es una interfaz
// nil: el `if req.Routes == nil` de applyRoute no se cumplía.
//
// El síntoma, medido con un servicio real sin `route_mode`: su stderr empezaba con
// `portless route: unknown route_mode "off"` en cada arranque, y el camino de ruta
// se ejecutaba entero para un servicio que explícitamente no quiere ruta.
// `route_mode = "off"` es el DEFAULT, así que era el caso mayoritario: una línea de
// ruido en el log de casi todos los servicios.
//
// Aquí la conversión ocurre una vez y en el paquete que posee el contrato "nil
// significa sin ruta", que es el único sitio donde puede comprobarse.
func RegistrarFor(m *manifest.Manifest) RouteRegistrar {
	c := portless.ClientFor(m)
	if c == nil {
		return nil
	}
	return c
}

// RouteRegistrar es el seam de rutas que necesita startsvc. Existe para que el
// paquete no dependa de cómo se construye el cliente de portless y para que los
// tests puedan ejercitar el ciclo completo sin un portless real.
type RouteRegistrar interface {
	// Apply registra la ruta del servicio en el puerto dado y devuelve lo que
	// se ha podido PROBAR. Nunca devuelve error: la ausencia de portless es
	// una degradación con aviso, no un fallo de arranque.
	//
	// prev es lo que se sabe de la ruta anterior: si la tenemos y sigue siendo
	// nuestra, el nombre puede aparecer en otro puerto (la app reinició) y hay
	// que mover la ruta. Sin esa prueba, cualquier nombre ajeno es conflicto.
	Apply(name string, port int, prev portless.Ownership) portless.Result
	// Reconcile limpia las rutas que este servicio se dejó en un arranque
	// anterior. Devuelve avisos, nunca errores.
	//
	// held es Ownership y NO un puerto: el handle se conserva aunque la
	// propiedad esté revocada, así que(handle vivo) no es (somos dueños), y un
	// puerto crudo aquí sería autoridad para borrar un nombre ajeno.
	Reconcile(prev string, held portless.Ownership, current string) []string
}

// Result es el arranque resuelto.
type Result struct {
	Meta     state.Meta
	Pid      int
	Port     int
	Warnings []string // no fatales: la app ignoró PORT, etc.
}

// Start arranca el servicio y devuelve su Meta con el puerto REAL.
//
// En fixed y none no se reserva ni se inyecta nada: el comportamiento es
// idéntico al de antes de este paquete, incluido el meta con el puerto
// declarado y SaveMeta inmediatamente después del spawn.
func Start(req Request) (Result, error) {
	mode := req.Manifest.EffectivePortMode()

	reserved, env := 0, []string(nil)
	if mode == manifest.PortModeDynamic {
		p, err := process.ReservePort()
		if err != nil {
			return Result{}, fmt.Errorf("could not reserve a dynamic port: %w", err)
		}
		reserved = p
		// HOST acompaña a PORT para que la app no abra en 0.0.0.0 por
		// defecto y exponga el servicio a la red sin querer.
		env = []string{fmt.Sprintf("PORT=%d", reserved), "HOST=127.0.0.1"}
	}

	res, err := req.Manager.Start(process.StartSpec{
		Command:    req.Manifest.Command,
		WorkDir:    req.Path,
		StdoutPath: req.StdoutPath,
		StderrPath: req.StderrPath,
		Env:        env,
	})
	if err != nil {
		process.ReleasePort(reserved) // el hijo nunca llegó a existir
		return Result{}, err
	}

	base := state.Meta{
		Name:           req.Manifest.Name,
		ProjectPath:    req.Path,
		Port:           req.Manifest.Port,
		ProcessPattern: req.Manifest.ProcessPattern,
		Command:        req.Manifest.Command,
		Pid:            res.Pid,
		Pgid:           res.Pgid,
		CreationTimeMs: res.CreationTimeMs,
		StartedAt:      time.Now().Format(time.RFC3339),
		State:          state.StateRunning,
	}

	// La ruta que este servicio se dejó en el arranque anterior se lee ANTES de
	// sobrescribir el Meta, porque es lo que la reconciliación necesita para
	// distinguir "mi ruta huérfana" de "una ruta viva de otro servicio".
	//
	// Se heredan los TRES hechos o ninguno. Copiar el nombre y el puerto pero no
	// la propiedad deja la concesión perdida en silencio en cada arranque, que es
	// exactamente el patrón que reopen el HIGH-B: un handle vivo que no concede
	// nada y una propiedad que nadie miró.
	if prev, err := req.Store.LoadMeta(req.Path); err == nil {
		base.RouteName = prev.RouteName
		base.RoutePort = prev.RoutePort
		base.RouteOwned = prev.RouteOwned
	}

	if mode != manifest.PortModeDynamic {
		// En fixed el puerto es el declarado y ya es el real, así que la ruta
		// se registra igual que en dynamic; en none no hay puerto y no hay nada
		// que apuntar. El manifiesto ya no deja pasar route_mode sin puerto
		// (Validate), así que esto sólo es una defensa.
		out := Result{Meta: base, Pid: res.Pid, Port: req.Manifest.Port}
		if mode == manifest.PortModeFixed {
			applyRoute(req, &base, req.Manifest.Port, &out)
		}
		if err := persistOrKill(req, base, res); err != nil {
			return Result{}, err
		}
		_ = req.Store.RegisterPid(req.Path, res.Pid, res.Pgid)
		return out, nil
	}
	return resolveDynamicPort(req, base, reserved)
}

// resolveDynamicPort es el tramo dynamic: el intento ya está en disco con el
// puerto reservado y el estado pendiente, y ahora toca descubrir el real.
func resolveDynamicPort(req Request, attempt state.Meta, reserved int) (Result, error) {
	// The attempt lands on disk BEFORE the discovery, carrying the reserved
	// port and the pending state: a concurrent tick reads this, never the
	// meta of the previous run whose CreationTimeMs no longer describes
	// anything.
	attempt.Port = reserved
	attempt.ReservedPort = reserved
	attempt.State = state.StatePortPending
	if err := persistOrKill(req, attempt, process.StartResult{Pid: attempt.Pid, Pgid: attempt.Pgid}); err != nil {
		return Result{}, err
	}
	_ = req.Store.RegisterPid(req.Path, attempt.Pid, attempt.Pgid)

	timeout := req.DiscoveryTimeout
	if timeout <= 0 {
		timeout = process.DefaultDynamicPortTimeout
	}
	d := process.DiscoverPort(attempt.Pid, reserved, req.Manifest.HealthURLPath(), timeout)
	if d.LineageDead {
		// El proceso ya no existe: el puerto reservado es un hueco
		// desperdiciado y hay que devolverlo al set.
		process.ReleasePort(reserved)
		return Result{}, fmt.Errorf("service exited during startup (no port to resolve)")
	}

	final := attempt
	final.Port = d.Port
	out := Result{Pid: attempt.Pid, Port: d.Port}

	switch {
	case d.Unresolved:
		// El plazo se agotó sin decidir. El proceso vive y puede que aún no
		// haya hecho bind: un Next.js que tarda 12s cae aquí. NO es
		// StateNoPort, porque ese afirma que no hay puerto y este afirma
		// que no lo sabemos. Presentarlo como "sin puerto" hacía que
		// displayPort cayera al puerto declarado y la tab Health sondeara
		// un puerto que nunca se confirmó — posiblemente el de otro worktree.
		final.State = state.StatePortUnresolved
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("service did not bind within %s; its port is unresolved, not absent — restart the service to retry discovery", timeout))
	case d.Port == 0 && len(d.All) == 0:
		// Sin puerto TCP con el linaje vivo: solo-UDP, worker, o una app
		// sin servidor. Es un estado, no un fallo de arranque.
		final.State = state.StateNoPort
		out.Warnings = append(out.Warnings,
			fmt.Sprintf("service has no TCP port (mode %q); port health checks are disabled",
				req.Manifest.EffectivePortMode()))
	default:
		final.State = state.StateRunning
		final.PortVerified = d.Verified
		if !d.HonoredReserved && reserved != d.Port {
			out.Warnings = append(out.Warnings,
				fmt.Sprintf("service ignored the offered port %d and bound %d instead", reserved, d.Port))
		}
		if !d.Verified {
			// R3: gana el menor, pero no hay forma de saber cuál es el
			// principal. Se declara, no se disimula.
			out.Warnings = append(out.Warnings, fmt.Sprintf(
				"service opened several ports %v and none answers health_path; picked %d as best guess (unverified port)",
				d.All, d.Port))
		}
	}

	out.Meta = final
	// La ruta se registra DESPUÉS del discovery y ANTES del SaveMeta final: el
	// puerto real ya está confirmado, y persistirla en el mismo Meta evita que
	// un Meta en disco afirme una ruta que nadie ha visto funcionar. Con el
	// puerto sin resolver no se registra nada, que es lo que hace imposible la
	// "ventana de 502" de la que habla el ADR.
	if final.State == state.StateRunning && final.Port > 0 {
		applyRoute(req, &final, final.Port, &out)
		out.Meta = final
	}
	if err := persistOrKill(req, final, process.StartResult{Pid: final.Pid, Pgid: final.Pgid}); err != nil {
		return Result{}, err
	}
	return out, nil
}

// applyRoute es el ÚNICO punto donde vroom habla con portless en el arranque.
// Reconcilia primero lo que este servicio se dejó antes, registra la ruta
// actual y deja el resultado en el Meta y en los avisos.
//
// Nada de esto puede fallar el arranque: si portless no está, no funciona, se
// cuelga, o su proxy no está en marcha, el servicio queda running en su puerto
// y el usuario recibe un aviso. La salud no depende de la ruta: una ruta es una
// dirección, no una dependencia.
func applyRoute(req Request, meta *state.Meta, port int, out *Result) {
	if req.Routes == nil {
		return // route_mode = "off": ni binario, ni shell, ni ruta
	}

	name, err := routeName(req)
	if err != nil {
		out.Warnings = append(out.Warnings, err.Error())
		return
	}

	// La reconciliación va ANTES del alta: sin ella, una rama renombrada
	// dejaría la ruta vieja apuntando a un puerto muerto para siempre, porque
	// `portless prune` no toca las rutas de alias (medido).
	out.Warnings = append(out.Warnings, req.Routes.Reconcile(meta.RouteName, portless.Ownership{Owned: meta.RouteOwned, Port: meta.RoutePort}, name)...)

	res := req.Routes.Apply(name, port, portless.Ownership{Owned: meta.RouteOwned, Port: meta.RoutePort})
	meta.RouteName = res.Name
	meta.RoutePort = port
	meta.RouteStatus = res.Status
	meta.RouteReason = res.Reason
	// Conceder la propiedad SÍ depende del resultado, y de qué resultado: se
	// concede con res.Registered —la escritura ocurrió—, no con
	// res.Succeeded(), que además exige verificación.
	//
	// HIGH-A: esto era `meta.RouteOwned = true` sin mirar `res`, de modo que un
	// alta que nunca ocurrió —por conflicto, o sin binario— acuñaba igual una
	// capacidad concedente, con el puerto pedido. Conceder con Succeeded()
	// habría sido el error opuesto: una ruta escrita con el proxy parado es
	// nuestra de verdad, y perder su handle haría que un reinicio con el puerto
	// movido chocara contra su propia ruta.
	meta.RouteOwned = res.Registered
	// La Url sólo se persiste si se ha VISTO responder. Un Meta en disco que
	// afirmara una URL sin verificar publicaría una dirección falsa a quien lo
	// leyera después.
	if res.Succeeded() {
		meta.RouteURL = res.Url
	} else {
		meta.RouteURL = ""
	}
	if warn := portless.Warn(res); warn != "" {
		out.Warnings = append(out.Warnings, warn)
	}
}

// persistOrKill guarda el meta del servicio recién arrancado, y si el guardado falla
// detiene al hijo antes de propagar el error.
//
// MEDIDO (bug): se devolvía `Result{}` sin detener al hijo. El caller recibía un error
// y NINGÚN PID, así que no tenía forma de pararlo: el proceso quedaba vivo sin
// registro, `vroom list` lo mostraba parado y el siguiente `vroom start` arrancaba un
// segundo por encima del mismo puerto. Lo detectó el guard de higiene de la suite, que
// cuenta los procesos de este binario que sobreviven a los tests.
//
// Se para antes de propagar el error porque el servicio no llegó a existir: sin meta
// no hay nada que gestionar, y un proceso que su gestor no conoce es peor que un
// arranque fallido.
//
// Y no se libera el puerto reservado, ni en dynamic: `Manager.Start` ya devolvió y el
// hijo lo estaba usando. Devolverlo al set reabriría la carrera de H1 mientras el hijo
// sigue ahí. La reserva se queda hasta el stop, como cualquier otra; parar el hijo y
// NO liberar el puerto es coherente, porque un puerto deservido un rato es el precio
// frente a un proceso zombi que lo ocupa para siempre.
//
// Los tres sitios que guardan después del spawn —el meta inicial de dynamic, el final
// de dynamic y el de fixed— comparten esta función. Antes eran tres copias del mismo
// bloque, y una copia es un sitio donde el arreglo del siguiente bug se aplica a dos y
// se olvida del tercero.
func persistOrKill(req Request, meta state.Meta, res process.StartResult) error {
	if err := req.Store.SaveMeta(req.Path, meta); err != nil {
		stopAfterPersistFailure(req, res)
		return err
	}
	return nil
}

// stopAfterPersistFailure detiene el hijo cuando la persistencia falla DESPUÉS del
// spawn.
//
// Existe porque los dos `SaveMeta` posteriores al spawn devuelven `Result{}`, sin PID: el
// caller no puede limpiar lo que no conoce. Un fallo de disco es raro pero el
// resultado sin pararlo es un proceso zombi que ocupa un puerto y que ninguna
// invocación posterior de `vroom` puede alcanzar.
//
// Best-effort a propósito: si el stop también falla, el error que se propaga es el de
// persistencia, que es el que el usuario tiene que arreglar. El zombi se vería en
// `ps` y el puerto ocupado se vería en el siguiente arranque.
func stopAfterPersistFailure(req Request, res process.StartResult) {
	if res.Pid <= 0 {
		return
	}
	_ = req.Manager.Stop(process.StopSpec{
		Pid:     res.Pid,
		Pgid:    res.Pgid,
		Timeout: process.DefaultStopTimeout,
	})
}

// routeName deriva el nombre de ruta del manifiesto, el proyecto y la rama.
func routeName(req Request) (string, error) {
	name, err := portless.DeriveName(
		req.Manifest.EffectiveRouteMode(), req.Manifest.RouteName, req.Branch, req.Manifest.Name)
	if err != nil {
		return "", fmt.Errorf("portless route: %w", err)
	}
	return name, nil
}
