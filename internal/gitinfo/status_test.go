package gitinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Fuera de un repo, ReadStatus devuelve error y rama vacía.
func TestReadStatusNoRepo(t *testing.T) {
	st := ReadStatus(t.TempDir())
	if st.Branch != "" {
		t.Errorf("Branch = %q, want vacío", st.Branch)
	}
	if st.Err == "" {
		t.Error("se esperaba error git fuera de un repo")
	}
}

// En un repo real, ReadStatus lee rama, estado sucio y commits.
func TestReadStatusRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no disponible")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "first")

	st := ReadStatus(dir)
	if st.Err != "" {
		t.Fatalf("Err = %q", st.Err)
	}
	if st.Branch != "main" {
		t.Errorf("Branch = %q, want main", st.Branch)
	}
	if st.Dirty() {
		t.Errorf("repo recién commiteado no debe estar sucio: %v", st.Changed)
	}
	if len(st.Commits) == 0 || !strings.Contains(st.Commits[0], "first") {
		t.Errorf("Commits = %v, want el commit inicial", st.Commits)
	}

	// Un fichero modificado marca el repo como sucio.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := ReadStatus(dir); !st.Dirty() {
		t.Error("con cambios sin commitear Dirty() debe ser true")
	}
}
