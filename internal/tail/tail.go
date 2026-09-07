// Package tail proporciona lectura incremental de ficheros de log para la
// consola en tiempo real (spec 0002 R19): solo bytes nuevos por llamada,
// strip de ANSI y cap de buffer.
package tail

import (
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
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", offset, err
	}
	if info.Size() < offset {
		offset = 0 // truncado/rotación: releer completo
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return "", offset, err
	}
	buf := make([]byte, info.Size()-offset)
	n, err := f.Read(buf)
	if n == 0 && err != nil {
		return "", offset, err
	}
	return string(buf[:n]), offset + int64(n), nil
}

// StripANSI elimina secuencias de escape ANSI (CSI, OSC y escapes de 2
// bytes) de s. Los logs de servicios traen color y el viewport no los
// interpreta: sin strip el ancho se rompe (spec R19/S19.5).
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
// y sin partir runes multibyte (spec R19/S19.6).
func CapBuffer(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	// Cortar en la primera línea completa dentro de la ventana final.
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
