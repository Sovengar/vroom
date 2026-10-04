package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The JSON result of `start` answers an agent, while the warnings are what a human reads later in the service log; a service that started with an unverified port looks fine if they never land there.

// MEDIDO: a dynamic-mode service that opens no TCP port is the only honest way in, because fixed mode has nothing to verify and the no-port path is the shortest of the three (no discovery deadline, no sockets).
func TestStartEscribeLosAvisosEnElLogDelServicio(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	// `port` is declared on purpose because Validate demands it in dynamic mode: it is the app fallback (PORT=${PORT:-N}), not the port vroom injects.
	worker := filepath.Join(root, "worker")
	writeFile(t, filepath.Join(worker, ".vroom.toml"), `name = "worker"
command_start = "sleep 30"
port_mode = "dynamic"
port = 8080
`)

	payload, err := cmdStart("worker", "")
	if err != nil {
		t.Fatalf("cmdStart: %v", err)
	}
	res := mustAction(t, payload, nil)
	if !res.OK {
		t.Fatalf("start = %+v, want OK: un worker sin puerto es un estado, no un fallo de arranque", res)
	}
	if res.Pid <= 0 {
		t.Fatalf("Pid = %d, want > 0", res.Pid)
	}
	t.Cleanup(func() { _, _ = cmdStop("worker", "") })

	log, err := os.ReadFile(store.StderrLog(worker))
	if err != nil {
		t.Fatalf("no hay log de stderr del servicio: %v", err)
	}
	aviso := string(log)
	if !strings.Contains(aviso, "vroom ▶ start:") {
		t.Fatalf("el log de stderr = %q, want una línea de aviso de arranque: los avisos que no "+
			"se escriben en ningún sitio no son avisos", aviso)
	}
	if !strings.Contains(aviso, "no TCP port") {
		t.Errorf("el aviso = %q, want que explique que el servicio no abrió puerto: el usuario "+
			"tiene que entender por qué la tab Health no sondea nada", aviso)
	}
	// It must not leak into stdout: stdout is the JSON reply and a warning there breaks any agent parsing it.
	if strings.Contains(res.Action, "no TCP port") {
		t.Errorf("action = %q: el motivo del aviso se ha colado en la respuesta en vez de quedarse "+
			"en el log", res.Action)
	}
}

// One-shots write to the service log, so if that log cannot be opened the command does not launch: refusing beats running a two-minute build whose output goes nowhere, and the OpenFile error is returned unwrapped because whoever reads it needs the errno (permissions or disk, nothing vroom can fix).
func TestRunLoggedFallaSiElLogNoSePuedeAbrir(t *testing.T) {
	dir := t.TempDir()
	roto := filepath.Join(dir, "logs")
	if err := os.MkdirAll(roto, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roto, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roto, 0o755) })

	_, code, err := runLogged("build", "echo hola", dir, filepath.Join(roto, "out.log"), "")
	if err == nil {
		t.Fatal("con el log sin permiso de escritura el comando no se puede lanzar")
	}
	if code != 0 {
		t.Errorf("exit code = %d con un fallo de lanzamiento, want 0: no llegó a ejecutarse nada", code)
	}
}

// MEDIDO: /dev/full is the only way to get a log that opens fine and then rejects writes with ENOSPC, and a banner that does not fit means the build output will not fit either, so the verdict is the same: do not launch.
func TestRunLoggedFallaSiElBannerNoSePuedeEscribir(t *testing.T) {
	const lleno = "/dev/full"
	if _, err := os.Stat(lleno); err != nil {
		t.Skipf("esta máquina no tiene %s, y sin él no hay forma de abrir un log que acepte el "+
			"OpenFile y rechace la escritura", lleno)
	}

	_, code, err := runLogged("build", "echo hola", t.TempDir(), lleno, "")
	if err == nil {
		t.Fatal("con un log que no acepta escrituras el comando no se puede lanzar")
	}
	if code != 0 {
		t.Errorf("exit code = %d con un fallo de escritura, want 0: no llegó a ejecutarse nada", code)
	}
}
