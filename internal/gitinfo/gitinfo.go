// Package gitinfo lee la rama git actual de un proyecto directamente del
// disco, sin spawnar el binario git: instantáneo y
// testeable. Soporta repos normales y worktrees.
package gitinfo

import (
	"os"
	"path/filepath"
	"strings"
)

// Branch devuelve la rama actual del repo en path. Si no hay repo, el HEAD
// es ilegible o está en un estado desconocido devuelve "" (la UI omite la
// fila). HEAD detached se muestra como sha corto + " (detached)".
func Branch(path string) string {
	head, ok := readHEAD(path)
	if !ok {
		return ""
	}
	return parseHEAD(head)
}

// readHEAD localiza y lee el fichero HEAD del repo en path, incluyendo
// worktrees (donde .git es un fichero con "gitdir: <ruta>").
func readHEAD(path string) (string, bool) {
	gitPath := filepath.Join(path, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return "", false
	}
	headPath := filepath.Join(gitPath, "HEAD")
	if !info.IsDir() {
		raw, err := os.ReadFile(gitPath)
		if err != nil {
			return "", false
		}
		line := strings.TrimSpace(string(raw))
		gitdir, ok := strings.CutPrefix(line, "gitdir:")
		if !ok {
			return "", false
		}
		gitdir = strings.TrimSpace(gitdir)
		if !filepath.IsAbs(gitdir) {
			gitdir = filepath.Join(path, gitdir)
		}
		headPath = filepath.Join(gitdir, "HEAD")
	}
	raw, err := os.ReadFile(headPath)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}

// parseHEAD interpreta el contenido de HEAD: "ref: refs/heads/X" → "X";
// sha de 40 hex (detached) → "abc1234 (detached)"; refs inusuales se
// muestran tal cual.
func parseHEAD(head string) string {
	if ref, ok := strings.CutPrefix(head, "ref: "); ok {
		ref = strings.TrimSpace(ref)
		if branch, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			return branch
		}
		return ref
	}
	if len(head) >= 7 && isHex(head[:7]) {
		return head[:7] + " (detached)"
	}
	return ""
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
