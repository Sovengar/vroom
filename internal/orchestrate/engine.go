package orchestrate

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"vroom/internal/gitinfo"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/startsvc"
	"vroom/internal/state"
)

// ResolvedService es un servicio resuelto contra los proyectos escaneados.
type ResolvedService struct {
	Name    string
	Project scanner.Project
}

// LaunchResult es el resultado de lanzar un stack.
type LaunchResult struct {
	OK     bool          `json:"ok"`
	Stack  string        `json:"stack"`
	Stages []StageResult `json:"stages"`
	Error  string        `json:"error,omitempty"`
}

// StageResult es el resultado de una etapa.
type StageResult struct {
	Name     string          `json:"name"`
	Services []ServiceResult `json:"services"`
}

// ServiceResult es el resultado de arrancar un servicio individual.
type ServiceResult struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	Pid    int    `json:"pid,omitempty"`
	// NoPort marca un servicio VIVO que no expone puerto TCP. No es un
	// error: no falla la etapa, no aborta el stack y no toca a sus
	// hermanos. Es el caso "solo UDP / worker / sin servidor".
	NoPort bool `json:"no_port,omitempty"`
	// PortUnresolved marca un servicio VIVO cuyo puerto no se pudo
	// decidir y cuyo discovery ya terminó. Tampoco es un error.
	PortUnresolved bool   `json:"port_unresolved,omitempty"`
	Error          string `json:"error,omitempty"`
}

// DryRunResult muestra el plan de ejecución sin ejecutar nada.
type DryRunResult struct {
	OK     bool          `json:"ok"`
	Stack  string        `json:"stack"`
	Stages []DryRunStage `json:"stages"`
}

// DryRunStage es una etapa en el plan de dry run.
type DryRunStage struct {
	Name     string   `json:"name"`
	Services []string `json:"services"`
}

// Engine orquesta el arranque de stacks de servicios.
type Engine struct {
	manager process.Manager
	store   *state.Store
}

// NewEngine crea un engine con el manager y store dados.
func NewEngine(manager process.Manager, store *state.Store) *Engine {
	return &Engine{manager: manager, store: store}
}

// LookupService resuelve un nombre de servicio a exactamente un proyecto.
// Manifest.Name no es identidad única (worktrees pueden repetirlo): ante
// duplicados devuelve un error explícito con los paths en orden estable,
// nunca un last-wins silencioso.
func LookupService(name string, projects []scanner.Project) (scanner.Project, error) {
	var matches []scanner.Project
	for _, p := range projects {
		if p.Configured && p.Manifest != nil && p.Manifest.Name == name {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return scanner.Project{}, fmt.Errorf("service %q not found in scanned projects", name)
	case 1:
		return matches[0], nil
	default:
		paths := make([]string, len(matches))
		for i, m := range matches {
			paths[i] = m.Path
		}
		sort.Strings(paths) // orden estable en el mensaje
		return scanner.Project{}, fmt.Errorf(
			"ambiguous service %q: found in %s; use unique manifest names",
			name, strings.Join(paths, ", "))
	}
}

// ResolveServices resuelve los nombres de servicio contra los proyectos
// escaneados. Devuelve error si algún nombre no se encuentra o si es
// ambiguo (varios proyectos lo declaran).
func (e *Engine) ResolveServices(serviceNames []string, projects []scanner.Project) ([]ResolvedService, error) {
	resolved := make([]ResolvedService, 0, len(serviceNames))
	for _, name := range serviceNames {
		p, err := LookupService(name, projects)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, ResolvedService{Name: name, Project: p})
	}
	return resolved, nil
}

// DryRun muestra el plan de ejecución sin ejecutar nada.
func (e *Engine) DryRun(stack *Stack, projects []scanner.Project) (*DryRunResult, error) {
	if _, err := e.validateServices(stack, projects); err != nil {
		return nil, err
	}
	result := &DryRunResult{OK: true, Stack: stack.Name}
	for _, stage := range stack.Stages {
		result.Stages = append(result.Stages, DryRunStage{
			Name:     stage.Name,
			Services: stage.Services,
		})
	}
	return result, nil
}

// Launch ejecuta la orquestación completa de un stack: etapas secuenciales,
// servicios paralelos dentro de cada etapa, health check, abort on failure.
//
// El error es SÓLO de resolución de nombres: una vez resueltos, un stage que falla
// no es un error de Go sino un `LaunchResult` con `OK: false` y su `Error` puesto,
// que es lo que el usuario lee. Quien ya tiene los nombres resueltos usa
// `LaunchResolved` y se ahorra el segundo `LookupService`.
func (e *Engine) Launch(stack *Stack, projects []scanner.Project) (*LaunchResult, error) {
	services, err := e.validateServices(stack, projects)
	if err != nil {
		return nil, err
	}
	return e.LaunchResolved(stack, services), nil
}

// LaunchResolved es `Launch` con los servicios ya resueltos, para el llamador que
// ya hizo la validación (la TUI valida cada stack antes de decidir, y volver a
// resolverlos allí era una segunda pasada entera por los mismos proyectos).
//
// No devuelve error porque no queda nada que pueda fallar: la resolución ya está
// hecha. Un fallo de arranque o de health check sigue siendo `LaunchResult.OK ==
// false`, igual que en `Launch`.
func (e *Engine) LaunchResolved(stack *Stack, services []ResolvedService) *LaunchResult {
	result := &LaunchResult{OK: true, Stack: stack.Name}
	var startedThisSession []string // paths de servicios arrancados por esta sesión
	var mu sync.Mutex

	for _, stage := range stack.Stages {
		sr := StageResult{Name: stage.Name}

		// Resolver servicios de esta etapa
		resolved := make([]ResolvedService, 0, len(stage.Services))
		for _, name := range stage.Services {
			for _, s := range services {
				if s.Name == name {
					resolved = append(resolved, s)
					break
				}
			}
		}

		// Arrancar en paralelo
		//
		// Los resultados van en un slice POSICIONAL y no en un mapa por nombre. Con
		// un mapa, un servicio nombrado dos veces en la misma etapa escribía dos
		// veces en la misma clave y el resumen perdía una línea; con el slice cada
		// arranque tiene su sitio y el `append` final no puede quedarse corto. Y
		// evita el `else` de "not started", que era inalcanzable: toda goroutine
		// escribe su posición y `wg.Wait()` no vuelve antes de que lo haga.
		var wg sync.WaitGroup
		var stageErr error
		var stageMu sync.Mutex
		resultados := make([]ServiceResult, len(resolved))

		for i, svc := range resolved {
			wg.Add(1)
			go func(i int, svc ResolvedService) {
				defer wg.Done()
				sr := e.startService(svc, stage.Timeout)

				mu.Lock()
				resultados[i] = sr
				if sr.Error != "" {
					stageMu.Lock()
					if stageErr == nil {
						stageErr = fmt.Errorf("service %q: %s", svc.Name, sr.Error)
					}
					stageMu.Unlock()
				}
				// MEDIDO (bug): el rollback se guiaba por `Action == "started"`,
				// pero startService devuelve Action VACÍO cuando el proceso
				// arrancó y el health check falló (ese camino devuelve
				// `ServiceResult{Error: ...}` a pelo). MEDIDO con un `sleep 300`
				// real: `Launch` devolvía OK=false, la etapa salía fallida y el
				// proceso SEGUÍA VIVO con el meta en "running". El siguiente
				// `vroom start` lo encontraba como already_running sobre un
				// servicio que no escucha, y el usuario no tenía forma de saber
				// de dónde salió ese proceso.
				//
				// La condición correcta es "hemos arrancado algo": el PID que
				// volvió. Un already_running devuelve Pid 0 y un fallo de start
				// también, así que ninguno entra al rollback, y un arranque
				// indicado cuya salud falla sí entra —que es justo lo que hay que
				// deshacer.
				if sr.Pid > 0 {
					startedThisSession = append(startedThisSession, svc.Project.Path)
				}
				mu.Unlock()
			}(i, svc)
		}
		wg.Wait()

		// Recopilar resultados de la etapa
		sr.Services = append(sr.Services, resultados...)
		result.Stages = append(result.Stages, sr)

		// Abort on failure
		if stageErr != nil {
			e.abortAndCleanup(startedThisSession)
			result.OK = false
			result.Error = stageErr.Error()
			return result
		}
	}
	return result
}

// LaunchAsync es la versión de Launch para la TUI: ejecuta la orquestación
// en una goroutine y devuelve un canal con el resultado.
func (e *Engine) LaunchAsync(stack *Stack, projects []scanner.Project) <-chan LaunchResult {
	ch := make(chan LaunchResult, 1)
	go func() {
		result, err := e.Launch(stack, projects)
		if err != nil {
			ch <- LaunchResult{OK: false, Stack: stack.Name, Error: err.Error()}
		} else {
			ch <- *result
		}
		close(ch)
	}()
	return ch
}

// StopStack para todos los servicios de un stack. Resuelve cada nombre
// con el mismo criterio explícito que el engine: ante duplicados falla en
// vez de parar un proyecto arbitrario.
func (e *Engine) StopStack(stack *Stack, projects []scanner.Project) error {
	services, err := e.validateServices(stack, projects)
	if err != nil {
		return err
	}
	e.StopResolved(services)
	return nil
}

// StopResolved para unos servicios de stack que ya están resueltos, para el
// llamador que validó antes. No devuelve error porque no queda nada que pueda
// fallar: la resolución de nombres es la única fuente de error de una parada, y ya
// está hecha. También por eso no necesita el `*Stack`: cada servicio se para una
// vez y el resto del stack ya no importa.
func (e *Engine) StopResolved(services []ResolvedService) {
	for _, svc := range services {
		e.stopService(svc.Project)
	}
}

// StackStatus evalúa el estado de todos los servicios de un stack con el
// mismo criterio de resolución que el CLI (LookupService): ante un nombre
// ambiguo devuelve error en lugar de elegir arbitrariamente el primero.
func (e *Engine) StackStatus(stack *Stack, projects []scanner.Project) (running, total int, err error) {
	seen := make(map[string]bool)
	for _, stage := range stack.Stages {
		for _, name := range stage.Services {
			if seen[name] {
				continue
			}
			seen[name] = true
			total++
			p, lookupErr := LookupService(name, projects)
			if lookupErr != nil {
				return running, total, lookupErr
			}
			meta, metaErr := e.store.LoadMeta(p.Path)
			if metaErr == nil && meta.Pid > 0 {
				status := e.manager.Evaluate(process.EvalSpec{
					Pid:            meta.Pid,
					CreationTimeMs: meta.CreationTimeMs,
					Port:           meta.Port,
					ProcessPattern: meta.ProcessPattern,
					PortPending:    meta.State == state.StatePortPending,
					PortUnresolved: meta.State == state.StatePortUnresolved,
					NoPort:         meta.State == state.StateNoPort,
				})
				if status == process.StatusRunning {
					running++
				}
			}
		}
	}
	return running, total, nil
}

// validateServices resuelve y valida todos los servicios del stack.
func (e *Engine) validateServices(stack *Stack, projects []scanner.Project) ([]ResolvedService, error) {
	allNames := make(map[string]bool)
	for _, stage := range stack.Stages {
		for _, name := range stage.Services {
			allNames[name] = true
		}
	}
	names := make([]string, 0, len(allNames))
	for name := range allNames {
		names = append(names, name)
	}
	sort.Strings(names) // orden estable: mensajes/errores reproducibles
	return e.ResolveServices(names, projects)
}

// processAlive dice si el estado implica un proceso en pie. Deliberadamente
// NO usa uiStatus.alive() del TUI: son paquetes distintos y este valor
// decide si se reinicia el servicio, así que se define una sola vez aquí.
func processAlive(s process.Status) bool {
	switch s {
	case process.StatusRunning, process.StatusPortPending,
		process.StatusNoPort, process.StatusPortUnresolved:
		return true
	default:
		return false
	}
}

// startService arranca un servicio: si ya está running, solo verifica health.
func (e *Engine) startService(svc ResolvedService, timeout time.Duration) ServiceResult {
	p := svc.Project

	// Verificar si ya está corriendo
	meta, err := e.store.LoadMeta(p.Path)
	if err == nil && meta.Pid > 0 {
		status := e.manager.Evaluate(process.EvalSpec{
			Pid:            meta.Pid,
			CreationTimeMs: meta.CreationTimeMs,
			Port:           meta.Port,
			ProcessPattern: meta.ProcessPattern,
			PortPending:    meta.State == state.StatePortPending,
			PortUnresolved: meta.State == state.StatePortUnresolved,
			NoPort:         meta.State == state.StateNoPort,
		})
		if processAlive(status) {
			// Ya corriendo: verificar salud y continuar. Los estados de
			// puerto (pending / no_port / port_unresolved) también cuentan
			// como "vive": todos significan proceso en pie con el puerto sin
			// cerrar. Descartarlos reiniciaba un servicio sano y gastaba una
			// ventana completa de discovery en cada launch.
			outcome, err := e.awaitPortOutcome(p, meta, timeout)
			if err != nil {
				return ServiceResult{Name: svc.Name, Error: fmt.Sprintf("health check failed: %v", err)}
			}
			return portServiceResult(svc.Name, "already_running", 0, outcome)
		}
	}

	// Arrancar
	if _, err := e.store.EnsureServiceDir(p.Path); err != nil {
		return ServiceResult{Name: svc.Name, Error: err.Error()}
	}
	out, err := startsvc.Start(startsvc.Request{
		Manifest:   p.Manifest,
		Path:       p.Path,
		Store:      e.store,
		Manager:    e.manager,
		StdoutPath: e.store.StdoutLog(p.Path),
		StderrPath: e.store.StderrLog(p.Path),
		Routes:     startsvc.RegistrarFor(p.Manifest),
		Branch:     gitinfo.Branch(p.Path),
	})
	if err != nil {
		return ServiceResult{Name: svc.Name, Error: err.Error()}
	}
	for _, w := range out.Warnings {
		_ = appendLine(e.store.StderrLog(p.Path), "── vroom ▶ start: "+w)
	}

	// Health check sobre el puerto REAL, con el modo que lo gobierna.
	outcome, err := e.awaitPortOutcome(p, out.Meta, timeout)
	if err != nil {
		// MEDIDO (bug): el PID y el Action se reportan TAMBIÉN cuando la salud
		// falla. Antes se devolvía `ServiceResult{Error: ...}` a pelo, con Pid 0,
		// y el rollback de Launch —que se guiaba por si el servicio se había
		// arrancado— no lo incluía. MEDIDO con un `sleep 300` real: `Launch`
		// devolvía OK=false y el proceso SEGUÍA VIVO con el meta en "running".
		//
		// Ahora el resultado dice las dos cosas, que son distintas: action/pid
		// describen qué pasó con el PROCESO (arrancó, y hay que deshacerlo), y
		// error describe por qué la ETAPA no puede darse por buena. Un consumidor
		// que sólo mire action sabe que hay un proceso detrás del error.
		r := portServiceResult(svc.Name, "started", out.Pid, outcome)
		r.Error = fmt.Sprintf("health check failed: %v", err)
		return r
	}

	return portServiceResult(svc.Name, "started", out.Pid, outcome)
}

// portServiceResult proyecta el veredicto no-fatal sobre el resultado del
// servicio. Los dos casos sin puerto son marcas, no errores: son la razón por
// la que un servicio vivo no puede pasar por sano, y la razón por la que
// tampoco debe tumbar la etapa.
func portServiceResult(name, action string, pid int, outcome PortOutcome) ServiceResult {
	return ServiceResult{
		Name:           name,
		Action:         action,
		Pid:            pid,
		NoPort:         outcome == PortNone,
		PortUnresolved: outcome == PortUnresolved,
	}
}

// PortOutcome es el veredicto no-fatal del gate de salud de un servicio.
// Hay tres, no dos: un servicio vivo puede estar sano, no exponer puerto TCP,
// o tener un puerto que nadie ha podido decidir. Los dos últimos NO son
// fallos y no pueden tumbar la etapa.
type PortOutcome int

const (
	// PortResolved: el puerto está decidido y escuchando.
	PortResolved PortOutcome = iota
	// PortNone: el servicio vive y no expone puerto TCP.
	PortNone
	// PortUnresolved: el servicio vive y su puerto no se pudo decidir; el
	// discovery ya terminó.
	PortUnresolved
)

// awaitPortOutcome gatea la salud de una etapa y traduce el veredicto a un
// resultado. El puerto viene del meta (el real), no del manifiesto (el
// default de la app).
//
// Los tres estados persistidos se mapean uno a uno, y dos de ellos son
// resultados, no errores:
//
//	no_port         → PortNone,       no fatal (vivo y sin puerto TCP)
//	port_unresolved → PortUnresolved, no fatal (vivo, puerto sin decidir)
//	port_pending    → ErrPortPending, SÍ fatal: discovery en vuelo
//
// Tratarlos como fallo encadenaba hasta abortAndCleanup, que paraba a los
// hermanos ya arrancados en la misma launch. ErrPortPending sigue siendo
// fallo de verdad: una etapa no puede darse por buena con el discovery en
// curso. Lo terminal no.
func (e *Engine) awaitPortOutcome(p scanner.Project, meta state.Meta, timeout time.Duration) (PortOutcome, error) {
	err := AwaitPort(PortWait{
		Port:        meta.Port,
		Mode:        p.Manifest.EffectivePortMode(),
		PortPending: meta.State == state.StatePortPending,
		NoPort:      meta.State == state.StateNoPort,
		Unresolved:  meta.State == state.StatePortUnresolved,
	}, timeout)
	switch {
	case errors.Is(err, ErrNoPort):
		return PortNone, nil
	case errors.Is(err, ErrPortUnresolved):
		return PortUnresolved, nil
	default:
		return PortResolved, err
	}
}

// stopService para un servicio individual.
func (e *Engine) stopService(p scanner.Project) {
	if p.Manifest == nil {
		return
	}
	// Parada graciosa no implementada en el engine: p.Manifest.Stop se
	// ignora intencionadamente (simplified stop; aquí no hay runLogged).
	meta, err := e.store.LoadMeta(p.Path)
	if err == nil {
		if meta.Pid > 0 || meta.Pgid > 0 || meta.Port > 0 {
			e.stopProcess(p.Path, &meta)
		} else {
			// Un servicio que ya estaba muerto (Pid 0) no pasa por stopProcess,
			// pero también deja una ruta detrás: se retira igualmente, fuera del
			// guard.
			e.releaseRouteOnStop(&meta)
		}
	}
	_ = e.store.ClearPid(p.Path)
	if err == nil {
		meta.State = state.StateStopped
		meta.Pid = 0
		meta.Pgid = 0
		meta.ReservedPort = 0
		_ = e.store.SaveMeta(p.Path, meta)
	}
}

// abortAndCleanup para todos los servicios arrancados en esta sesión.
func (e *Engine) abortAndCleanup(paths []string) {
	for _, path := range paths {
		meta, err := e.store.LoadMeta(path)
		if err == nil {
			if meta.Pid > 0 || meta.Pgid > 0 || meta.Port > 0 {
				e.stopProcess(path, &meta)
			} else {
				// Ídem stopService: la ruta se retira aunque el servicio ya
				// estuviese muerto y por tanto no pasa por stopProcess.
				e.releaseRouteOnStop(&meta)
			}
		}
		_ = e.store.ClearPid(path)
		if err == nil {
			meta.State = state.StateStopped
			meta.Pid = 0
			meta.Pgid = 0
			meta.ReservedPort = 0
			_ = e.store.SaveMeta(path, meta)
		}
	}
}

// stopProcess detiene el proceso y deja rastro de los avisos: el guard de
// propiedad del puerto falla cerrado, y un aviso silencioso se lee como que
// el puerto quedó libre cuando no lo está.
//
// Cubre stopService y abortAndCleanup, que es justo lo que hace falta: el
// rollback de un arranque fallido también consume reservas.
// El seam de retirada del engine.
//
// Existe porque sin él la retirada no la observaba nadie: el reviewer comprobó
// que borrando los tres call sites de Release la suite seguía en verde, así que
// la decisión 13 del ADR —"se retira en los tres caminos"— no la verificaba
// nada. En producción ambas están a cero y sale el cliente real.
var (
	engineReleaseStub          portless.ReleaserFunc
	engineReleaseStubInstalled bool
)

func engineRouteReleaser() portless.Releaser {
	if engineReleaseStubInstalled && engineReleaseStub != nil {
		return engineReleaseStub
	}
	if portless.IsTestBinary() {
		return portless.InertReleaser()
	}
	return nil
}

func (e *Engine) stopProcess(path string, meta *state.Meta) {
	var warns []string
	_ = e.manager.Stop(process.StopSpec{
		Pid: meta.Pid, Pgid: meta.Pgid, Port: meta.Port,
		Timeout: process.DefaultStopTimeout,
		Warn:    func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) },
	})
	// El servicio ya está parado: su reserva vuelve al pool.
	process.ReleasePort(meta.ReservedPort)
	// Y su ruta deja de existir. El fallo es benigno (quitar lo que no está
	// sale con 1 y un stop repetido no es un error), y esto cubre tanto
	// stopService como abortAndCleanup.
	e.releaseRouteOnStop(meta)
	for _, w := range warns {
		_ = appendLine(e.store.StderrLog(path), "── vroom ▶ stop: "+w)
	}
}

// releaseRouteOnStop retira la ruta y REVOCA la propiedad si la retirada
// surtió efecto. Muta el Meta; quien lo persiste es el SaveMeta del llamador.
//
// Existe como función única porque los tres caminos de parada del engine
// necesitan exactamente lo mismo, y porque una de las ramas —el servicio ya
// muerto en abortAndCleanup— estaba llamando a `portless.Release(nil, ...)`
// con el cliente real hardcodeado, que ningún test podía observar. Una ruta que
// ese camino dejaba era, precisamente, la que este HIGH hacía posible pisar.
//
// SOLO retira si la propiedad está CONCEDIDA: el handle sobrevive a la
// revocación para que la reconciliación tenga dónde mirar, así que usarlo como
// autoridad de borrado borraría rutas ajenas.
func (e *Engine) releaseRouteOnStop(meta *state.Meta) {
	if !meta.RouteOwned {
		return // nunca fue nuestra: no se toca nada
	}
	if !portless.Release(engineRouteReleaser(), meta.RouteName) {
		return // la retirada no surtió efecto: no se revoca nada
	}
	meta.RouteOwned = false
}

// appendLine añade una línea al log de un servicio.
func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(line + "\n")
	return err
}
