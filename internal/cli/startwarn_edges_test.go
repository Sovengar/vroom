package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Lo que el arranque le dice al usuario por el log, y no por la respuesta.
//
// El resultado JSON de `start` es la respuesta para un agente. Los avisos son otra
// cosa: son lo que un humano lee después, abriendo el log del servicio. Si ahí no
// llegan, un servicio que arrancó pero con el puerto sin verificar parece un
// servicio que arrancó bien.
// ---------------------------------------------------------------------------

// TestStartEscribeLosAvisosEnElLogDelServicio: el `Warnings` que no se pierden.
//
// El caso que se provoca es un servicio en `port_mode = "dynamic"` que NO abre
// ningún puerto TCP —un worker, una app sin servidor—. El arranque va bien, pero
// el puerto queda sin resolver, y eso tiene que aparecer en el log de stderr del
// servicio con la línea `vroom ▶ start:`.
//
// MEDIDO: es la única forma honesta de llegar aquí. Los avisos sólo se emiten en
// modo dynamic, porque en modo fixed el puerto es el declarado y no hay nada que
// verificar; y el camino de "sin puerto" es el más corto de los tres —no hay que
// esperar al plazo del discovery ni abrir sockets—.
func TestStartEscribeLosAvisosEnElLogDelServicio(t *testing.T) {
	root := cliEnv(t)
	store := chdirTree(t, root)

	// Un worker: vive, no escucha. En dynamic, el discovery acaba sin puertos.
	//
	// `port` declarado a propósito porque `Validate` lo exige en dynamic: es el
	// fallback de la app (`PORT=${PORT:-N}`), no el puerto que vroom inyecta.
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

	// El aviso tiene que estar en el log de stderr del servicio, que es lo único
	// que sobrevive a que la TUI se cierre.
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
	// Y no puede salir por stdout: stdout es la respuesta JSON, y un aviso ahí rompe
	// a cualquier agente que la parsee.
	if strings.Contains(res.Action, "no TCP port") {
		t.Errorf("action = %q: el motivo del aviso se ha colado en la respuesta en vez de quedarse "+
			"en el log", res.Action)
	}
}

// TestRunLoggedFallaSiElLogNoSePuedeAbrir: el primer descriptor.
//
// Los one-shot escriben en el log del servicio. Si ese log no se puede abrir, el
// comando no se lanza: es preferible decir que no se pudo escribir el log a ejecutar
// un build de dos minutos cuyo output no va a ninguna parte.
//
// Y el error tiene que ser el del `OpenFile` sin envolver, porque quien lo lee
// necesita el errno: esto pasa por permisos o por disco, no por nada que vroom pueda
// arreglar.
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

// TestRunLoggedFallaSiElBannerNoSePuedeEscribir: el mismo descriptor, sin espacio.
//
// El segundo fallo posible es el que sólo aparece con el fichero YA abierto: se
// abre bien y no se puede escribir en él. Un banner que no cabe significa que
// tampoco cabrá la salida del build, así que es el mismo veredicto: no se lanza.
//
// MEDIDO: `/dev/full` es exactamente eso —abre, escribe y devuelve ENOSPC—.
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
