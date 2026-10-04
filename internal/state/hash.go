package state

import (
	"crypto/sha256"
	"encoding/hex"
)

// PathKey hashes the absolute path so same-named projects in different paths get distinct service directories.
func PathKey(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(sum[:])[:8]
}
