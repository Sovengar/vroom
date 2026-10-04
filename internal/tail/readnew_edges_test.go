package tail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadNewDevuelveElErrorCuandoElLogEsIlegible: el `Open` que no es "no existe".
//
// `ReadNew` tiene dos salidas distintas para un log que no se puede leer, y
// confundirlas es un fallo de verdad:
//
//   - "todavía no está" → vacío sin error, porque un servicio que no ha arrancado
//     no tiene log todavía y eso es normal.
//   - "está y no se puede leer" → error, porque un directorio de logs con permisos
//     cambiados o un montaje caído es un problema que el usuario tiene que ver.
//
// La primera se prueba con un path inexistente, que es el caso normal. Esta es la
// segunda, y se provoca con una ruta debajo de un FICHERO: `open` devuelve ENOTDIR,
// que no es `ENOENT`.
//
// El contrato es que el offset se devuelve tal cual: quien llama decide si eso es un
// fallo terminal o sólo un tick perdido, y devolver 0 haría que el siguiente tick
// releyera el log entero desde el principio.
func TestReadNewDevuelveElErrorCuandoElLogEsIlegible(t *testing.T) {
	fichero := filepath.Join(t.TempDir(), "soy-un-fichero")
	if err := os.WriteFile(fichero, []byte("no soy un directorio"), 0o644); err != nil {
		t.Fatal(err)
	}

	const offset = 4096
	data, nuevo, err := ReadNew(filepath.Join(fichero, "stdout.log"), offset)
	if err == nil {
		t.Fatalf("ReadNew = %q sin error con una ruta debajo de un fichero: un log ilegible se "+
			"confunde con uno que no existe y la consola se queda vacía sin decir por qué", data)
	}
	if data != "" {
		t.Errorf("data = %q con un error de apertura, want vacío", data)
	}
	if nuevo != offset {
		t.Errorf("offset = %d, want %d (el que entró): devolver 0 haría que el siguiente tick "+
			"releyera el log entero desde el principio", nuevo, offset)
	}
}

// TestReadNewSobreUnLogQueCambiaDebajoDevuelveLoQuePudoLeer: el `ReadAt` a medias.
//
// El caso real es un reinicio de servicio: entre que el tail pregunta cuánto tiene
// el fichero y que lo lee, el log se trunca a cero. Lo que se lee entonces es menos
// de lo pedido, y lo que se devuelve es lo que hubo.
//
// Lo que NO debe pasar es un error: el log se movió, el servicio no está fallando, y
// un error aquí dejaría la consola en blanco un tick. Con el siguiente tick el
// buffer vuelve a estar bien, y eso es lo que se comprueba.
func TestReadNewSobreUnLogQueCambiaDebajoDevuelveLoQuePudoLeer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(path, []byte("primera linea\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	primero, nuevo, err := ReadNew(path, 0)
	if err != nil {
		t.Fatalf("ReadNew: %v", err)
	}
	if primero != "primera linea\n" {
		t.Fatalf("primera lectura = %q, want %q", primero, "primera linea\n")
	}

	// El log se trunca mientras el tail cree que tiene 14 bytes.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}

	// Pedir desde más allá del final es la rotación: `ReadNew` tiene que releer desde
	// cero, no devolver un error ni un buffer vacío.
	trasRotacion, nuevo2, err := ReadNew(path, nuevo)
	if err != nil {
		t.Errorf("ReadNew tras truncar el log = %v, want nil: un log que se movió no es un fallo "+
			"del servicio, y un error aquí vacía la consola un tick", err)
	}
	if trasRotacion != "" {
		t.Errorf("data = %q tras truncar a cero, want vacío: el fichero está vacío, y eso es lo "+
			"que hay", trasRotacion)
	}
	if nuevo2 != 0 {
		t.Errorf("offset = %d tras releer un log vacío, want 0", nuevo2)
	}
}

// TestReadNewNoDevuelveMasDeLoQueHay: la lectura posicional.
//
// El tail lee desde el offset con `ReadAt`, que no mueve el descriptor. Antes se
// leía con `Read` después de un `Seek`, y el `Read` exigía que el descriptor
// estuviera positioned: con el `Seek(0, io.SeekEnd)` de "cuánto tiene" el `Read`
// se habría quedado al final y no habría devuelto nada.
//
// Este test es el que distingue una cosa de la otra: lee un fichero con contenido
// desde el offset 0 y espera TODO el contenido, no cero bytes.
func TestReadNewNoDevuelveMasDeLoQueHay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	const contenido = "linea uno\nlinea dos\n"
	if err := os.WriteFile(path, []byte(contenido), 0o644); err != nil {
		t.Fatal(err)
	}

	data, nuevo, err := ReadNew(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if data != contenido {
		t.Errorf("data = %q, want %q: leer desde el principio tiene que devolver el fichero entero",
			data, contenido)
	}
	if nuevo != int64(len(contenido)) {
		t.Errorf("offset = %d, want %d", nuevo, len(contenido))
	}

	// Y desde un offset intermedio, sólo la cola.
	cola, nuevo2, err := ReadNew(path, 9)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(contenido, cola) {
		t.Errorf("cola = %q, want la cola del contenido a partir del offset 9", cola)
	}
	if nuevo2 != int64(len(contenido)) {
		t.Errorf("offset = %d, want %d", nuevo2, len(contenido))
	}
}
