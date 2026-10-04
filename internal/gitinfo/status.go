package gitinfo

import (
	"os/exec"
	"strings"
)

type Status struct {
	Branch  string
	Changed []string
	Commits []string
	Err     string
}

func (s Status) Dirty() bool { return len(s.Changed) > 0 }

func ReadStatus(path string) Status {
	st := Status{Branch: Branch(path)}

	statusOut, err := runGit(path, "status", "--porcelain")
	if err != nil {
		st.Err = err.Error()
		return st
	}
	for _, line := range strings.Split(statusOut, "\n") {
		if strings.TrimSpace(line) != "" {
			st.Changed = append(st.Changed, line)
		}
	}

	if logOut, err := runGit(path, "log", "--oneline", "-n", "6"); err == nil {
		for _, line := range strings.Split(logOut, "\n") {
			if strings.TrimSpace(line) != "" {
				st.Commits = append(st.Commits, line)
			}
		}
	}
	return st
}

func runGit(path string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return out.String(), err
	}
	return strings.TrimRight(out.String(), "\n"), nil
}
