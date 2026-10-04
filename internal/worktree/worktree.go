// Package worktree discovers git repository topology and is the only place in the project allowed to spawn the git binary; it returns flat data so internal/gitinfo stays disk-only (see docs/adr/adr-0011-worktree-topology-discovery-boundary.md).
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Kept distinct from topology errors so the scanner can degrade per repo without hiding a real git failure.
var ErrGitUnavailable = errors.New("git binary not available")

// A per-repo bound: one hanging git must never stall the whole scan.
const DefaultTimeout = 3 * time.Second

// WaitDelay bounds the pipe close after the context deadline, else a live descendant holding the pipes makes Wait outlast the timeout.
var (
	listTimeout   = DefaultTimeout
	listWaitDelay = time.Second
)

type Worktree struct {
	Path     string
	HEAD     string
	Branch   string
	Detached bool
	Bare     bool
	Prunable bool
}

func gitPath() string {
	if p, err := exec.LookPath("git"); err == nil {
		return p
	}
	return ""
}

func List(dir string) ([]Worktree, error) {
	return listWith(dir, gitPath())
}

func listWith(dir, git string) ([]Worktree, error) {
	if git == "" {
		return nil, ErrGitUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, "-C", dir, "worktree", "list", "--porcelain")
	cmd.WaitDelay = listWaitDelay
	// stderr stays out of stdout because any git warning would corrupt the porcelain parse.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// wrapped so errors.Is(err, context.DeadlineExceeded) still matches.
			return nil, fmt.Errorf("git worktree list timed out in %s: %w", dir, ctxErr)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git worktree list failed in %s: %w: %s", dir, err, msg)
		}
		return nil, fmt.Errorf("git worktree list failed in %s: %w", dir, err)
	}
	return ParsePorcelain(stdout.String())
}

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
			path := strings.TrimSpace(val)
			if path == "" {
				continue
			}
			cur = &Worktree{Path: path}
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

func shortRef(ref string) string {
	if branch, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
		return branch
	}
	return ref
}

// The conjunction plus core.bare is what removes false positives, so do not relax it to a single marker.
func IsBareRepo(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return false
	}
	for _, name := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return hasBareMarker(filepath.Join(dir, "config"))
}

// Scoped to the [core] section, else a bare key anywhere makes any dir with HEAD/objects/refs look like a bare repo.
func hasBareMarker(configPath string) bool {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	inCore := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(stripConfigComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inCore = strings.EqualFold(line, "[core]")
			continue
		}
		if !inCore {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "bare") {
			continue
		}
		if isTrueConfigValue(strings.TrimSpace(val)) {
			return true
		}
	}
	return false
}

// Single quotes are literal in git config, so only # and ; outside quotes start a comment.
func stripConfigComment(line string) string {
	inQuote := false
	for i, r := range line {
		switch {
		case r == '\'':
			inQuote = !inQuote
		case (r == '#' || r == ';') && !inQuote:
			return line[:i]
		}
	}
	return line
}

func isTrueConfigValue(v string) bool {
	switch strings.ToLower(v) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}
