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
commands.start.run = "go run main.go"
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
	if m.Commands.Start.Run != "go run main.go" {
		t.Errorf("incorrect fields: %+v", m)
	}
	if m.Port != 8080 || m.ProcessPattern != "vsocial-api" {
		t.Errorf("incorrect fields: %+v", m)
	}
}

func TestParseMinimalValid(t *testing.T) {
	path := writeManifest(t, `
name = "mi-servicio"
commands.start.run = "./start.sh"
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
		{"without name", `commands.start.run = "go run main.go"`, "name"},
		{"without commands.start.run", `name = "x"`, "commands.start.run"},
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
			content := fmt.Sprintf("name = \"x\"\ncommands.start.run = \"y\"\nport = %d\n", tt.port)
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
commands.start.run = "y"
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
commands.start.run = "y"
`)
	m, err := Parse(path)
	if err != nil {
		t.Fatalf("the old group key must not break parsing: %v", err)
	}
	if m.PrimaryGroup != "" || m.SecondaryGroup != "" {
		t.Errorf("group must no longer group: %+v", m)
	}
}

// commands.install/commands.build are optional one-shot commands (mise run ... or anything), so their absence cannot invalidate the manifest; commands.stop is optional too.
func TestParseInstallBuildStop(t *testing.T) {
	path := writeManifest(t, `
name = "web-frontend"
commands.start.run = "node server.js"
commands.install.run = "pnpm install"
commands.build.run = "mise run build"
commands.stop.run = "docker stop web-frontend"
`)
	m, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected parse: %v", err)
	}
	if m.Commands.Install.Run != "pnpm install" || m.Commands.Build.Run != "mise run build" {
		t.Errorf("install/build misparsed: %+v", m)
	}
	if m.Commands.Stop.Run != "docker stop web-frontend" {
		t.Errorf("stop misparsed: %+v", m)
	}

	minimal, err := Parse(writeManifest(t, "name = \"x\"\ncommands.start.run = \"y\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if minimal.Commands.Install.Run != "" || minimal.Commands.Build.Run != "" || minimal.Commands.Stop.Run != "" {
		t.Errorf("install/build/stop must default to empty: %+v", minimal)
	}
}

// commands.start.hooks are optional: their absence must keep behaving exactly as before the hooks existed.
func TestParseStartHooks(t *testing.T) {
	path := writeManifest(t, `
name = "actuacions-api"
commands.start.run = "mise run start"
commands.start.hooks.pre_run = "fuser -k 5005/tcp || true"
commands.start.hooks.post_run = "curl -fsS localhost:8090/actuacions/health"
`)
	m, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected parse: %v", err)
	}
	if m.Commands.Start.Hooks.PreRun != "fuser -k 5005/tcp || true" {
		t.Errorf("pre_run misparsed: %+v", m)
	}
	if m.Commands.Start.Hooks.PostRun != "curl -fsS localhost:8090/actuacions/health" {
		t.Errorf("post_run misparsed: %+v", m)
	}

	minimal, err := Parse(writeManifest(t, "name = \"x\"\ncommands.start.run = \"y\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if minimal.Commands.Start.Hooks.PreRun != "" || minimal.Commands.Start.Hooks.PostRun != "" {
		t.Errorf("both hooks must default to empty: %+v", minimal)
	}
}

// The table form the docs show must decode exactly like the dotted form, including the nested hooks table.
func TestParseTableFormOfCommands(t *testing.T) {
	path := writeManifest(t, `
name = "actuacions-api"
port = 8090

[commands.start]
run = "mise run start"

  [commands.start.hooks]
  pre_run = "fuser -k 5005/tcp || true"

[commands.stop]
run = "docker stop actuacions-api"
`)
	m, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected parse: %v", err)
	}
	if m.Commands.Start.Run != "mise run start" {
		t.Errorf("start.run misparsed: %+v", m)
	}
	if m.Commands.Start.Hooks.PreRun != "fuser -k 5005/tcp || true" {
		t.Errorf("hooks.pre_run misparsed: %+v", m)
	}
	if m.Commands.Stop.Run != "docker stop actuacions-api" {
		t.Errorf("stop.run misparsed: %+v", m)
	}
	if m.Port != 8090 {
		t.Errorf("port = %d, want 8090: a [commands.*] header must not swallow the keys before it", m.Port)
	}
}

// A pre-[commands] key is a hard rename with no alias, so it must fail NAMING the new key: otherwise the fleet migrates by discovering which commands silently vanished.
func TestParseLegacyCommandKeysNameTheNewKey(t *testing.T) {
	for legacy, target := range movedKeys {
		t.Run(legacy, func(t *testing.T) {
			path := writeManifest(t, fmt.Sprintf("name = \"x\"\ncommands.start.run = \"y\"\n%s = \"whatever\"\n", legacy))
			_, err := Parse(path)
			if err == nil {
				t.Fatal("a manifest written with the old key must not parse")
			}
			if !strings.Contains(err.Error(), legacy) || !strings.Contains(err.Error(), target) {
				t.Errorf("error = %q, want it to name both %q and %q", err.Error(), legacy, target)
			}
		})
	}
}

// A top-level key written after a [commands.*] header decodes INSIDE that table: port would become commands.start.port and the port contract would vanish while the manifest kept parsing. The parse has to say so instead.
func TestParseRejectsTopLevelKeysSwallowedByACommandsHeader(t *testing.T) {
	path := writeManifest(t, `
name = "x"

[commands.start]
run = "y"
port = 8090
`)
	_, err := Parse(path)
	if err == nil {
		t.Fatal("a top-level key swallowed by a [commands.*] header must not parse")
	}
	for _, want := range []string{"commands.start.port", "[commands]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}
}

func TestParseUnknownFieldsIgnored(t *testing.T) {
	path := writeManifest(t, `
name = "x"
commands.start.run = "y"
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
commands.start.run = "echo hi"
secondary_group = "Composers"
`)
	_, err := Parse(path)
	if err == nil {
		t.Fatal("expected error for reserved secondary_group 'Composers'")
	}
}
