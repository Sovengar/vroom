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

// DeriveName returns the PRIMARY candidate of the ladder: named_with_auto_fallback gives the stable URL that OAuth callbacks and CORS need, auto gives the branch-derived one; auto is branch scope and never sees the worktree path, so two worktrees on one branch derive the same name and the second gets a clean conflict with the first route intact instead of a second address; and deriving from the branch means `git branch -m` renames the route, which is why Reconcile is mandatory, though portless's own convention derives from the branch too (M13).
func DeriveName(mode, routeName, branch, project string) (string, error) {
	switch mode {
	case RouteModeNamedWithAutoFallback:
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

// RouteCandidates is the claim ladder: named_with_auto_fallback tries route_name first and only when another worktree already holds it falls back to <branch>.<project>, so whoever starts first keeps the stable URL and every other worktree still gets an address instead of a bare route_conflict. auto returns its single candidate on purpose — its value is that the hostname does NOT depend on who ran first. A fallback that would equal the primary, or that cannot be derived at all, collapses to the single primary: that is exactly the pre-ladder behaviour.
func RouteCandidates(mode, routeName, branch, project string) ([]string, error) {
	primary, err := DeriveName(mode, routeName, branch, project)
	if err != nil {
		return nil, err
	}
	if mode != RouteModeNamedWithAutoFallback {
		return []string{primary}, nil
	}
	fallback, err := DeriveName(RouteModeAuto, "", branch, project)
	if err != nil || fallback == primary {
		return []string{primary}, nil
	}
	return []string{primary, fallback}, nil
}
