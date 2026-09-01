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
)

// Meta es el schema de meta.json (spec R5).
type Meta struct {
	Name           string `json:"name"`
	ProjectPath    string `json:"project_path"`
	Group          string `json:"group"`
	Port           int    `json:"port"`
	ProcessPattern string `json:"process_pattern"`
	Command        string `json:"command"`
	Pid            int    `json:"pid"`
	Pgid           int    `json:"pgid"`
	CreationTimeMs int64  `json:"creation_time_ms"`
	StartedAt      string `json:"started_at"` // RFC3339
	State          string `json:"state"`      // running|stopped|unknown
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
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal meta.json: %w", err)
	}
	target := filepath.Join(s.ServiceDir(projectPath), "meta.json")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("could not write meta.json: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		return fmt.Errorf("could not replace meta.json: %w", err)
	}
	return nil
}

// LoadMeta lee meta.json. Si el JSON está corrupto devuelve error
// (el llamador marca stopped y loguea warning, S5.1); si no existe
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
