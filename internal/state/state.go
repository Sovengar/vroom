// Package state persiste el estado de servicios en disco.
//
// Layout:
//
//	{base}/services/{hash}/meta.json    metadatos del servicio
//	{base}/services/{hash}/pid          PID del proceso principal
//	{base}/services/{hash}/pgid         PGID del process group
//	{base}/services/{hash}/stdout.log   stdout capturado
//	{base}/services/{hash}/stderr.log   stderr capturado
//
// base se resuelve en runtime: $XDG_STATE_HOME/vroom (default ~/.local/state/vroom)
// en Linux; portable a otras plataformas sin cambiar el código.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// StateRunning indica el servicio daemonizado y vivo.
	StateRunning = "running"
	// StateStopped indica el servicio detenido (o PID muerto/reciclado).
	StateStopped = "stopped"
	// StateUnknown indica PID vivo pero verificación de puerto/pattern fallida.
	StateUnknown = "unknown"
	// StatePortPending indica que el proceso está vivo y el puerto real aún
	// no se ha resuelto (reservado, discovery en vuelo). Se persiste para
	// que un reinicio de la TUI no lo lea como "sin puerto" y no lo confunda
	// con "vivo y sano".
	StatePortPending = "port_pending"
	// StateNoPort indica que el proceso está vivo y NO tiene puerto TCP, y
	// que eso es su estado, no una espera pendiente.
	StateNoPort = "no_port"
	// StatePortUnresolved indica que se agotó el plazo de discovery sin
	// poder decidir el puerto principal. El proceso vive y puede que aún
	// no haya hecho bind. NO es StateNoPort: ese dice "no tiene", este
	// dice "no lo sabemos todavía", y la UI los muestra distinto para no
	// presentar como puerto real uno que nunca se confirmó.
	StatePortUnresolved = "port_unresolved"
)

// Meta es el schema de meta.json.
type Meta struct {
	Name        string `json:"name"`
	ProjectPath string `json:"project_path"`
	Port        int    `json:"port"`
	// ReservedPort es el puerto que vroom reservó y ofreció al hijo en
	// dynamic, DISTINTO de Port (el puerto real que acabó escuchando). Se
	// persiste aparte porque el ciclo de vida de los dos es distinto:
	// ReservedPort se devuelve al pool en el stop, y sin él no hay forma de
	// saber qué reserva hay que liberar.
	ReservedPort   int    `json:"reserved_port,omitempty"`
	ProcessPattern string `json:"process_pattern"`
	Command        string `json:"command"`
	Pid            int    `json:"pid"`
	Pgid           int    `json:"pgid"`
	CreationTimeMs int64  `json:"creation_time_ms"`
	StartedAt      string `json:"started_at"` // RFC3339
	State          string `json:"state"`      // running|stopped|unknown|port_pending|no_port
	// PortVerified dice si el puerto persistido fue confirmado contra un
	// listener real del linaje. False = el puerto fue elegido sin prueba
	// (multi-puerto sin heurística posible). Ver docs/adr/adr-0012.
	PortVerified bool `json:"port_verified"`
	// RouteName es el nombre de ruta que vroom registró para este servicio, y
	// el puerto al que apuntaba (RoutePort). Se persisten porque su ciclo de
	// vida es DISTINTO del del servicio: la ruta sobrevive a un reinicio del
	// proxy (medido, M3) y no la limpia `portless prune` (M5), así que sin
	// esto ni el stop ni la reconciliación del arranque podrían encontrarla.
	// Vacío = este servicio no registró ninguna ruta.
	RouteName   string `json:"route_name,omitempty"`
	RoutePort   int    `json:"route_port,omitempty"`
	RouteStatus string `json:"route_status,omitempty"` // registered | degraded
	// RouteOwned dice que la ruta anterior sigue siendo NUESTRA: se pone al
	// registrar y se quita al retirarla.
	//
	// Existe porque RouteName y RoutePort solos NO autorizan a escribir sobre
	// el nombre: son un nombre y un número, y ninguno caduca. Con sólo ellos, un
	// actor que tomara el nombre y lo dejara en nuestro puerto anterior pasaba
	// a ser, de hecho, un dueño cuya ruta podíamos pisar. Es la diferencia entre
	// "sé qué puerto tenía" y "sé que sigue siendo mía".
	//
	// Se limpia DESPUÉS de que la retirada tenga efecto, no antes: si el
	// `Remove` falla, la ruta puede seguir ahí, y perder el handle convertiría
	// esa ruta en algo que nadie puede limpiar salvo la reconciliación.
	RouteOwned bool `json:"route_owned,omitempty"`
	// RouteReason explica una degradación. Vacío cuando la ruta funciona. El
	// estado de la RUTA nunca afecta al State del servicio: la salud no
	// depende de la dirección.
	RouteReason string `json:"route_reason,omitempty"`
	RouteURL    string `json:"route_url,omitempty"`
}

// Store accede al directorio de estado persistente.
type Store struct {
	base string
}

// DefaultBaseDir resuelve el directorio base de estado según plataforma:
// $XDG_STATE_HOME/vroom si está definida, si no ~/.local/state/vroom.
func DefaultBaseDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "vroom"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "vroom"), nil
}

// NewStore crea el store usando DefaultBaseDir y asegura services/.
func NewStore() (*Store, error) {
	base, err := DefaultBaseDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(base, "services"), 0o755); err != nil {
		return nil, fmt.Errorf("could not create state directory %s (check permissions): %w", base, err)
	}
	return &Store{base: base}, nil
}

// NewStoreAt crea un store sobre un directorio arbitrario (usado en tests).
func NewStoreAt(base string) *Store {
	return &Store{base: base}
}

// Base devuelve el directorio raíz del store.
func (s *Store) Base() string { return s.base }

// ServiceDir devuelve services/{hash} para el proyecto en projectPath.
func (s *Store) ServiceDir(projectPath string) string {
	return filepath.Join(s.base, "services", PathKey(projectPath))
}

// EnsureServiceDir crea services/{hash} con permisos 0755 si no existe.
func (s *Store) EnsureServiceDir(projectPath string) (string, error) {
	dir := s.ServiceDir(projectPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("could not create service directory %s (check permissions): %w", dir, err)
	}
	return dir, nil
}

// StdoutLog devuelve la ruta del log de stdout.
func (s *Store) StdoutLog(projectPath string) string {
	return filepath.Join(s.ServiceDir(projectPath), "stdout.log")
}

// StderrLog devuelve la ruta del log de stderr.
func (s *Store) StderrLog(projectPath string) string {
	return filepath.Join(s.ServiceDir(projectPath), "stderr.log")
}

// PidFile devuelve la ruta del fichero pid.
func (s *Store) PidFile(projectPath string) string {
	return filepath.Join(s.ServiceDir(projectPath), "pid")
}

// PgidFile devuelve la ruta del fichero pgid.
func (s *Store) PgidFile(projectPath string) string {
	return filepath.Join(s.ServiceDir(projectPath), "pgid")
}

// SaveMeta escribe meta.json de forma atómica (tmp + rename).
func (s *Store) SaveMeta(projectPath string, m Meta) error {
	if _, err := s.EnsureServiceDir(projectPath); err != nil {
		return err
	}
	target := filepath.Join(s.ServiceDir(projectPath), "meta.json")
	if err := writeJSONAtomic(target, m); err != nil {
		return fmt.Errorf("could not save %s: %w", filepath.Base(target), err)
	}
	return nil
}

// writeJSONAtomic serializa v y lo deja en target con el patrón tmp + rename, que
// es lo que hace que un corte de luz a mitad de escritura no deje un meta a medias.
//
// El error de `json.MarshalIndent` se convierte en panic en vez de en un `return`
// error, y el motivo es concreto: con los tipos que se guardan aquí —`Meta` y
// `map[string]bool`, todo string, int, int64 y bool— `encoding/json` nunca falla.
// La rama que lo comprobaba era, literalmente, código muerto con forma de
// comprobación, y una comprobación que no se puede ejecutar es una que nadie lee.
//
// Un panic tampoco es bonito, pero es HONESTO: si mañana `Meta` gana un campo que
// `json` no sabe serializar —un `float64` con NaN, un canal, un ciclo— eso es un
// error de programación en el sitio que añadió el campo, no una condición de
// ejecución. Y aquí se ve, porque el panic nombra el tipo que no se pudo serializar
// en vez de devolver un error a la octava capa de la TUI. El test
// `TestWriteJSONAtomicRevientaConUnTipoNoSerializable` lo comprueba.
func writeJSONAtomic(target string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("json.MarshalIndent de %T falló y eso no puede pasar con los tipos que "+
			"vroom guarda aquí: %v", v, err))
	}
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// LoadMeta lee meta.json. Si el JSON está corrupto devuelve error
// (el llamador marca stopped y loguea warning); si no existe
// devuelve os.ErrNotExist envuelto.
func (s *Store) LoadMeta(projectPath string) (Meta, error) {
	var m Meta
	data, err := os.ReadFile(filepath.Join(s.ServiceDir(projectPath), "meta.json"))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("corrupt meta.json: %w", err)
	}
	return m, nil
}

// RegisterPid persiste pid y pgid como ficheros de texto.
func (s *Store) RegisterPid(projectPath string, pid, pgid int) error {
	dir, err := s.EnsureServiceDir(projectPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(fmt.Sprintf("%d\n", pid)), 0o644); err != nil {
		return fmt.Errorf("could not write pid file: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pgid"), []byte(fmt.Sprintf("%d\n", pgid)), 0o644); err != nil {
		return fmt.Errorf("could not write pgid file: %w", err)
	}
	return nil
}

// ClearPid elimina los ficheros pid y pgid (al detener el servicio).
// Los logs y meta.json se conservan para histórico.
func (s *Store) ClearPid(projectPath string) error {
	for _, f := range []string{s.PidFile(projectPath), s.PgidFile(projectPath)} {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("could not clean up %s: %w", f, err)
		}
	}
	return nil
}

// ---- UI persisted state (collapsed groups) ----

// CollapsedFile devuelve la ruta del fichero collapsed.json.
func (s *Store) CollapsedFile() string {
	return filepath.Join(s.base, "collapsed.json")
}

// SaveCollapsed persiste el mapa de grupos colapsados a disco (átomico).
// Las claves son el nombre del primario o "primario/secundario".
func (s *Store) SaveCollapsed(groups map[string]bool) error {
	// El mismo criterio que en `SaveMeta`, y por el mismo motivo: un
	// `map[string]bool` siempre se serializa. El panic y su motivo están en
	// `writeJSONAtomic`.
	target := s.CollapsedFile()
	if err := writeJSONAtomic(target, groups); err != nil {
		return fmt.Errorf("could not save %s: %w", filepath.Base(target), err)
	}
	return nil
}

// LoadCollapsed lee el mapa de grupos colapsados desde disco.
// Si el fichero no existe devuelve nil sin error; si está corrupto
// devuelve un mapa vacío (el usuario pierde el estado pero no la sesión).
func (s *Store) LoadCollapsed() map[string]bool {
	data, err := os.ReadFile(s.CollapsedFile())
	if err != nil {
		return nil // primer arranque o limpieza: todos expandidos
	}
	groups := make(map[string]bool)
	if err := json.Unmarshal(data, &groups); err != nil {
		return nil // corrupto: empezar limpio
	}
	return groups
}
