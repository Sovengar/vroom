package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseValidAppliesDefaults(t *testing.T) {
	path := writeManifest(t, `
name = "vsocial-api"
group = "vsocial"
command_start = "go run main.go"
port = 8080
process_pattern = "vsocial-api"
`)
	m, err := Parse(path)
	if err != nil {
		t.Fatalf("parse inesperado: %v", err)
	}
	if m.Name != "vsocial-api" || m.Group != "vsocial" || m.Command != "go run main.go" {
		t.Errorf("campos incorrectos: %+v", m)
	}
	if m.Port != 8080 || m.ProcessPattern != "vsocial-api" {
		t.Errorf("campos incorrectos: %+v", m)
	}
}

// S1.1
func TestParseMinimalValid(t *testing.T) {
	path := writeManifest(t, `
name = "mi-servicio"
command_start = "./start.sh"
`)
	m, err := Parse(path)
	if err != nil {
		t.Fatalf("parse inesperado: %v", err)
	}
	if m.Group != "" || m.Port != 0 || m.ProcessPattern != "" {
		t.Errorf("defaults no aplicados: %+v", m)
	}
}

// S1.2
func TestParseMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"sin name", `command_start = "go run main.go"`, "name"},
		{"sin command_start", `name = "x"`, "command_start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeManifest(t, tt.content)
			_, err := Parse(path)
			if err == nil {
				t.Fatal("esperaba error de validación")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q no menciona campo %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// S1.3
func TestParsePortOutOfRange(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{"mayor a 65535", 99999},
		{"negativo", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := fmt.Sprintf("name = \"x\"\ncommand_start = \"y\"\nport = %d\n", tt.port)
			path := writeManifest(t, content)
			_, err := Parse(path)
			if err == nil {
				t.Fatal("esperaba error de rango de puerto")
			}
			if !strings.Contains(err.Error(), "port") {
				t.Errorf("error %q no menciona port", err.Error())
			}
		})
	}
}

// S1.4
func TestParseEmptyOptionals(t *testing.T) {
	path := writeManifest(t, `
name = "x"
group = ""
command_start = "y"
port = 0
process_pattern = ""
`)
	m, err := Parse(path)
	if err != nil {
		t.Fatalf("parse inesperado: %v", err)
	}
	if m.Group != "" || m.Port != 0 || m.ProcessPattern != "" {
		t.Errorf("opcionales vacíos mal manejados: %+v", m)
	}
}

// 0003 R26: command_install/command_build son comandos one-shot
// opcionales (pueden ser `mise run ...` o cualquier comando); su
// ausencia no invalida el manifiesto. command_stop igualmente opcional.
func TestParseInstallBuildStop(t *testing.T) {
	path := writeManifest(t, `
name = "web-frontend"
command_start = "node server.js"
command_install = "pnpm install"
command_build = "mise run build"
command_stop = "docker stop web-frontend"
`)
	m, err := Parse(path)
	if err != nil {
		t.Fatalf("parse inesperado: %v", err)
	}
	if m.Install != "pnpm install" || m.Build != "mise run build" {
		t.Errorf("install/build mal parseados: %+v", m)
	}
	if m.Stop != "docker stop web-frontend" {
		t.Errorf("stop mal parseado: %+v", m)
	}

	minimal, err := Parse(writeManifest(t, "name = \"x\"\ncommand_start = \"y\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if minimal.Install != "" || minimal.Build != "" || minimal.Stop != "" {
		t.Errorf("install/build/stop deben default a vacío: %+v", minimal)
	}
}

func TestParseUnknownFieldsIgnored(t *testing.T) {
	path := writeManifest(t, `
name = "x"
command_start = "y"
future_field = "algo"
another = 42
`)
	if _, err := Parse(path); err != nil {
		t.Fatalf("campos desconocidos no deben romper el parse: %v", err)
	}
}

// S-T1
func TestParseInvalidTOML(t *testing.T) {
	path := writeManifest(t, `name = [sin cerrar`)
	if _, err := Parse(path); err == nil {
		t.Fatal("esperaba error de sintaxis TOML")
	}
}

func TestParseMissingFile(t *testing.T) {
	if _, err := Parse(filepath.Join(t.TempDir(), FileName)); err == nil {
		t.Fatal("esperaba error por fichero inexistente")
	}
}
