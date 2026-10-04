package tail

import (
	"errors"
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

// TestReadNewNoRevientaConUnLogQueEsUnDirectorio: la regresión de CI.
//
// Este test existe porque el código la introdujo. Preguntar el tamaño con
// `f.Seek(0, io.SeekEnd)` en vez de con `f.Stat()` parece más elegante —hace de una
// vez lo que ambos hacían— pero sobre un descriptor de DIRECTORIO `Seek` al final
// devuelve un tamaño enorme, del orden de 2^63 en ext4, y `make([]byte, tamano-offset)`
// revienta con "len out of range".
//
// Que un log acabe siendo un directorio no es una hipótesis: el servicio escribe su
// log, alguien lo borra y crea un directorio en su sitio —una migración, un script de
// despliegue, un `mkdir` a ciegas— y el siguiente tick de la consola lo lee.
//
// Lo que se comprueba es que NO PANIQUE y que devuelva un error: el caller trata un
// error como "esta lectura no vale" y sigue, mientras que un panic en el tick de la
// consola se come la TUI entera.
//
// MEDIDO: en el runner de CI (ext4) el fallo era un panic; en la máquina de desarrollo
// el mismo caso pasaba, porque el tamaño del directorio que devuelve `Seek` depende
// del sistema de ficheros. Un test que sólo falla en CI también es un test que no
// avisó a tiempo.
func TestReadNewNoRevientaConUnLogQueEsUnDirectorio(t *testing.T) {
	asDir := filepath.Join(t.TempDir(), "log-es-un-directorio")
	if err := os.MkdirAll(filepath.Join(asDir, "con-contenido"), 0o755); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ReadNew reventó con un log que es un directorio: %v. Un panic aquí se come "+
				"la TUI, y un error de lectura es algo que el tick ya sabe manejar", r)
		}
	}()

	for _, off := range []int64{0, 1, 4096, 1 << 40} {
		data, nuevo, err := ReadNew(asDir, off)
		if err == nil {
			t.Errorf("ReadNew(%q, %d) = %q sin error: un directorio no es un log", asDir, off, data)
		}
		if nuevo > off {
			t.Errorf("ReadNew(%q, %d) dejó el offset en %d: avanzar sobre una lectura fallida "+
				"perdería los bytes que no se leyeron", asDir, off, nuevo)
		}
	}
}

// TestReadNewPropagaElFalloDePreguntarElTamaño: la rama que hace que exista
// `readNew`.
//
// Preguntar el tamaño por el descriptor puede fallar aunque el `Open` haya tenido
// éxito: entre los dos, el log se rota o se borra, y `Stat` devuelve ENOENT. Ese
// error tiene que salir como error y dejar el offset intacto, igual que cualquier otro
// fallo de lectura —si no, el siguiente tick leería desde más allá y esas líneas no se
// verían nunca más—.
func TestReadNewPropagaElFalloDePreguntarElTamaño(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(path, []byte("contenido\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const offset = 3
	data, nuevo, err := readNew(path, offset, func(*os.File) (int64, error) {
		return 0, errors.New("el log se rotó justo antes de preguntar")
	})
	if err == nil {
		t.Fatal("readNew = nil cuando no se puede preguntar el tamaño: el buffer se haría con un " +
			"tamaño inventado")
	}
	if data != "" {
		t.Errorf("data = %q con un fallo al preguntar, want vacío", data)
	}
	if nuevo != offset {
		t.Errorf("offset = %d tras un fallo al preguntar, want %d", nuevo, offset)
	}
}

// TestReadNewConUnOffsetNegativoNoRevienta: el suelo que evita el desbordamiento.
//
// Nadie produce hoy un offset negativo —el que entra es la vuelta anterior de esta
// misma función—, pero un entero con signo que se cuele por un desbordamiento en la
// suma `offset + n` haría que `size - offset` fuera enorme. Sin el suelo, eso no es un
// log vacío: es un `make` de exabytes, y por tanto un panic en la TUI.
//
// El suelo tiene que existir aunque el test sea hipotético: es la diferencia entre
// "un caso raro da un error" y "un caso raro mata la sesión de quien está mirando la
// consola".
func TestReadNewConUnOffsetNegativoNoRevienta(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	const contenido = "linea\n"
	if err := os.WriteFile(path, []byte(contenido), 0o644); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ReadNew reventó con un offset negativo: %v", r)
		}
	}()

	data, nuevo, err := ReadNew(path, -1)
	if err != nil {
		t.Fatalf("ReadNew con offset negativo = %v, want nil: un offset imposible se corrige, no "+
			"se devuelve como error", err)
	}
	if data != contenido {
		t.Errorf("data = %q, want %q: el suelo a 0 hace que se lea el fichero entero", data, contenido)
	}
	if nuevo != int64(len(contenido)) {
		t.Errorf("offset = %d, want %d", nuevo, len(contenido))
	}
}

// TestTamanoDeFallaCuandoElDescriptorYaNoSirve: el error de `f.Stat()`.
//
// `readNew` existe para poder inyectar la pregunta por el tamaño, y esta es la razón:
// `f.Stat()` sobre un descriptor que ya no vale devuelve error, y sin el seam esa
// comprobación era una línea que nadie podía ejecutar.
//
// MEDIDO: cerrar el descriptor por debajo del `Stat` lo hace fallar con EBADF, que es
// exactamente el estado en el que queda un descriptor si el fichero se borra y el
// sistema lo recicla. Es un caso real en una máquina con churn de inodos.
func TestTamanoDeFallaCuandoElDescriptorYaNoSirve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(path, []byte("contenido\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := tamanoDe(f); err == nil {
		t.Error("tamanoDe = nil sobre un descriptor cerrado: un tamaño inventado a partir de un " +
			"fd muerto es un buffer de tamaño arbitrario")
	}
}
