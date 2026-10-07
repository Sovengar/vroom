package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Must match gitinfo.parseHEAD's criterion, or the same repo gets two branch names and the auto portless route path depends on which one ran.
func TestShortRefAcortaSoloLasRamasDeHeads(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"branch", "refs/heads/main", "main"},
		{"branch with slash", "refs/heads/feature/login", "feature/login"},
		{"tag", "refs/tags/v1.0", "refs/tags/v1.0"},
		{"remote", "refs/remotes/origin/main", "refs/remotes/origin/main"},
		{"loose HEAD", "HEAD", "HEAD"},
		{"empty", "", ""},
		{"partial prefix", "refs/head/main", "refs/head/main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shortRef(tt.in); got != tt.want {
				t.Errorf("shortRef(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// HEAD, objects and refs without the core.bare = true marker are not enough, since a stray objects/ directory is the likeliest false positive.
func TestIsBareRepoExigeLosTresYElMarcador(t *testing.T) {
	t.Run("all three but without marker: NOT bare", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []string{"HEAD", "objects", "refs"} {
			if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		write(t, filepath.Join(dir, "config"), "[core]\n\tbare = false\n")
		if IsBareRepo(dir) {
			t.Error("without core.bare = true it is NOT a bare repo: any folder with objects/ would look like one")
		}
	})

	t.Run("without config: NOT bare", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []string{"HEAD", "objects", "refs"} {
			if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if IsBareRepo(dir) {
			t.Error("without config nothing can be proven: declaring it bare would be guessing")
		}
	})

	t.Run("a normal repo with .git: NOT bare", func(t *testing.T) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, ".git", "config"), "[core]\n\tbare = true\n")
		if IsBareRepo(dir) {
			t.Error("a directory with .git has work inside: it is never a bare repo")
		}
	})

	t.Run("a worktree with .git as a file: NOT bare", func(t *testing.T) {
		// A linked worktree's .git is a file and its main repo IS bare, which makes this the most confusable case.
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /path\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if IsBareRepo(dir) {
			t.Error("a worktree is not a bare repo, even if its main repo is")
		}
	})

	t.Run("complete bare: it is", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []string{"HEAD", "objects", "refs"} {
			if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		write(t, filepath.Join(dir, "config"), "[core]\n\tbare = true\n")
		if !IsBareRepo(dir) {
			t.Error("the three files plus core.bare = true is a bare repo")
		}
	})

	t.Run("missing one of the three", func(t *testing.T) {
		for _, falta := range []string{"HEAD", "objects", "refs"} {
			dir := t.TempDir()
			for _, n := range []string{"HEAD", "objects", "refs"} {
				if n == falta {
					continue
				}
				if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write(t, filepath.Join(dir, "config"), "[core]\n\tbare = true\n")
			if IsBareRepo(dir) {
				t.Errorf("without %s it is not a bare repo", falta)
			}
		}
	})
}

// The key must be looked up inside [core] only, or a [miapp] bare = true build variable makes the project appear twice in the scan.
func TestHasBareMarkerSoloCuentaLaSeccionCore(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   bool
	}{
		{"core with bare", "[core]\n\tbare = true\n", true},
		{"core with bare and other keys", "[core]\n\tbare = true\n\tlogallrefupdates = false\n", true},
		{"[core] section with uppercase", "[Core]\n\tbare = true\n", true},
		{"key in lowercase", "[core]\n\tBare = true\n", true},
		{"repeated key: first true wins", "[core]\n\tbare = false\n\tbare = true\n", true},
		// MEASURED: with a repeated key the first true wins, unlike git's last-wins; git never writes the marker twice and reimplementing its precedence would cover a config git cannot produce.
		{"repeated key: first true wins anyway", "[core]\n\tbare = true\n\tbare = false\n", true},
		{"other section", "[myapp]\n\tbare = true\n", false},
		{"core without the key", "[core]\n\tfilemode = true\n", false},
		{"without sections", "bare = true\n", false},
		{"core section without equals", "[core]\n\tbare true\n", false},
		{"comment at end of line", "[core]\n\tbare = true # the marker\n", true},
		{"empty line and spaces", "[core]\n\n   \n\tbare = true\n", true},
		// MEASURED: a single-quoted value does not count because the quotes are not stripped, though git would accept it; the marker is written by git and never quoted.
		{"quoted value", "[core]\n\tbare = 'true'\n", false},
		{"non-boolean value", "[core]\n\tbare = maybe\n", false},
		{"empty value", "[core]\n\tbare =\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := write(t, filepath.Join(t.TempDir(), "config"), tt.config)
			if got := hasBareMarker(path); got != tt.want {
				t.Errorf("hasBareMarker(%q) = %v, want %v", tt.config, got, tt.want)
			}
		})
	}
}

// Fail closed: a false positive adds a row the user notices as a mismatched group count, while a false negative only shows up as an orphan worktree.
func TestHasBareMarkerConConfigIlegibleESFalse(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a file without permission: the case cannot be triggered")
	}
	path := write(t, filepath.Join(t.TempDir(), "config"), "[core]\n\tbare = true\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	if hasBareMarker(path) {
		t.Error("an unreadable config cannot prove anything: declaring it bare would be guessing")
	}
}

// This is what separates a config parser from strings.Cut: a value like C:\Users\me#my-repo would lose half the path.
func TestStripConfigCommentRespetaLasComillasSimples(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no comment", "[core]\n\tbare = true", "[core]\n\tbare = true"},
		{"comment with hash", "\tbare = true # this", "\tbare = true "},
		{"comment with semicolon", "\tbare = true ; esto", "\tbare = true "},
		{"hash inside quotes", "\tpath = 'C:/Users/me#repo'", "\tpath = 'C:/Users/me#repo'"},
		{"semicolon inside quotes", "\tpath = 'a;b'", "\tpath = 'a;b'"},
		{"comment after quotes", "\tpath = 'a' # note", "\tpath = 'a' "},
		// MEASURED: an unterminated quote makes everything after it quoted so the hash survives, which is the safe direction for a truncated config.
		{"unclosed quote", "\tpath = 'a#b", "\tpath = 'a#b"},
		{"comment at start", "# all comment", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripConfigComment(tt.in); got != tt.want {
				t.Errorf("stripConfigComment(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// git accepts many truthy spellings, and understanding only "true" would drop a bare = 1 repo from the scan and orphan its worktrees.
func TestIsTrueConfigValueAceptaLasFormasDeGit(t *testing.T) {
	// MEASURED: no TrimSpace here, because callers already trim it and a second read of the same rule would live in two places.
	for _, v := range []string{"true", "TRUE", "True", "1", "yes", "on"} {
		if !isTrueConfigValue(v) {
			t.Errorf("isTrueConfigValue(%q) = false: git accepts it as true", v)
		}
	}
	for _, v := range []string{"false", "FALSE", "0", "no", "off", "", "maybe", "2"} {
		if isTrueConfigValue(v) {
			t.Errorf("isTrueConfigValue(%q) = true: it is not a truthy value in git", v)
		}
	}
	if isTrueConfigValue("  true  ") {
		t.Error("isTrueConfigValueTrimmed")
	}
}

// The hand-made file tests check the conjunction; this one checks that the conjunction describes what git actually writes.
func TestIsBareRepoConUnRepoRealDeVerdad(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available: the heuristic would be tested against hand-made files, not a real repo")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "repo.git")
	gitHere(t, base, "init", "--bare", "-q", dir)

	if !IsBareRepo(dir) {
		t.Error("a real `git init --bare` is not recognized as a bare repo: the heuristic does not describe what git writes")
	}

	normal := filepath.Join(base, "normal")
	gitHere(t, base, "init", "-q", "-b", "main", normal)
	if IsBareRepo(normal) {
		t.Error("a normal `git init` is being taken for a bare repo")
	}
}

func write(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func gitHere(t *testing.T, dir string, args ...string) {
	t.Helper()
	base := []string{"-c", "user.email=test@test", "-c", "user.name=test", "-c", "protocol.file.allow=always"}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
