package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// Exists is the filter of the whole discovery, and its bool return is deliberate: "no manifest" is not a failure, it is the answer the scan already knows how to handle.
func TestExistsDistinguePresenteAusenteYDirectorio(t *testing.T) {
	conManifiesto := t.TempDir()
	if err := os.WriteFile(filepath.Join(conManifiesto, FileName), []byte("name = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sinManifiesto := t.TempDir()

	// A directory NAMED .vroom.toml exists as a path but is not a manifest, and Stat finds it, so Exists says true.
	conDirectorio := t.TempDir()
	if err := os.MkdirAll(filepath.Join(conDirectorio, FileName), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		dir  string
		want bool
	}{
		{"con manifiesto", conManifiesto, true},
		{"sin manifiesto", sinManifiesto, false},
		{".vroom.toml como directorio", conDirectorio, true},
		{"directorio que no existe", filepath.Join(sinManifiesto, "nada"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Exists(tt.dir); got != tt.want {
				t.Errorf("Exists(%s) = %v, want %v", filepath.Base(tt.dir), got, tt.want)
			}
		})
	}
}

func TestExistsConManifiestoIlegibleSigueSiendoExiste(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root puede leer un fichero sin permiso: el caso de ILEGIBLE no se puede provocar")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("name = \"x\"\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	// An unreadable manifest still exists, so the scan shows the row and publishes the parse error instead of making the project look nonexistent.
	if !Exists(dir) {
		t.Error("Exists = false con un manifiesto ilegible: el directorio desaparecería del escaneo en vez de aparecer roto")
	}
}
