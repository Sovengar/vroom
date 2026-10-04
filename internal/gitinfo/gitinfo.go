// Package gitinfo reads the current branch straight from disk and never spawns the git binary, so a machine without git still shows branches (see docs/adr/adr-0011-worktree-topology-discovery-boundary.md).
package gitinfo

import (
	"os"
	"path/filepath"
	"strings"
)

func Branch(path string) string {
	head, ok := readHEAD(path)
	if !ok {
		return ""
	}
	return parseHEAD(head)
}

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
