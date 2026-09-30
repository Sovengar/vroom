package portless

import (
	"os"
	"path/filepath"
	"testing"
)

// MEDIDO: `portless alias` rechaza con exit 1 cualquier hostname con guion
// bajo, espacio, dos puntos o acentos, y TRUNCA en silencio un nombre con
// barra. Una rama de git está llena de guiones bajos y barras, así que sin
// sanear vroom registraría el nombre equivocado —o chocaría con el worktree
// vecino— sin decir nada. Estos tests son el contrato de ese saneo.
func TestHostnameNormalizesLikePortless(t *testing.T) {
	cases := []struct{ in, want string }{
		{"miapp", "miapp.localhost"},
		{"miapp.localhost", "miapp.localhost"}, // no se duplica el TLD
		{"MiApp", "miapp.localhost"},           // portless normaliza a minúsculas
		{"mi_app", "mi-app.localhost"},         // guion bajo -> guion
		{"feat/mi_app", "feat-mi-app.localhost"},
		{"a..b", "a.b.localhost"}, // puntos consecutivos rechazados por portless
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

// El caso que más daño haría si no se saneara: la truncación en la barra.
// `Feat/My_Branch.proj` se registró como `feat.localhost` — el nombre de otro
// proyecto, en silencio.
func TestHostnameDoesNotTruncateAtSlash(t *testing.T) {
	if got := Hostname("feat/my_branch.proj"); got != "feat-my-branch.proj.localhost" {
		t.Errorf("un nombre con barra debe conservarla como guion, no truncarse: %q", got)
	}
}

func TestDeriveName(t *testing.T) {
	t.Run("auto usa rama y proyecto", func(t *testing.T) {
		got, err := DeriveName(RouteModeAuto, "", "feat/mi_app", "api")
		if err != nil {
			t.Fatal(err)
		}
		if got != "feat-mi-app.api" {
			t.Errorf("auto debe derivar <rama>.<proyecto>, got %q", got)
		}
	})

	t.Run("auto sin rama cae al proyecto", func(t *testing.T) {
		got, err := DeriveName(RouteModeAuto, "", "", "api")
		if err != nil {
			t.Fatal(err)
		}
		if got != "api" {
			t.Errorf("sin rama se prefiere el proyecto, got %q", got)
		}
	})

	t.Run("named usa route_name y sanea", func(t *testing.T) {
		got, err := DeriveName(RouteModeNamed, "My_OAuth_Callback", "feat/x", "api")
		if err != nil {
			t.Fatal(err)
		}
		if got != "my-oauth-callback" {
			t.Errorf("named debe usar route_name saneado, got %q", got)
		}
	})

	t.Run("named con nombre inservible es error", func(t *testing.T) {
		if _, err := DeriveName(RouteModeNamed, "///", "", "api"); err == nil {
			t.Error("un route_name inservible debe rechazarse, no degenerar en un nombre vacío")
		}
	})

	t.Run("modo desconocido es error", func(t *testing.T) {
		if _, err := DeriveName("wat", "", "", "api"); err == nil {
			t.Error("un route_mode desconocido debe rechazarse")
		}
	})
}

func TestRouteModeEnabled(t *testing.T) {
	// off (y su forma ausente) NO buscan el binario: es la puerta de
	// compatibilidad hacia atrás.
	for _, m := range []string{RouteModeOff, ""} {
		if RouteModeEnabled(m) {
			t.Errorf("el modo %q no debe buscar portless", m)
		}
	}
	for _, m := range []string{RouteModeAuto, RouteModeNamed} {
		if !RouteModeEnabled(m) {
			t.Errorf("el modo %q sí debe trabajar con portless", m)
		}
	}
}

// El state dir se deriva del entorno, nunca de un path de usuario escrito a
// mano: vroom corre bajo un gestor de servicios cuyo entorno no es el shell de
// login. Un path que funciona en la terminal y falla en el daemon es un bug.
//
// Y el orden NO es el que decía el plan: MEDIDO, el CLI honra
// PORTLESS_STATE_DIR e IGNORA PORTLESS_HOME. Con el orden del plan, vroom leía
// proxy.port de un directorio y el binario escribía routes.json en otro, así que
// una ruta se registraba y luego no se podía quitar — un fallo silencioso que
// este test es lo que vuelve imposible reintroducir.
func TestResolveStateDirOrder(t *testing.T) {
	t.Run("PORTLESS_STATE_DIR gana", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "/iso/state")
		t.Setenv("XDG_STATE_HOME", "/xdg")
		if got := ResolveStateDir(); got != "/iso/state" {
			t.Errorf("PORTLESS_STATE_DIR debe ganar, got %q", got)
		}
	})

	t.Run("PORTLESS_HOME NO decide", func(t *testing.T) {
		// Es el override que el CLI NO honra. Que vroom lo consultara es
		// exactamente el bug anterior.
		t.Setenv("PORTLESS_STATE_DIR", "/iso/state")
		t.Setenv("PORTLESS_HOME", "/otra/cosa")
		if got := ResolveStateDir(); got != "/iso/state" {
			t.Errorf("PORTLESS_HOME no debe decidir el estado, got %q", got)
		}
	})

	t.Run("XDG_STATE_HOME si no hay PORTLESS_STATE_DIR", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "/xdg")
		if got := ResolveStateDir(); got != filepath.Join("/xdg", "portless") {
			t.Errorf("XDG_STATE_HOME debe ser el segundo, got %q", got)
		}
	})

	t.Run("HOME/.portless como último recurso", func(t *testing.T) {
		t.Setenv("PORTLESS_STATE_DIR", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "/home/alguien")
		if got := ResolveStateDir(); got != filepath.Join("/home/alguien", ".portless") {
			t.Errorf("HOME debe ser el último recurso, got %q", got)
		}
	})
}

// MEDIDO: `env -i PATH=/usr/bin:/bin` NO resuelve portless, porque vive tras
// los shims de mise. Por eso la resolución tiene un tercer paso.
func TestResolveBinaryFallsBackToMiseShims(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PORTLESS_BIN", "")
	t.Setenv("PATH", t.TempDir()) // PATH vacío: LookPath NO puede resolver

	shim := filepath.Join(home, ".local", "share", "mise", "shims")
	if err := os.MkdirAll(shim, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(shim, "portless")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := ResolveBinary(); got != want {
		t.Errorf("con PATH vacío debe resolverse por los shims de mise, got %q want %q", got, want)
	}
}

func TestResolveBinaryPrefersExplicitEnv(t *testing.T) {
	t.Setenv("PORTLESS_BIN", "/opt/portless/bin/portless")
	if got := ResolveBinary(); got != "/opt/portless/bin/portless" {
		t.Errorf("PORTLESS_BIN debe ganar, got %q", got)
	}
}

// Sin proxy.port no hay puerto: la ausencia ES la señal (M6). Nunca se cae a
// un 1355 supuesto, ni siquiera cuando el state dir no resuelve.
func TestProxyPortRequiresTheFile(t *testing.T) {
	c := New(WithBinary("/fake/portless"), WithStateDir(t.TempDir()))
	if _, err := c.ProxyPort(); err == nil {
		t.Error("sin proxy.port no puede haber puerto de proxy")
	}

	empty := New(WithBinary("/fake/portless"), WithStateDir(""))
	if _, err := empty.ProxyPort(); err == nil {
		t.Error("sin state dir no puede haber puerto de proxy")
	}
}

// proxy.port medido son 4 bytes sin salto de línea; se acepta con y sin.
func TestProxyPortParsesBareNumber(t *testing.T) {
	for _, content := range []string{"1399", "1399\n", " 1399 "} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "proxy.port"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		c := New(WithBinary("/fake/portless"), WithStateDir(dir))
		got, err := c.ProxyPort()
		if err != nil || got != 1399 {
			t.Errorf("proxy.port %q debe dar 1399, got %d err=%v", content, got, err)
		}
	}
}
