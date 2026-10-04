// Package tail proporciona lectura incremental de ficheros de log para la
// consola en tiempo real: solo bytes nuevos por llamada,
// strip de ANSI y cap de buffer.
package tail

import (
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
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", offset, nil
		}
		return "", offset, err
	}
	defer func() { _ = f.Close() }()

	// El tamaño se saca del descriptor y no del nombre. La razón no es el rendimiento:
	// un `f.Stat()` tiene su propio error, y como `f` viene de un `os.Open` que acaba
	// de devolver nil, ese error no se puede provocar nunca. Es decir, la línea que
	// lo comprobaba era código muerto con aspecto de comprobación.
	//
	// `Seek(0, io.SeekEnd)` es la forma de preguntar "cuánto tiene" por el descriptor
	// que ya está abierto, así que devuelve la posición y hace las dos cosas que
	// hacían `Stat` y `Seek` juntos. `Seek` sí puede fallar de verdad —un fichero que
	// se borra o se cierra entre medias—, y por eso su error se comprueba.
	//
	// La lectura va con `ReadAt` y no con `Read` porque `ReadAt` no mueve el
	// descriptor: el `Seek(0, io.SeekEnd)` de arriba lo dejó al final, y un `Read`
	// de ahí no leería nada. `io.EOF` está permitido porque es lo que devuelve la
	// última lectura de un fichero: no es un fallo, es que se acabó.
	tamano, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return "", offset, err
	}
	if tamano < offset {
		offset = 0 // truncado/rotación: releer completo
	}
	buf := make([]byte, tamano-offset)
	// El error de `ReadAt` se descarta a propósito, y no por descuido. Con un log
	// rotándose o truncándose entre el `Seek` y el `Read` —que es justo lo que pasa
	// con un reinicio de servicio— `ReadAt` devuelve menos bytes de los pedidos, o un
	// error, y lo único que importa es devolver lo que se pudo leer. `ReadAt`
	// garantiza `0 <= n <= len(buf)`, así que `buf[:n]` es siempre válido.
	//
	// La otra alternativa —propagar el error— dejaría la consola en blanco por un
	// fichero que se movió mientras lo leíamos, y el próximo tick lo volvería a
	// tener: un parpadeo por algo que no es un fallo del servicio.
	n, _ := f.ReadAt(buf, offset)
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
