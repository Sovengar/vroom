package cli

import (
	"encoding/json"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// El JSON emite el puerto REAL del servicio, no el declarado. El bug que
// este feature elimina: ProjectInfo.Port se asignaba antes de
// evaluateStatus, así que siempre ganaba el valor del manifiesto.
func TestBuildProjectInfoEmitsResolvedPort(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	p := scanner.Project{
		Path: dir, Name: "a", Configured: true,
		Manifest: &manifest.Manifest{
			Name: "api", Command: "run", Port: 8080, PortMode: manifest.PortModeDynamic,
		},
	}
	if err := store.SaveMeta(dir, state.Meta{
		Name: "api", ProjectPath: dir,
		Port: 41501, Pid: 424242, Pgid: 424242,
		State: state.StateRunning, PortVerified: true,
	}); err != nil {
		t.Fatal(err)
	}

	info := buildProjectInfo(process.NewManager(), store, map[string]bool{}, p)

	if info.Port != 41501 {
		t.Errorf("Port = %d, want el puerto real 41501 (no el declarado 8080)", info.Port)
	}
	if !info.PortVerified {
		t.Error("PortVerified debe propagarse")
	}
	if info.PortMode != manifest.PortModeDynamic {
		t.Errorf("PortMode = %q, want dynamic", info.PortMode)
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["port"] != float64(41501) {
		t.Errorf("el JSON debe emitir el puerto real, got %v", decoded["port"])
	}
}

// Sin meta (servicio parado) el JSON vuelve al puerto declarado: es lo que
// el usuario espera ver en un servicio que no está corriendo.
func TestBuildProjectInfoFallsBackToDeclaredPort(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	p := scanner.Project{
		Path: "/nope", Name: "a", Configured: true,
		Manifest: &manifest.Manifest{Name: "api", Command: "run", Port: 8080},
	}
	info := buildProjectInfo(process.NewManager(), store, map[string]bool{}, p)
	if info.Port != 8080 {
		t.Errorf("Port = %d, want el declarado 8080", info.Port)
	}
	if info.PortMode != manifest.PortModeFixed {
		t.Errorf("PortMode = %q, want fixed (port_mode ausente)", info.PortMode)
	}
}
