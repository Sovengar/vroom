// Package cli implementa la interfaz de línea de comandos de vroom para
// consumo por IA: todos los comandos devuelven JSON en stdout y errores
// en stderr con formato {"error":"..."}.
//
// Comandos:
//
//	vroom                    → lanza la TUI (comportamiento por defecto)
//	vroom list               → lista todos los proyectos con estado completo
//	vroom start <name>       → arranca un servicio daemonizado
//	vroom stop <name>        → detiene un servicio
//	vroom build <name>       → ejecuta command_build (one-shot síncrono)
//	vroom install <name>     → ejecuta command_install (one-shot síncrono)
//	vroom logs <name>        → muestra los logs del servicio
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"vroom/internal/config"
	"vroom/internal/gitinfo"
	"vroom/internal/manifest"
	"vroom/internal/orchestrate"
	"vroom/internal/portless"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/startsvc"
	"vroom/internal/state"
	"vroom/internal/tail"
)

// ---- JSON output types ----

// ListResult es la respuesta de `vroom list`.
type ListResult struct {
	Projects []ProjectInfo `json:"projects"`
}

// ProjectInfo contiene toda la información de un proyecto para consumo
// externo (IA).
type ProjectInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Configured bool   `json:"configured"`
	Status     string `json:"status"`
	// Port es el puerto REAL: el que vroom confirmó que el proceso escucha,
	// o 0 cuando no hay ninguno confirmado. Nunca es el declarado: un puerto
	// que vroom no confirmó puede ser el del twin de otro worktree, y un
	// agente que lo lea se disconnecta del sitio equivocado.
	Port int `json:"port"`
	// DeclaredPort es lo que dice el manifiesto: el puerto por DEFECTO de la
	// app (PORT=${PORT:-N}). Se publica aparte para que "no hay puerto real"
	// no se confunda con "no hay puerto en el manifiesto", y para no perder
	// la información al quitar el fallback de Port.
	DeclaredPort int    `json:"declared_port,omitempty"`
	PortMode     string `json:"port_mode,omitempty"`
	// PortVerified es tri-estado a propósito, y por eso es *bool:
	//   ausente → la fila no está configurada o su manifiesto no parseó, así
	//            que no hay contrato de puerto que afirmar;
	//   false   → hay contrato de puerto y vroom NO confirmó ninguno. Esto
	//            es lo que un `bool` con omitempty hacía imposible de emitir.
	//   true    → el puerto publicado está confirmado contra un listener real.
	PortVerified *bool `json:"port_verified,omitempty"`
	// RouteMode es la INTENCIÓN, tal como PortMode: lo que dice el manifiesto,
	// no lo que<vroom> consiguió. Se publica aunque la ruta se degradara, para
	// que un agente pueda distinguir "no se pidió ruta" de "se pidió y falló".
	// Y se OMITE cuando no hay contrato de ruta, para que un manifiesto legacy
	// produzca exactamente el mismo JSON que antes de este campo.
	RouteMode string `json:"route_mode,omitempty"`
	// Route es el RESULTADO, y es un puntero porque el AUSENTE también es un
	// estado: un manifiesto sin contrato de ruta no afirma ni niega nada. Misma
	// lección que PortVerified, y por eso *RouteInfo y no un valor con
	// omitempty.
	Route          *RouteInfo `json:"route,omitempty"`
	Command        string     `json:"command,omitempty"`
	CommandStop    string     `json:"command_stop,omitempty"`
	CommandBuild   string     `json:"command_build,omitempty"`
	CommandInstall string     `json:"command_install,omitempty"`
	ProcessPattern string     `json:"process_pattern,omitempty"`
	GitBranch      string     `json:"git_branch,omitempty"`
	PrimaryGroup   string     `json:"primary_group,omitempty"`
	SecondaryGroup string     `json:"secondary_group,omitempty"`
	RepoRoot       string     `json:"repo_root,omitempty"`
	IsWorktree     bool       `json:"is_worktree,omitempty"`
	BareContainer  bool       `json:"bare_container,omitempty"`
	WorktreeErr    string     `json:"worktree_error,omitempty"`
	Collapsed      bool       `json:"collapsed"`
	Pid            int        `json:"pid,omitempty"`
	Pgid           int        `json:"pgid,omitempty"`
	StartedAt      string     `json:"started_at,omitempty"`
	ManifestError  string     `json:"manifest_error,omitempty"`
}

// RouteInfo es el resultado de la ruta, tal como lo lee un agente.
//
// Tri-estado y por construcción: Name siempre (es el nombre PRETENDIDO, haya
// éxito o no — nunca una url, que es lo que un agente intentaría abrir),
// Status siempre, Url SÓLO si se ha visto funcionar, Reason sólo al degradar.
//
// Una URL que nadie verificó no se publica. Es la lección de port_verified
// aplicada entera: un campo que afirma una dirección falsa es peor que un
// campo ausente, porque el agente que lo lea se conecta a otra cosa.
type RouteInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"` // registered | degraded
	Url    string `json:"url,omitempty"`
	Reason string `json:"reason,omitempty"`
	Port   int    `json:"port,omitempty"`
}

// ActionResult es la respuesta de start/stop/build/install.
type ActionResult struct {
	OK       bool   `json:"ok"`
	Project  string `json:"project"`
	Action   string `json:"action"`
	Pid      int    `json:"pid,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Elapsed  string `json:"elapsed,omitempty"`
	Error    string `json:"error,omitempty"`
}

// LogsResult es la respuesta de `vroom logs`.
type LogsResult struct {
	Project string `json:"project"`
	Stdout  string `json:"stdout,omitempty"`
	Stderr  string `json:"stderr,omitempty"`
}

// ErrorResult es el formato estándar de error.
type ErrorResult struct {
	Error string `json:"error"`
}

// ---- Helpers ----

// outputJSON escribe el contrato de salida en w.
//
// El writer es un PARÁMETRO, no un global: la salida de este paquete es su
// contrato con los agentes, así que es lo último que puede quedar atado a un
// recurso del proceso. Con `os.Stdout` dentro, ningún comando se puede ejercer
// en un test —habría que sustituir el descriptor del proceso entero para leer lo que
// escribió— y los nueve command* se quedaban sin cubrir sin que nadie lo
// notara: no es que fueran difíciles de probar, es que no se podían probar.
func outputJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// outputError escribe el contrato de error. NO sale: devuelve el código para que
// lo interprete el caller.
//
// Que el código de salida sea un valor de retorno y no un os.Exit en medio del
// paquete es lo que permite probarlo, y también lo que deja la salida en un
// único sitio: main decide, el paquete informa. Un os.Exit aquí convertía cada
// ruta de error en una sentencia que ningún test podía ejecutar.
func outputError(w io.Writer, msg string) int {
	_ = json.NewEncoder(w).Encode(ErrorResult{Error: msg})
	return 1
}

func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.WriteString(line + "\n")
	return err
}

func resolveRoot() string {
	cfg := config.Load()
	root, _ := os.Getwd()
	if cfg.Scanner.Root != "" {
		scanRoot := cfg.Scanner.Root
		if strings.HasPrefix(scanRoot, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				scanRoot = filepath.Join(home, scanRoot[1:])
			}
		}
		if !filepath.IsAbs(scanRoot) {
			scanRoot = filepath.Join(root, scanRoot)
		}
		return scanRoot
	}
	return root
}

func loadConfig() config.Config {
	return config.Load()
}

// findProject busca un proyecto por path (direccionador canónico) o por
// nombre manifest. query puede ser un nombre o una ruta absoluta; path
// (del flag --path) tiene prioridad y desambigua. Primero busca por
// nombre del manifiesto; si no encuentra, intenta por nombre del
// directorio. Devuelve error accionable si hay ambigüedad.
func findProject(projects []scanner.Project, query, path string) (scanner.Project, error) {
	if path != "" {
		return findByPath(projects, path)
	}
	if filepath.IsAbs(query) {
		return findByPath(projects, query)
	}

	var matches []scanner.Project
	for _, p := range projects {
		if p.Configured && p.Manifest != nil && p.Manifest.Name == query {
			matches = append(matches, p)
		}
	}
	// Fallback: buscar por nombre del directorio
	if len(matches) == 0 {
		for _, p := range projects {
			if p.Name == query {
				matches = append(matches, p)
			}
		}
	}
	switch len(matches) {
	case 0:
		return scanner.Project{}, fmt.Errorf("project not found: %s", query)
	case 1:
		return matches[0], nil
	default:
		paths := make([]string, len(matches))
		for i, m := range matches {
			paths[i] = m.Path
		}
		sort.Strings(paths) // orden estable para el mensaje
		return scanner.Project{}, fmt.Errorf(
			"ambiguous project name %q: found in %s; use --path to disambiguate",
			query, strings.Join(paths, ", "))
	}
}

// findByPath resuelve un proyecto por su ruta absoluta exacta. Normaliza
// ambos lados (abs, clean y symlinks resueltos) para que un path con
// symlink apunte al proyecto correcto; si el path no existe cae a su forma
// absoluta/limpia y devuelve "project not found" como antes.
func findByPath(projects []scanner.Project, path string) (scanner.Project, error) {
	abs := normalizePath(path)
	for _, p := range projects {
		if normalizePath(p.Path) == abs {
			return p, nil
		}
	}
	return scanner.Project{}, fmt.Errorf("project not found: %s", path)
}

// normalizePath devuelve la ruta absoluta, limpia y con symlinks resueltos
// cuando es posible; si la ruta no existe (o falla la resolución) usa la
// forma absoluta/limpia.
func normalizePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(abs)
}

// extractPathFlag separa el flag --path <valor> (o --path=<valor>) de los
// demás argumentos (posicionales y otros flags, p.ej. --tail/--stream de
// logs). Devuelve error si --path aparece sin valor, con valor vacío o más
// de una vez: el comportamiento es predecible en vez de ignorarlo en
// silencio.
func extractPathFlag(args []string) (rest []string, path string, err error) {
	rest = make([]string, 0, len(args))
	seen := false
	set := func(val string) error {
		if seen {
			return fmt.Errorf("--path specified more than once")
		}
		if val == "" {
			return fmt.Errorf("--path requires a non-empty value")
		}
		seen = true
		path = val
		return nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--path" {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return nil, "", fmt.Errorf("--path requires a value")
			}
			if err := set(args[i+1]); err != nil {
				return nil, "", err
			}
			i++
			continue
		}
		if val, ok := strings.CutPrefix(a, "--path="); ok {
			if err := set(val); err != nil {
				return nil, "", err
			}
			continue
		}
		rest = append(rest, a)
	}
	return rest, path, nil
}

// evaluateStatus devuelve el estado evaluado de un proyecto.
func evaluateStatus(manager process.Manager, store *state.Store, path string) (string, state.Meta) {
	meta, err := store.LoadMeta(path)
	if err != nil {
		return state.StateStopped, state.Meta{}
	}
	status := manager.Evaluate(process.EvalSpec{
		Pid:            meta.Pid,
		CreationTimeMs: meta.CreationTimeMs,
		Port:           meta.Port,
		ProcessPattern: meta.ProcessPattern,
		// Los tres estados de puerto se pasan desde el meta persistido. Sin
		// esto la TUI y el JSON cuentan historias distintas sobre el mismo
		// servicio: el mismo meta leía "port_unresolved" en una y "running"
		// en la otra.
		PortPending:    meta.State == state.StatePortPending,
		PortUnresolved: meta.State == state.StatePortUnresolved,
		NoPort:         meta.State == state.StateNoPort,
	})
	return string(status), meta
}

// buildProjectInfo construye ProjectInfo desde un scanner.Project.
func buildProjectInfo(manager process.Manager, store *state.Store, collapsed map[string]bool, p scanner.Project) ProjectInfo {
	info := ProjectInfo{
		Name:          p.Name,
		Path:          p.Path,
		Configured:    p.Configured,
		Status:        state.StateStopped,
		GitBranch:     gitinfo.Branch(p.Path),
		RepoRoot:      p.RepoRoot,
		IsWorktree:    p.IsWorktree,
		BareContainer: p.IsBareContainer,
		WorktreeErr:   p.WorktreeErr,
	}

	if !p.Configured {
		info.Status = state.StateStopped
		if p.ManifestErr != "" {
			info.ManifestError = p.ManifestErr
		}
		return info
	}

	m := p.Manifest
	info.Command = m.Command
	info.CommandStop = m.Stop
	info.CommandBuild = m.Build
	info.CommandInstall = m.Install
	info.ProcessPattern = m.ProcessPattern
	info.PrimaryGroup = m.PrimaryGroup
	info.SecondaryGroup = m.SecondaryGroup
	info.PortMode = m.EffectivePortMode()
	// route_mode se publica como INTENCIÓN, incluso degradada: es lo que dice
	// el manifiesto. El resultado va aparte, en Route. Con route_mode = "off"
	// no se publica nada: un manifiesto que no declaró ruta no afirma ni niega
	// que tenga una, y eso es distinto de afirmar que no la tiene.
	if m.EffectiveRouteMode() != manifest.RouteModeOff {
		info.RouteMode = m.EffectiveRouteMode()
	}

	// Colapso: clave = primary o primary/secondary
	if m.PrimaryGroup != "" {
		key := m.PrimaryGroup
		if m.SecondaryGroup != "" {
			key = m.PrimaryGroup + "/" + m.SecondaryGroup
		}
		info.Collapsed = collapsed[key]
	}

	status, meta := evaluateStatus(manager, store, p.Path)
	info.Status = status
	if meta.Pid > 0 {
		info.Pid = meta.Pid
		info.Pgid = meta.Pgid
		info.StartedAt = meta.StartedAt
	}

	// El puerto se resuelve DESPUÉS de evaluateStatus: meta es el estado real
	// del servicio. Asignarlo antes haría que el JSON emita siempre el puerto
	// declarado, que es exactamente el bug que este cambio elimina.
	//
	// Y no hay fallback al declarado para un servicio vivo: 0 es la verdad
	// y el declarado vive en declared_port. Para uno parado se conserva,
	// porque entonces es la única información que hay.
	info.DeclaredPort = m.Port
	if meta.Pid > 0 {
		info.Port = meta.Port
		info.PortVerified = boolPtr(meta.PortVerified)
	} else {
		info.Port = m.Port
	}

	// El objeto de ruta sólo existe si hay CONTRATO de ruta. Un manifiesto sin
	// route_mode no afirma ni niega nada sobre rutas, que es distinto de
	// afirmar que no hay.
	//
	// Y sale del Meta persistido, no de una comprobación en vivo: el JSON no
	// shellea a portless en cada `vroom list`. Por eso lo que se afirma es
	// "último estado conocido", y por eso una ruta degradada no trae url.
	if info.RouteMode != "" {
		r := RouteInfo{Name: meta.RouteName, Port: meta.RoutePort}
		switch meta.RouteStatus {
		case portless.StatusRegistered:
			r.Status = portless.StatusRegistered
			r.Url = meta.RouteURL
		case portless.StatusDegraded:
			r.Status = portless.StatusDegraded
			r.Reason = meta.RouteReason
		}
		// RouteStatus vacío = nunca se intentó (o el servicio nunca arrancó):
		// no hay resultado que afirmar, y un objeto con status vacío sería un
		// contrato que el JSON no cumple.
		if r.Status != "" {
			info.Route = &r
		}
	}

	return info
}

func boolPtr(b bool) *bool { return &b }

// cliRouteReleaser devuelve el seam de retirada, o nil para que portless
// construya el cliente real.
//
// El punto de inyección existe porque sin él la retirada de la CLI no la
// observaba nadie: el reviewer comprobó que borrando los tres call sites de
// Release la suite seguía en verde, de modo que la decisión 13 del ADR —"se
// retira en los tres caminos"— no la verificaba nada. En producción ambas
// variables están a cero y sale el camino real.
var (
	cliReleaseStub          portless.ReleaserFunc
	cliReleaseStubInstalled bool
)

func cliRouteReleaser() portless.Releaser {
	if cliReleaseStubInstalled && cliReleaseStub != nil {
		return cliReleaseStub
	}
	// LOW-3: sin stub, en un binario de test, `nil` construiría el cliente real
	// y resolvería el portless y el state dir del desarrollador. Un test que se
	// olvide de instalar el seam no debe poder mutar su routes.json real. La
	// comprobación vive en portless porque eran tres copias iguales.
	if portless.IsTestBinary() {
		return portless.InertReleaser()
	}
	return nil
}

// releaseRouteOnStop retira la ruta de un servicio parado y REVOCA la
// propiedad si la retirada surtió efecto. Muta el Meta; quien lo persiste es el
// SaveMeta del stop, que va justo después.
//
// SOLO retira si la propiedad está CONCEDIDA. El handle (RouteName/RoutePort)
// sobrevive a la revocación a propósito —para que la reconciliación tenga dónde
// mirar—, así que usarlo como autoridad de borrado es el mismo error que se
// corrigió en Reconcile: con una ruta ajena ocupando el nombre y un alta que
// chocó con ella, el stop la borraba. Reproducido contra portless real antes de
// este guard.
//
// Va FUERA del guard de proceso a propósito: un servicio que ya estaba muerto
// cuando se paró (Pid 0) también deja una ruta detrás.
func releaseRouteOnStop(meta *state.Meta) {
	if !meta.RouteOwned {
		return // nunca fue nuestra: no se toca nada
	}
	if !portless.Release(cliRouteReleaser(), meta.RouteName) {
		return // la retirada no surtió efecto: no se revoca nada
	}
	meta.RouteOwned = false
}

// ---- Commands ----

// Run es el punto de entrada del CLI. Devuelve true si manejó un
// subcomando (el caller debe salir); false si debe lanzar la TUI.
//
// El reparto es deliberado: Run sólo reparte y EMITE, y cada cmd* devuelve el
// valor a publicar. Antes los comandos escribían ellos mismos y llamaban a
// os.Exit en caso de error, con lo que ninguno podía ejecutarse en un test sin
// matar el binario de test. Ahora el error viaja como valor y el proceso se
// cierra en un solo sitio —main— que es donde esa decisión pertenece.
// El `exit` va por parámetro y no como un `var` global intercambiable por la misma
// razón que en `cmd/vroom`: una llamada a `os.Exit` mata el proceso de test, así que
// la única forma de comprobar que se pide el código correcto es poder inyectar la
// salida. Con un `var`, cualquier test que lo tocara contaminaría a los que corren
// en el mismo proceso; con un parámetro, cada test ve sólo el suyo.
func Run(args []string, exit func(int)) bool {
	handled, code := runInto(os.Stdout, os.Stderr, args)
	if handled && code != 0 {
		exit(code)
	}
	return handled
}

// runInto es Run con los writers y el código de salida como valores.
//
// La separación es lo que hace que el contrato entero sea comprobable: stdout es
// la RESPUESTA y stderr el DIAGNÓSTICO, y un test puede exigir que un error vaya
// a stderr y no a stdout. Con os.Stdout y os.Exit dentro, las nueve rutas de
// error del CLI eran sentencias que ningún test podía ejecutar.
//
// Y devuelve `handled` aparte del código porque son dos preguntas distintas: si
// no hubo subcomando hay que lanzar la TUI, y eso no es un error ni un éxito.
func runInto(stdout, stderr io.Writer, args []string) (handled bool, code int) {
	payload, handled, err := dispatch(args)
	if !handled {
		return false, 0
	}
	if err != nil {
		return true, outputError(stderr, err.Error())
	}
	if err := outputJSON(stdout, payload); err != nil {
		// Un stdout que no acepta el JSON no es un comando que "no hizo nada":
		// el agente recibiría una respuesta vacía y la leería como que no hay
		// proyectos. Sale con 1 y lo dice en el mismo contrato.
		return true, outputError(stderr, "could not write response: "+err.Error())
	}
	return true, 0
}

// dispatch reparte un subcomando y devuelve (payload, manejado, error).
//
// Devolver el error en vez de emitirlo es lo que hace testeable el reparto: los
// nueve comandos tienen caminos de error REALES —proyecto no encontrado, no
// configurado, escaneo fallido, comando one-shot ausente— y son justo los que
// nunca se ejercitaban porque cada uno terminaba el proceso.
//
// Los errores de --path y de uso se comprueban AQUÍ y no en cada cmd*, porque
// son de la FORMA de la invocación: no dependen de ningún comando, y duplicar
// el parseo nueve veces sólo multiplicaría la superficie a la que hay que
// mirar.
func dispatch(args []string) (payload any, handled bool, err error) {
	if len(args) == 0 {
		return nil, false, nil
	}

	cmd := args[0]

	// Un comando con flag --path comparte el mismo parseo y el mismo contrato de
	// error. La tabla es lo que evita nueve copias del mismo bloque.
	switch cmd {
	case "start", "stop", "build", "install", "logs":
		usage := "usage: vroom " + cmd + " <project-name|path> [--path <path>]"
		if cmd == "logs" {
			usage += " [--tail N --stream merged|stdout|stderr]"
		}
		rest, path, perr := extractPathFlag(args[1:])
		if perr != nil {
			return nil, true, perr
		}
		if len(rest) < 1 {
			return nil, true, errors.New(usage)
		}
		switch cmd {
		case "start":
			payload, err = cmdStart(rest[0], path)
		case "stop":
			payload, err = cmdStop(rest[0], path)
		case "build":
			payload, err = cmdBuild(rest[0], path)
		case "install":
			payload, err = cmdInstall(rest[0], path)
		case "logs":
			payload, err = cmdLogs(rest[0], rest[1:], path)
		}
		return payload, true, err

	case "list", "status":
		payload, err = cmdList()
		return payload, true, err
	case "launch":
		payload, err = cmdLaunch(args[1:])
		return payload, true, err
	case "help", "--help", "-h":
		payload, err = cmdHelp()
		return payload, true, err
	default:
		return nil, false, nil // comando desconocido → TUI
	}
}

// cliSession es el contexto que TODOS los comandos de lectura necesitan: la
// config, el store y el manager.
//
// Vive aquí porque el preámbulo estaba copiado en cinco comandos y porque es
// la razón por la que los comandos no se podían probar: cada uno resolvía su
// propia configuración desde el entorno y escribía su propia salida, así que
// probar uno exigía montar el entorno entero de producción.
//
// sessionErr es un error YA ENVUELTO con su prefijo, porque el prefijo es
// parte del contrato: un agente distingue "no encuentro el proyecto" de "el
// escaneo falló" sin leer el stack.
type cliSession struct {
	cfg     config.Config
	root    string
	store   *state.Store
	manager process.Manager
}

// newCliSession monta el contexto, o devuelve el error con el prefijo que el
// contrato publica para ese punto.
//
// El prefijo NO se aplica en los tres sitios internos: "scan error: " lo dice
// el comando que escanea, y store/config no son errores de escaneo. Por eso
// scanError lleva su propio envoltorio y los otros dos no.
func newCliSession() (cliSession, error) {
	cfg := loadConfig()
	store, err := state.NewStore()
	if err != nil {
		return cliSession{}, err
	}
	return cliSession{
		cfg:     cfg,
		root:    resolveRoot(),
		store:   store,
		manager: process.NewManager(),
	}, nil
}

// scan ejecuta el escaneo del root con el prefijo del contrato. Se llama aparte
// de newCliSession porque `launch --list` necesita el store pero NO escanea: el
// compose file es el contrato de ese comando, y un error de disco no debe
// impedir listar stacks.
func (s cliSession) scan() (scanner.ScanResult, error) {
	res, err := scanner.Scan(s.root, s.cfg.Scanner.Depth)
	if err != nil {
		return res, fmt.Errorf("scan error: %w", err)
	}
	return res, nil
}

// resolve busca el proyecto y devuelve el error tal cual, porque findProject ya
// redacta un mensaje accionable (nombra el nombre y los paths candidatos) y
// envolverlo lo enterraría.
func (s cliSession) resolve(query, path string) (scanner.Project, error) {
	res, err := s.scan()
	if err != nil {
		return scanner.Project{}, err
	}
	return findProject(res.Projects, query, path)
}

// cmdList lista todos los proyectos con estado completo.
func cmdList() (any, error) {
	s, err := newCliSession()
	if err != nil {
		return nil, err
	}

	scanResult, err := s.scan()
	if err != nil {
		return nil, err
	}

	collapsed := s.store.LoadCollapsed()

	result := ListResult{
		Projects: make([]ProjectInfo, 0, len(scanResult.Projects)),
	}
	for _, p := range scanResult.Projects {
		result.Projects = append(result.Projects, buildProjectInfo(s.manager, s.store, collapsed, p))
	}

	return result, nil
}

// cmdStart arranca un servicio daemonizado.
func cmdStart(name, path string) (any, error) {
	s, err := newCliSession()
	if err != nil {
		return nil, err
	}

	p, err := s.resolve(name, path)
	if err != nil {
		return nil, err
	}

	if !p.Configured {
		return nil, fmt.Errorf("project %q is not configured (missing or invalid .vroom.toml)", name)
	}

	// Verificar si ya está corriendo
	status, _ := evaluateStatus(s.manager, s.store, p.Path)
	if status == state.StateRunning {
		return ActionResult{
			OK:      true,
			Project: name,
			Action:  "already_running",
		}, nil
	}

	// Arrancar
	if _, err := s.store.EnsureServiceDir(p.Path); err != nil {
		return nil, fmt.Errorf("could not create service dir: %w", err)
	}

	out, err := startsvc.Start(startsvc.Request{
		Manifest:   p.Manifest,
		Path:       p.Path,
		Store:      s.store,
		Manager:    s.manager,
		StdoutPath: s.store.StdoutLog(p.Path),
		StderrPath: s.store.StderrLog(p.Path),
		Routes:     startsvc.RegistrarFor(p.Manifest),
		Branch:     gitinfo.Branch(p.Path),
	})
	if err != nil {
		return nil, fmt.Errorf("start failed: %w", err)
	}
	for _, w := range out.Warnings {
		_ = appendLine(s.store.StderrLog(p.Path), "── vroom ▶ start: "+w)
	}

	return ActionResult{
		OK:      true,
		Project: name,
		Action:  "started",
		Pid:     out.Pid,
	}, nil
}

// cmdStop detiene un servicio.
func cmdStop(name, path string) (any, error) {
	s, err := newCliSession()
	if err != nil {
		return nil, err
	}

	p, err := s.resolve(name, path)
	if err != nil {
		return nil, err
	}

	// Parada graciosa si command_stop está definido
	if p.Configured && p.Manifest.Stop != "" {
		// El fallo de command_stop NO detiene el cleanup: el servicio puede
		// seguir vivo y quitar su PID sin matarlo lo dejaría huérfano y sin
		// dueño, que es peor que un comando de parada que falla. El error se
		// descarta a propósito, y por eso el `_` está commented y no es un
		// olvido.
		_, _, _ = runLogged("stop", p.Manifest.Stop, p.Path, s.store.StdoutLog(p.Path), s.store.StderrLog(p.Path))
	}

	if err := stopCleanup(s.store, s.manager, p.Path); err != nil {
		return nil, err
	}

	return ActionResult{
		OK:      true,
		Project: name,
		Action:  "stopped",
	}, nil
}

// stopCleanup es todo lo que hace cmdStop DESPUÉS de matar el proceso, con el
// manager inyectado.
//
// Está separado del comando a propósito: cmdStop escanea disco, resuelve config
// y escribe JSON, así que no se puede ejercer en un test sin convertir el test en
// una integración de todo el CLI. Y la alternativa —dejar la lógica escrita en
// el comando y "probarla" reimplementándola en el test— es peor: un test que
// replica la lógica pasa aunque la lógica se borre, que es exactamente el hueco
// que dejó la primera versión (borrar los tres call sites dejaba la suite verde).
//
// Y devuelve error en vez de emitirlo porque ClearPid es el ÚNICO punto de esta
// función cuyo fallo es irrecuperable: sin él el Meta sigue afirmando un PID
// vivo para un servicio ya parado, y el siguiente `vroom list` afirmaría un
// servicio corriendo que no existe. Los demás fallos —parada, retirada de
// ruta— se registran y el cleanup sigue, como antes.
func stopCleanup(store *state.Store, manager process.Manager, path string) error {
	meta, err := store.LoadMeta(path)
	if err == nil && (meta.Pid > 0 || meta.Pgid > 0 || meta.Port > 0) {
		var warns []string
		_ = manager.Stop(process.StopSpec{
			Pid: meta.Pid, Pgid: meta.Pgid, Port: meta.Port,
			Timeout: process.DefaultStopTimeout,
			Warn:    func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) },
		})
		for _, w := range warns {
			_ = appendLine(store.StderrLog(path), "── vroom ▶ stop: "+w)
		}
		// El servicio ya está parado: su reserva vuelve al pool.
		process.ReleasePort(meta.ReservedPort)
	}
	if err == nil {
		// Y su ruta deja de existir: una dirección que apunta a un puerto
		// muerto es peor que ninguna. Va FUERA del guard de proceso a
		// propósito: un servicio que ya estaba muerto cuando se paró (Pid 0)
		// también deja una ruta detrás.
		releaseRouteOnStop(&meta)
	}

	if err := store.ClearPid(path); err != nil {
		return fmt.Errorf("could not clear pid: %w", err)
	}
	if err == nil {
		meta.State = state.StateStopped
		meta.Pid = 0
		meta.Pgid = 0
		meta.ReservedPort = 0
		_ = store.SaveMeta(path, meta)
	}
	_ = appendLine(store.StderrLog(path), "── vroom ▶ stop: service stopped ──")
	return nil
}

// cmdBuild ejecuta command_build de forma síncrona.
func cmdBuild(name, path string) (any, error) {
	return cmdOneShot(name, path, "build")
}

// cmdInstall ejecuta command_install de forma síncrona.
func cmdInstall(name, path string) (any, error) {
	return cmdOneShot(name, path, "install")
}

// cmdOneShot ejecuta un comando one-shot (build/install).
//
// El switch de kind es exhaustivo A PROPÓSITO y sin default: si mañana aparece
// una tercera clase de one-shot, este comando tiene que negarse a ejecutarla
// en vez de publicar un ActionResult sin comando ni código, que un agente leería
// como "se ejecutó y salió bien". kind es una constante de este paquete, así que
// el default es código muerto y el compilador es quien lo avisa al añadir el
// caso.
func cmdOneShot(name, path, kind string) (any, error) {
	s, err := newCliSession()
	if err != nil {
		return nil, err
	}

	p, err := s.resolve(name, path)
	if err != nil {
		return nil, err
	}

	if !p.Configured {
		return nil, fmt.Errorf("project %q is not configured", name)
	}

	var command string
	switch kind {
	case "build":
		command = p.Manifest.Build
	case "install":
		command = p.Manifest.Install
	default:
		return nil, fmt.Errorf("unknown one-shot command %q", kind)
	}

	if command == "" {
		return nil, fmt.Errorf("project %q has no %s command defined", name, kind)
	}

	elapsed, exitCode, err := runLogged(kind, command, p.Path, s.store.StdoutLog(p.Path), s.store.StderrLog(p.Path))
	result := ActionResult{
		OK:       err == nil,
		Project:  name,
		Action:   kind,
		ExitCode: exitCode,
		Elapsed:  elapsed.String(),
	}
	if err != nil {
		result.Error = err.Error()
	}
	return result, nil
}

// logFilter son los dos flags de `vroom logs`. Es un tipo y no dos enteros
// sueltos porque los dos viajan juntos por todo el comando y juntos deciden qué
// se lee: separarlosZGinvitaba a leer stderr cuando se pidió stdout.
type logFilter struct {
	tail   int    // 0 = todo el log
	stream string // merged | stdout | stderr
}

// parseLogFlags lee los flags simples de logs: --tail N, --stream valor.
//
// Un flag sin valor se ignora en vez de fallar, y un valor no numérico se
// trata como 0 (todo el log). Es deliberado: el contrato es "lo que pediste o
// el log entero", y un error por un `--tail` mal escrito dejaría al usuario sin
// logs, que es peor que darle logs de más. El valor se reporta en el
// resultado implícito, no se oculta.
func parseLogFlags(flags []string) logFilter {
	f := logFilter{stream: "merged"}
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--tail":
			if i+1 < len(flags) {
				_, _ = fmt.Sscanf(flags[i+1], "%d", &f.tail)
				i++
			}
		case "--stream":
			if i+1 < len(flags) {
				f.stream = flags[i+1]
				i++
			}
		}
	}
	return f
}

// readLog devuelve el log completo sin códigos ANSI.
//
// Un log ilegible devuelve "" y no un error: los logs son un extra informativo
// del estado del servicio, y un comando de consulta no puede dejar de responder
// porque el fichero de log no se puede leer. La fila del proyecto sigue siendo
// la misma.
func readLog(path string) string {
	data, _, err := tail.ReadNew(path, 0)
	if err != nil {
		return ""
	}
	return tail.StripANSI(data)
}

// lastNLines devuelve las últimas n líneas de s.
//
// n <= 0 y "más líneas de las que hay" devuelven s tal cual. Recortar por líneas
// y no por bytes es lo que evita partir una línea por la mitad, que es justo lo
// que un agente no puede usar.
//
// MEDIDO (bug): el salto de línea final cuenta como una línea MÁS. Un log de
// tres líneas escrito como "a\nb\nc\n" se parte en cuatro, así que `--tail 2`
// devolvía la tercera y una vacía —una línea de menos de las pedidas, y la que
// falta es justo la más antigua que el usuario quería—. Por eso el elemento
// final vacío se descarta ANTES de contar, y se devuelve con salto para que el
// recuento del consumidor siga siendo el que pidió.
func lastNLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	// Un log que acaba en "\n" no tiene una última línea: el salto es el
	// FINALIZADOR de la anterior. Sin esto, `tail N` devuelve N-1.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n") + "\n"
}

// cmdLogs muestra los logs de un servicio.
func cmdLogs(name string, flags []string, path string) (any, error) {
	s, err := newCliSession()
	if err != nil {
		return nil, err
	}

	p, err := s.resolve(name, path)
	if err != nil {
		return nil, err
	}

	f := parseLogFlags(flags)

	result := LogsResult{Project: name}
	stdoutPath := s.store.StdoutLog(p.Path)
	stderrPath := s.store.StderrLog(p.Path)

	switch f.stream {
	case "stdout":
		result.Stdout = lastNLines(readLog(stdoutPath), f.tail)
	case "stderr":
		result.Stderr = lastNLines(readLog(stderrPath), f.tail)
	default: // merged
		result.Stdout = lastNLines(readLog(stdoutPath), f.tail)
		result.Stderr = lastNLines(readLog(stderrPath), f.tail)
	}

	return result, nil
}

// cmdHelp muestra la ayuda. Devuelve la misma forma de mapa que antes: la ayuda
// también es JSON porque este paquete tiene UN contrato de salida, y un comando
// que imprimiera texto plano rompería la regla que un agente puede asumir.
func cmdHelp() (any, error) {
	return map[string]any{
		"commands": map[string]string{
			"vroom":        "launch the TUI (default when no arguments)",
			"vroom list":   "list all projects with full state (JSON)",
			"vroom status": "alias for list",
			"vroom start <name|path> [--path <path>]":   "start a service by project name or path",
			"vroom stop <name|path> [--path <path>]":    "stop a service by project name or path",
			"vroom build <name|path> [--path <path>]":   "run command_build (synchronous)",
			"vroom install <name|path> [--path <path>]": "run command_install (synchronous)",
			"vroom logs <name|path> [--path <path>]":    "show service logs (--tail N --stream merged|stdout|stderr)",
			"vroom launch --list":                       "list all orchestration stacks",
			"vroom launch <name>":                       "launch an orchestration stack",
			"vroom launch <name> --dry":                 "dry run: show plan without executing",
		},
		"notes": map[string]string{
			"--path": "use an explicit project path when a manifest name is ambiguous across worktrees",
		},
	}, nil
}

// LaunchListResult es la respuesta de `vroom launch --list`.
type LaunchListResult struct {
	File   string              `json:"file"`
	Stacks []orchestrate.Stack `json:"stacks"`
}

// cmdLaunch maneja el subcomando launch: --list, <name>, <name> --dry.
//
// `--list` NO escanea proyectos y NO necesita store: su contrato es el compose
// file, así que construir el store y recorrer el disco para él sería trabajo que
// puede fallar sin cambiar la respuesta. Antes lo hacía, y un `vroom launch
// --list` en un directorio con el home ilegible fallaba por una razón que no
// tiene que ver con los stacks.
func cmdLaunch(args []string) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: vroom launch --list | vroom launch <name> [--dry]")
	}

	// Buscar compose file en CWD
	cf, err := orchestrate.ParseComposeFile(".")
	if err != nil {
		return nil, err
	}

	if args[0] == "--list" {
		cwd, _ := os.Getwd()
		return LaunchListResult{
			File:   filepath.Join(cwd, orchestrate.ComposeFileName),
			Stacks: cf.Stacks,
		}, nil
	}

	stackName := args[0]
	stack, err := cf.FindStack(stackName)
	if err != nil {
		return nil, err
	}

	// Scan projects
	s, err := newCliSession()
	if err != nil {
		return nil, err
	}
	scanResult, err := s.scan()
	if err != nil {
		return nil, err
	}

	engine := orchestrate.NewEngine(s.manager, s.store)

	// Check for --dry flag
	dryRun := false
	for _, a := range args[1:] {
		if a == "--dry" {
			dryRun = true
			break
		}
	}

	if dryRun {
		result, err := engine.DryRun(stack, scanResult.Projects)
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	result, err := engine.Launch(stack, scanResult.Projects)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ---- Internal helpers (ported from TUI for CLI use) ----

// runLogged ejecuta un comando one-shot con `sh -c` en workDir.
func runLogged(kind, command, workDir, stdoutPath, stderrPath string) (time.Duration, int, error) {
	if dir := filepath.Dir(stdoutPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, 0, err
		}
	}

	banner := fmt.Sprintf("── vroom ▶ %s: %s ──", kind, command)

	// El stdout se abre UNA vez y de ahí sale también el banner. Con dos
	// `OpenFile` sobre el mismo fichero —uno para el banner y otro para el comando—
	// el segundo no podía fallar nunca, porque el primero acababa de demostrar que
	// el directorio existe y el modo es de escritura: un `if err != nil` con forma
	// de comprobación y sin nada detrás.
	//
	// Con un descriptor, los dos fallos son reales y se distinguen: que no se pueda
	// abrir el log, y que se abra pero no acepte escrituras —disco lleno, un
	// `/dev/full`—. El segundo importa más de lo que parece: un banner que no cabe
	// significa que tampoco cabrá la salida del build.
	out, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = out.Close() }()
	if _, err := fmt.Fprintf(out, "%s\n", banner); err != nil {
		return 0, 0, err
	}

	start := time.Now()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workDir
	errF, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = errF.Close() }()
	cmd.Stdout = out
	cmd.Stderr = errF
	runErr := cmd.Run()
	elapsed := time.Since(start).Round(10 * time.Millisecond)
	if runErr != nil {
		exitCode := 0
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		_, _ = fmt.Fprintf(out, "── vroom ✗ %s failed (exit %d, %s) ──\n", kind, exitCode, elapsed)
		return elapsed, exitCode, runErr
	}
	_, _ = fmt.Fprintf(out, "── vroom ✓ %s ok (%s) ──\n", kind, elapsed)
	return elapsed, 0, nil
}
