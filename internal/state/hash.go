package state

import (
	"crypto/sha256"
	"encoding/hex"
)

// PathKey genera la clave de servicio: primeros 8 hex chars del SHA-256
// del path absoluto del proyecto. Evita colisiones entre proyectos
// homónimos en rutas distintas (S4.1).
func PathKey(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(sum[:])[:8]
}
