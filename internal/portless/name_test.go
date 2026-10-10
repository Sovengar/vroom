package portless

import (
	"os"
	"path/filepath"
	"testing"
	"vroom/internal/manifest"
)

// MEASURED: `portless alias` rejects underscores, spaces, colons and accents and silently truncates at a slash, so a git branch would register the wrong name or collide with the twin worktree.
func TestHostnameNormalizesLikePortless(t *testing.T) {
	cases := []struct{ in, want string }{
		{"miapp", "miapp.localhost"},
		{"miapp.localhost", "miapp.localhost"},
		{"MiApp", "miapp.localhost"},
		{"mi_app", "mi-app.localhost"},
		{"feat/mi_app", "feat-mi-app.localhost"},
		{"a..b", "a.b.localhost"},
		{"-lead", "lead.localhost"},
		{"  spaced  ", "spaced.localhost"},
		{"a b", "a-b.localhost"},
		{"", ""},
		{"...", ""},
	}
	for _, tc := range cases {
		if got := Hostname(tc.in); got != tc.want {
			t.Errorf("Hostname(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The costliest case: Feat/My_Branch.proj registered as feat.localhost, another project's name, silently.
func TestHostnameDoesNotTruncateAtSlash(t *testing.T) {
	if got := Hostname("feat/my_branch.proj"); got != "feat-my-branch.proj.localhost" {
		t.Errorf("a name with a slash must keep it as a hyphen, not truncate: %q", got)
	}
}

func TestDeriveName(t *testing.T) {
	t.Run("auto uses branch and project", func(t *testing.T) {
		got, err := DeriveName(manifest.URLGenByWorkspaceHostname, "", "feat/mi_app", "api")
		if err != nil {
			t.Fatal(err)
		}
		if got != "feat-mi-app.api" {
			t.Errorf("auto must derive <branch>.<project>, got %q", got)
		}
	})

	t.Run("auto without branch falls back to project", func(t *testing.T) {
		got, err := DeriveName(manifest.URLGenByWorkspaceHostname, "", "", "api")
		if err != nil {
			t.Fatal(err)
		}
		if got != "api" {
			t.Errorf("without branch the project is preferred, got %q", got)
		}
	})

	t.Run("named uses route_name and sanitizes", func(t *testing.T) {
		got, err := DeriveName(manifest.URLGenByHostnameOrWorkspace, "My_OAuth_Callback", "feat/x", "api")
		if err != nil {
			t.Fatal(err)
		}
		if got != "my-oauth-callback" {
			t.Errorf("named must use sanitized route_name, got %q", got)
		}
	})

	t.Run("named with unusable name is error", func(t *testing.T) {
		if _, err := DeriveName(manifest.URLGenByHostnameOrWorkspace, "///", "", "api"); err == nil {
			t.Error("an unusable route_name must be rejected, not degenerate into an empty name")
		}
	})

	t.Run("unknown mode is error", func(t *testing.T) {
		if _, err := DeriveName("wat", "", "", "api"); err == nil {
			t.Error("an unknown route_mode must be rejected")
		}
	})
}

func TestClientForSoloResuelveGeneracionesQuePublican(t *testing.T) {
	for _, gen := range []string{"", manifest.URLGenByPort, manifest.URLGenNone, "inventado"} {
		if ClientFor(gen) != nil {
			t.Errorf("ClientFor(%q) must be nil (no portless resolution)", gen)
		}
	}
	for _, gen := range []string{
		manifest.URLGenByHostname,
		manifest.URLGenByWorkspaceHostname,
		manifest.URLGenByHostnameOrWorkspace,
	} {
		c := ClientFor(gen)
		if c == nil {
			t.Errorf("ClientFor(%q) must return a client", gen)
			continue
		}
		// A test binary degrades to a bin-less client, never the real portless.
		if IsTestBinary() && c.HasBinary() {
			t.Errorf("ClientFor(%q) in a test binary must not resolve a real binary", gen)
		}
	}
}

// MEASURED: the CLI honours PORTLESS_STATE_DIR and ignores PORTLESS_HOME and XDG_STATE_HOME, so the plan's order read proxy.port from one directory while the binary wrote routes.json in another.
func TestResolveStateDirOrder(t *testing.T) {
	t.Run("PORTLESS_STATE_DIR wins", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "/iso/state")
		t.Setenv("XDG_STATE_HOME", "/xdg")
		if got := ResolveStateDir(); got != "/iso/state" {
			t.Errorf("PORTLESS_STATE_DIR must win, got %q", got)
		}
	})

	t.Run("PORTLESS_HOME does NOT decide", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "/iso/state")
		t.Setenv("PORTLESS_HOME", "/otra/cosa")
		if got := ResolveStateDir(); got != "/iso/state" {
			t.Errorf("PORTLESS_HOME must not decide the state, got %q", got)
		}
	})

	t.Run("XDG_STATE_HOME does NOT decide", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "/xdg")
		t.Setenv("HOME", "/home/alguien")
		if got := ResolveStateDir(); got != filepath.Join("/home/alguien", ".portless") {
			t.Errorf("XDG_STATE_HOME must not decide: portless ignores it, got %q", got)
		}
	})

	t.Run("HOME/.portless as last resort", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "/home/alguien")
		if got := ResolveStateDir(); got != filepath.Join("/home/alguien", ".portless") {
			t.Errorf("HOME must be the last resort, got %q", got)
		}
	})
}

func TestResolveBinaryFallsBackToMiseShims(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PORTLESS_BIN", "")
	t.Setenv("PATH", t.TempDir())

	shim := filepath.Join(home, ".local", "share", "mise", "shims")
	if err := os.MkdirAll(shim, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(shim, "portless")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := ResolveBinary(); got != want {
		t.Errorf("with empty PATH it must resolve via mise shims, got %q want %q", got, want)
	}
}

func TestResolveBinaryPrefersExplicitEnv(t *testing.T) {
	t.Setenv("PORTLESS_BIN", "/opt/portless/bin/portless")
	if got := ResolveBinary(); got != "/opt/portless/bin/portless" {
		t.Errorf("PORTLESS_BIN must win, got %q", got)
	}
}

// MEASURED (M6): the absence of proxy.port IS the signal, and 1355 is never assumed, not even when the state dir does not resolve.
func TestProxyPortRequiresTheFile(t *testing.T) {
	c := New(WithBinary("/fake/portless"), WithStateDir(t.TempDir()))
	if _, err := c.ProxyPort(); err == nil {
		t.Error("without proxy.port there can be no proxy port")
	}

	empty := New(WithBinary("/fake/portless"), WithStateDir(""))
	if _, err := empty.ProxyPort(); err == nil {
		t.Error("without state dir there can be no proxy port")
	}
}

// MEASURED: 4 bytes with no trailing newline, accepted with or without.
func TestProxyPortParsesBareNumber(t *testing.T) {
	for _, content := range []string{"1399", "1399\n", " 1399 "} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "proxy.port"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		c := New(WithBinary("/fake/portless"), WithStateDir(dir))
		got, err := c.ProxyPort()
		if err != nil || got != 1399 {
			t.Errorf("proxy.port %q must give 1399, got %d err=%v", content, got, err)
		}
	}
}
