// Package worktree descubre la topología de repositorios git (worktrees
// linkeados y bare repos). Es el único punto del proyecto autorizado a
// spawnar el binario git: internal/gitinfo sigue leyendo HEAD solo de
// disco. La topología se expone como datos planos para que el scanner
// anote los proyectos sin construir una estructura anidada.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ErrGitUnavailable indica que el binario git no está disponible en PATH.
// El scanner lo usa para degradar por repo sin romper el scan completo.
var ErrGitUnavailable = errors.New("git binary not available")

// DefaultTimeout acota cada invocación de git por repo (plan 0011: la
// dependencia del binario git en el scan debe ser acotada y degradable).
const DefaultTimeout = 3 * time.Second

// listTimeout y listWaitDelay son variables para que los tests puedan
// acortarlos.
//
// listTimeout acota la invocación de git. listWaitDelay acota el cierre de
// los pipes tras el deadline del contexto: sin él, un proceso descendiente
// vivo con los pipes abiertos puede colgar cmd.Wait() más allá del
// timeout, así que el timeout no acotaría el tiempo de reloj real.
var (
	listTimeout   = DefaultTimeout
	listWaitDelay = time.Second
)

// Worktree es una entrada de `git worktree list --porcelain`.
type Worktree struct {
	Path     string // ruta absoluta del worktree
	HEAD     string // sha completo
	Branch   string // ref corta (refs/heads/x → x); "" si detached o bare
	Detached bool   // HEAD detached
	Bare     bool   // el propio repo es bare
	Prunable bool   // worktree ausente/obsoleto según git
}

// gitPath busca el binario git en PATH.
func gitPath() string {
	if p, err := exec.LookPath("git"); err == nil {
		return p
	}
	return ""
}

// List devuelve los worktrees registrados del repo que contiene dir.
// Devuelve ErrGitUnavailable si git no está disponible; cualquier otro
// error (exit != 0, salida inválida) describe el fallo de topología.
func List(dir string) ([]Worktree, error) {
	return listWith(dir, gitPath())
}

// listWith es List con el binario ya resuelto (inyectable en tests).
func listWith(dir, git string) ([]Worktree, error) {
	if git == "" {
		return nil, ErrGitUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, "-C", dir, "worktree", "list", "--porcelain")
	cmd.WaitDelay = listWaitDelay
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Degradación por timeout: se envuelve el error de contexto
			// para que errors.Is(err, context.DeadlineExceeded) funcione.
			return nil, fmt.Errorf("git worktree list timed out in %s: %w", dir, ctxErr)
		}
		return nil, fmt.Errorf("git worktree list failed in %s: %w", dir, err)
	}
	return ParsePorcelain(string(out))
}

// ParsePorcelain interpreta la salida de `git worktree list --porcelain`.
// Función pura (sin exec) para testear table-driven: bloques separados por
// línea en blanco con claves `worktree`, `HEAD`, `branch`, `detached`,
// `bare` y `prunable`. Salida vacía = sin worktrees; salida no vacía sin
// ningún bloque `worktree` = salida inválida.
func ParsePorcelain(out string) ([]Worktree, error) {
	var wts []Worktree
	var cur *Worktree
	flush := func() {
		if cur != nil {
			wts = append(wts, *cur)
			cur = nil
		}
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			flush()
			cur = &Worktree{Path: strings.TrimSpace(val)}
		case "HEAD":
			if cur != nil {
				cur.HEAD = strings.TrimSpace(val)
			}
		case "branch":
			if cur != nil {
				cur.Branch = shortRef(strings.TrimSpace(val))
			}
		case "detached":
			if cur != nil {
				cur.Detached = true
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
			}
		}
	}
	flush()
	if len(wts) == 0 && strings.TrimSpace(out) != "" {
		return nil, fmt.Errorf("invalid git worktree porcelain output")
	}
	return wts, nil
}

// shortRef acorta una ref de git: refs/heads/x → x; otras refs se dejan
// tal cual (mismo criterio que gitinfo.parseHEAD).
func shortRef(ref string) string {
	if branch, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return branch
	}
	return ref
}

// IsBareRepo reporta si dir es un bare repo. Heurística reforzada (plan
// 0011 decisión 7): conjunción HEAD + objects/ + refs/ presentes, sin
// .git, y con el marcador autoritativo core.bare = true que escriben
// `git init --bare` / `git clone --bare` (elimina falsos positivos).
func IsBareRepo(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return false // un repo normal (dir o file) nunca es bare
	}
	for _, name := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return hasBareMarker(filepath.Join(dir, "config"))
}

// hasBareMarker busca la clave bare = true en el config de git.
func hasBareMarker(configPath string) bool {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "bare = true" {
			return true
		}
	}
	return false
}
