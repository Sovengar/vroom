package portless

import (
	"fmt"
	"regexp"
	"strings"

	"vroom/internal/manifest"
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

// DeriveName returns the PRIMARY candidate of the ladder: by_hostname gives the stable URL that OAuth callbacks and CORS need, by_workspace_hostname gives the branch-derived one; the workspace generation is branch scope and never sees the worktree path, so two worktrees on one branch derive the same name and the second gets a clean conflict with the first route intact instead of a second address; and deriving from the branch means `git branch -m` renames the route, which is why Reconcile is mandatory, though portless's own convention derives from the branch too (M13).
func DeriveName(gen, routeName, branch, project string) (string, error) {
	switch gen {
	case manifest.URLGenByHostname, manifest.URLGenByHostnameOrWorkspace:
		n := sanitizeName(routeName)
		if n == "" {
			return "", fmt.Errorf("route_name %q is not a usable hostname (portless accepts only lowercase letters, digits, hyphens and dots)", routeName)
		}
		return n, nil
	case manifest.URLGenByWorkspaceHostname:
		if b := sanitizeName(branch); b != "" {
			return b + "." + sanitizeName(project), nil
		}
		n := sanitizeName(project)
		if n == "" {
			return "", fmt.Errorf("project name %q is not a usable hostname", project)
		}
		return n, nil
	default:
		return "", fmt.Errorf("url_generation %q publishes no hostname", gen)
	}
}

// RouteCandidates is the claim ladder: by_hostname_or_workspace tries route_name first and only when another worktree already holds it falls back to <branch>.<project>, so whoever starts first keeps the stable URL and every other worktree still gets an address instead of a bare route_conflict. by_hostname and by_workspace_hostname return their single candidate on purpose — by_hostname's value is that a foreign holder is an ERROR instead of a silent move, and by_workspace_hostname's is that the hostname does NOT depend on who ran first. A fallback that would equal the primary, or that cannot be derived at all, collapses to the single primary: that is exactly the pre-ladder behaviour.
func RouteCandidates(gen, routeName, branch, project string) ([]string, error) {
	primary, err := DeriveName(gen, routeName, branch, project)
	if err != nil {
		return nil, err
	}
	if gen != manifest.URLGenByHostnameOrWorkspace {
		return []string{primary}, nil
	}
	fallback, err := DeriveName(manifest.URLGenByWorkspaceHostname, "", branch, project)
	if err != nil || fallback == primary {
		return []string{primary}, nil
	}
	return []string{primary, fallback}, nil
}
