package portless

import (
	"fmt"
	"regexp"
	"strings"
)

const HostSuffix = ".localhost"

// Measured: portless rejects underscores, spaces, colons and accents, and silently truncates at a slash, so "Feat/My_Branch.proj" once registered as feat.localhost - another project's name.
var sanitiser = regexp.MustCompile(`[^a-z0-9.-]+`)

// consecutiveDots because portless rejects repeated dots too ("consecutive dots are not allowed").
var consecutiveDots = regexp.MustCompile(`\.{2,}`)

// Hostname normalizes exactly like portless does, so a name already ending in HostSuffix is not doubled and either form works for alias and --remove.
func Hostname(name string) string {
	n := sanitizeName(name)
	if n == "" {
		return ""
	}
	if strings.HasSuffix(n, HostSuffix) {
		return n
	}
	return n + HostSuffix
}

func sanitizeName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.ReplaceAll(n, HostSuffix, "")
	n = sanitiser.ReplaceAllString(n, "-")
	n = consecutiveDots.ReplaceAllString(n, ".")
	n = strings.Trim(n, "-.")
	n = strings.TrimRight(n, "-")
	if n == "" {
		return ""
	}
	return n
}

// DeriveName returns auto (branch + project) or named (a stable URL that OAuth callbacks and CORS need); auto is branch scope and never sees the worktree path, so two worktrees on one branch derive the same name and the second gets a clean conflict with the first route intact instead of a second address; and deriving from the branch means `git branch -m` renames the route, which is why Reconcile is mandatory, though portless's own convention derives from the branch too (M13).
func DeriveName(mode, routeName, branch, project string) (string, error) {
	switch mode {
	case RouteModeNamed:
		n := sanitizeName(routeName)
		if n == "" {
			return "", fmt.Errorf("route_name %q is not a usable hostname (portless accepts only lowercase letters, digits, hyphens and dots)", routeName)
		}
		return n, nil
	case RouteModeAuto:
		if b := sanitizeName(branch); b != "" {
			return b + "." + sanitizeName(project), nil
		}
		n := sanitizeName(project)
		if n == "" {
			return "", fmt.Errorf("project name %q is not a usable hostname", project)
		}
		return n, nil
	default:
		return "", fmt.Errorf("unknown route_mode %q", mode)
	}
}
