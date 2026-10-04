package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Exists es el filtro de TODA ladiscovery: el escaneo llama a inspectDir sobre
// cada directorio y sólo entra en los que devuelven true.
//
// Está al 0% y es la función de la que depende que el escaneo no se detenga en
// cualquier carpeta con un fichero llamado a mano. Su valor de retorno es un bool
// y no un error a propósito: "no hay manifiesto" no es un fallo de nada, es la
// respuesta que el escaneo ya sabe manejar.
//
// Y hay un borde que importa: un `.vroom.toml` ILEGIBLE cuenta como existente.
// Si devolviera false con un error de permisos, el directorio desaparecería del
// escaneo y el usuario vería un proyecto como si no existiera, en vez de como
// roto — que es lo que puede arreglar.
// ---------------------------------------------------------------------------

func TestExistsDistinguePresenteAusenteYDirectorio(t *testing.T) {
	conManifiesto := t.TempDir()
	if err := os.WriteFile(filepath.Join(conManifiesto, FileName), []byte("name = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sinManifiesto := t.TempDir()

	// Un directorio LLAMADO .vroom.toml en vez de fichero: existe como ruta pero
	// no es un manifiesto. Stat lo encuentra, así que Exists dice true. Es lo
	// correcto para el escaneo (que luego fallará al parsearlo y marcará la fila
	// como no configurada con su error) y lo que evita que el escaneo se pare en
	// este caso sin decir nada.
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

	// Existe, aunque no se pueda leer. El escaneo lo mete como fila y el error de
	// parseo es lo que se publica; desaparecer del escaneo sería peor, porque el
	// usuario no vería nada y concluiría que el proyecto no existe.
	if !Exists(dir) {
		t.Error("Exists = false con un manifiesto ilegible: el directorio desaparecería del escaneo en vez de aparecer roto")
	}
}
