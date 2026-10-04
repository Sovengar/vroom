// Package tail proporciona lectura incremental de ficheros de log para la
// consola en tiempo real: solo bytes nuevos por llamada,
// strip de ANSI y cap de buffer.
package tail

import (
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// ReadNew devuelve los bytes añadidos a path desde offset, junto al nuevo
// offset. Si el fichero no existe todavía (servicio sin arrancar) devuelve
// vacío sin error. Si el fichero quedó más pequeño que offset (rotación o
// truncado) se relee desde el principio.
func ReadNew(path string, offset int64) (data string, newOffset int64, err error) {
	return readNew(path, offset, tamanoDe)
}

// tamanoDe pregunta el tamaño por el descriptor ya abierto, y no por el nombre.
//
// Va inyectado porque su error sí se puede provocar: entre el `os.Open` y la
// pregunta, el log puede rotarse o desaparecer, y `Stat` devuelve ENOENT. Antes
// esto estaba en la línea de `f.Stat()` con su error, que era una comprobación que
// nadie había podido ejecutar; ahora es una función, y su fallo tiene un test.
func tamanoDe(f *os.File) (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// readNew es `ReadNew` con la pregunta por el tamaño inyectada.
//
// MEDIDO (bug, encontrado en CI): la versión anterior preguntaba el tamaño con
// `f.Seek(0, io.SeekEnd)`, que parece más elegante porque hace las dos cosas que
// hacían `Stat` y `Seek` a la vez. No sirve: sobre un descriptor de DIRECTORIO,
// `Seek` al final devuelve un tamaño enorme —en ext4, del orden de 2^63—, así que
// `make([]byte, tamano-offset)` reventaba con "len out of range". Y un log que es un
// directorio es un caso real: el log se crea, alguien lo sustituye por un directorio,
// y el tick de la consola lo lee.
//
// El panic es peor que el error que el código anterior daba. `Stat` sobre un
// directorio devuelve su tamaño de bloque, que es pequeño, y el `ReadAt` posterior
// falla con EISDIR como debe.
func readNew(path string, offset int64, tamano func(*os.File) (int64, error)) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", offset, nil
		}
		return "", offset, err
	}
	defer func() { _ = f.Close() }()

	size, err := tamano(f)
	if err != nil {
		return "", offset, err
	}
	// Tres suelos, y los tres importan para que el `make` de abajo no pueda reventar.
	// El primero es la rotación: un log truncado por debajo del offset se relee entero.
	if size < offset {
		offset = 0
	}
	// El segundo es un offset negativo o desbordado. Nadie lo produce hoy —el offset
	// viene de la vuelta anterior de esta misma función—, pero un entero con signo que
	// se cuela por un desbordamiento en la suma `offset + n` haría que `size-offset`
	// fuera enorme, y eso es un panic en la TUI en vez de un log vacío.
	if offset < 0 {
		offset = 0
	}
	// El tercero, para que el buffer sea válido por construcción.
	buf := make([]byte, max(size-offset, 0))

	// `ReadAt` y no `Read`: no mueve el descriptor, así que no depende de en qué
	// posición quedó. `io.EOF` NO es un fallo —es lo que devuelve la última lectura de
	// un fichero, y también una lectura que se topó con una rotación a medio camino—.
	// Cualquier otro error sí se propaga, y aquí hay uno que de verdad importa: `EISDIR`
	// cuando el log es un directorio.
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", offset, err
	}
	return string(buf[:n]), offset + int64(n), nil
}

// StripANSI elimina secuencias de escape ANSI (CSI, OSC y escapes de 2
// bytes) de s. Los logs de servicios traen color y el viewport no los
// interpreta: sin strip el ancho se rompe.
func StripANSI(s string) string {
	if !strings.ContainsRune(s, '\x1b') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != '\x1b' {
			b.WriteByte(c)
			i++
			continue
		}
		// \x1b[ ... final 0x40-0x7E (CSI)
		if i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7E) {
				i++
			}
			if i < len(s) {
				i++ // consume el byte final
			}
			continue
		}
		// \x1b] / \x1bP / \x1bX / \x1b^ / \x1b_ : cadena terminada en BEL o ESC \
		if i+1 < len(s) && strings.IndexByte("]PX^_", s[i+1]) >= 0 {
			i += 2
			for i < len(s) {
				if s[i] == '\x07' {
					i++
					break
				}
				if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
			continue
		}
		i += 2 // escape de 2 bytes (\x1b + 1)
	}
	return b.String()
}

// CapBuffer recorta s a como máximo maxBytes conservando el final (el
// contenido más reciente), cortando por línea completa cuando es posible
// y sin partir runes multibyte.
func CapBuffer(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	// Cortar en la primera línea completa dentro de la ventana final.
	//
	// El corte va hacia ADELANTE a propósito, y por eso este es el diseño
	// correcto: cualquier línea que empiece en o después de `cut` cabe en
	// maxBytes, porque a partir de `cut` quedan exactamente maxBytes. Buscar hacia
	// ATRÁS en cambio daría un sufijo de maxBytes o más, y con líneas largas que
	// no cabe ninguna: se acabaría partiendo la línea por la mitad.
	//
	// El precio es que se puede descartar contenido reciente: el buffer empieza en
	// el principio de la línea siguiente a la ventana. Es lo que hace que el
	// principio no sea texto partido, que es lo que importa al leerlo.
	cut := len(s) - maxBytes
	if nl := strings.IndexByte(s[cut:], '\n'); nl >= 0 {
		cut = cut + nl + 1
		return s[cut:]
	}
	// Una sola línea enorme: cortar por rune para no partir multibyte.
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return s[cut:]
}
