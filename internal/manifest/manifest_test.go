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
primary_group = "vsocial"
secondary_group = "backend"
command_start = "go run main.go"
port = 8080
process_pattern = "vsocial-api"
`)
	m, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected parse: %v", err)
	}
	if m.Name != "vsocial-api" || m.PrimaryGroup != "vsocial" || m.SecondaryGroup != "backend" {
		t.Errorf("incorrect fields: %+v", m)
	}
	if m.Command != "go run main.go" {
		t.Errorf("incorrect fields: %+v", m)
	}
	if m.Port != 8080 || m.ProcessPattern != "vsocial-api" {
		t.Errorf("incorrect fields: %+v", m)
	}
}

func TestParseMinimalValid(t *testing.T) {
	path := writeManifest(t, `
name = "mi-servicio"
command_start = "./start.sh"
`)
	m, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected parse: %v", err)
	}
	if m.PrimaryGroup != "" || m.SecondaryGroup != "" || m.Port != 0 || m.ProcessPattern != "" {
		t.Errorf("defaults not applied: %+v", m)
	}
}

func TestParseMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"without name", `command_start = "go run main.go"`, "name"},
		{"without command_start", `name = "x"`, "command_start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeManifest(t, tt.content)
			_, err := Parse(path)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not mention field %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParsePortOutOfRange(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{"greater than 65535", 99999},
		{"negative", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := fmt.Sprintf("name = \"x\"\ncommand_start = \"y\"\nport = %d\n", tt.port)
			path := writeManifest(t, content)
			_, err := Parse(path)
			if err == nil {
				t.Fatal("expected port range error")
			}
			if !strings.Contains(err.Error(), "port") {
				t.Errorf("error %q does not mention port", err.Error())
			}
		})
	}
}

func TestParseEmptyOptionals(t *testing.T) {
	path := writeManifest(t, `
name = "x"
primary_group = ""
secondary_group = ""
command_start = "y"
port = 0
process_pattern = ""
`)
	m, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected parse: %v", err)
	}
	if m.PrimaryGroup != "" || m.SecondaryGroup != "" || m.Port != 0 || m.ProcessPattern != "" {
		t.Errorf("empty optionals mishandled: %+v", m)
	}
}

// The old `group` key no longer groups (hard replacement, no alias); unknown fields are ignored.
func TestParseGroupKeyIgnored(t *testing.T) {
	path := writeManifest(t, `
name = "x"
group = "backend"
command_start = "y"
`)
	m, err := Parse(path)
	if err != nil {
		t.Fatalf("the old group key must not break parsing: %v", err)
	}
	if m.PrimaryGroup != "" || m.SecondaryGroup != "" {
		t.Errorf("group must no longer group: %+v", m)
	}
}

// command_install/command_build are optional one-shot commands (mise run ... or anything), so their absence cannot invalidate the manifest; command_stop is optional too.
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
		t.Fatalf("unexpected parse: %v", err)
	}
	if m.Install != "pnpm install" || m.Build != "mise run build" {
		t.Errorf("install/build misparsed: %+v", m)
	}
	if m.Stop != "docker stop web-frontend" {
		t.Errorf("stop misparsed: %+v", m)
	}

	minimal, err := Parse(writeManifest(t, "name = \"x\"\ncommand_start = \"y\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if minimal.Install != "" || minimal.Build != "" || minimal.Stop != "" {
		t.Errorf("install/build/stop must default to empty: %+v", minimal)
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
		t.Fatalf("unknown fields must not break parsing: %v", err)
	}
}

// S-T1
func TestParseInvalidTOML(t *testing.T) {
	path := writeManifest(t, `name = [sin cerrar`)
	if _, err := Parse(path); err == nil {
		t.Fatal("expected TOML syntax error")
	}
}

func TestParseMissingFile(t *testing.T) {
	if _, err := Parse(filepath.Join(t.TempDir(), FileName)); err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestParseReservedSecondaryGroup(t *testing.T) {
	path := writeManifest(t, `
name = "test"
command_start = "echo hi"
secondary_group = "Composers"
`)
	_, err := Parse(path)
	if err == nil {
		t.Fatal("expected error for reserved secondary_group 'Composers'")
	}
}
