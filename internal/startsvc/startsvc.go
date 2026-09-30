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
//  5. persistir el puerto real antes de devolver el control
package startsvc

import (
	"fmt"
	"time"

	"vroom/internal/manifest"
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

	if mode != manifest.PortModeDynamic {
		if err := req.Store.SaveMeta(req.Path, base); err != nil {
			return Result{}, err
		}
		_ = req.Store.RegisterPid(req.Path, res.Pid, res.Pgid)
		return Result{Meta: base, Pid: res.Pid, Port: req.Manifest.Port}, nil
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
	attempt.State = state.StatePortPending
	if err := req.Store.SaveMeta(req.Path, attempt); err != nil {
		process.ReleasePort(reserved)
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
	if err := req.Store.SaveMeta(req.Path, final); err != nil {
		return Result{}, err
	}
	return out, nil
}
