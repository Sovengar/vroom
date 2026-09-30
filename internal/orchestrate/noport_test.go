package orchestrate

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// ---- Escenario: un servicio vivo sin puerto TCP NO tumba el stack ----

// TestHelperNoPort no es un test: es el servicio sin puerto TCP (solo UDP).
func TestHelperNoPort(t *testing.T) {
	if os.Getenv("VROOM_NOPORT_HELPER") != "1" {
		t.Skip("proceso helper, no un test")
	}
	port, err := strconv.Atoi(os.Getenv("VROOM_NOPORT_PORT"))
	if err != nil {
		os.Exit(2)
	}
	// Un socket UDP no aparece como listener TCP: es exactamente el caso
	// "vive pero no tiene puerto TCP".
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		os.Exit(3)
	}
	defer func() { _ = conn.Close() }()

	go func() {
		buf := make([]byte, 64)
		for {
			if _, _, err := conn.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	time.Sleep(60 * time.Second)
}

func noportFixture(t *testing.T) (scanner.Project, string) {
	t.Helper()
	dir := t.TempDir()
	port, err := freeUDPPort(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VROOM_NOPORT_HELPER", "1")
	t.Setenv("VROOM_NOPORT_PORT", strconv.Itoa(port))
	return scanner.Project{
		Path: dir, Name: "worker", Configured: true,
		Manifest: &manifest.Manifest{
			Name:     "worker",
			Command:  shellQuote(os.Args[0]) + " -test.run=^TestHelperNoPort$",
			Port:     8080,
			PortMode: manifest.PortModeDynamic,
		},
	}, dir
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func freeUDPPort(t *testing.T) (int, error) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return 0, err
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	_ = conn.Close()
	return port, nil
}

// Un servicio vivo sin puerto TCP se reporta como tal, no como fallo de
// salud: la launch termina bien y sus hermanos siguen vivos.
func TestLaunchNoPortServiceDoesNotAbortStack(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}

	worker, workerDir := noportFixture(t)
	sibling := scanner.Project{
		Path: t.TempDir(), Name: "api", Configured: true,
		Manifest: &manifest.Manifest{
			Name: "api", Command: "sleep 120", Port: 0, PortMode: manifest.PortModeNone,
		},
	}
	projects := []scanner.Project{sibling, worker}

	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(process.NewManager(), store)
	t.Cleanup(func() { _ = engine.StopStack(&Stack{Name: "s"}, projects) })

	stack := &Stack{
		Name: "s",
		Stages: []Stage{{
			Name:     "stage1",
			Services: []string{"api", "worker"},
			Timeout:  6 * time.Second,
		}},
	}

	result, err := engine.Launch(stack, projects)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// (a) La launch NO aborta.
	if !result.OK {
		t.Fatalf("un servicio sin puerto no debe fallar la launch: %s", result.Error)
	}
	if result.Error != "" {
		t.Errorf("result.Error = %q, want vacío", result.Error)
	}

	// (c) Se reporta como tal, no como fallo de health check.
	byName := map[string]ServiceResult{}
	for _, sr := range result.Stages[0].Services {
		byName[sr.Name] = sr
	}
	w, ok := byName["worker"]
	if !ok {
		t.Fatalf("el servicio sin puerto no aparece en el resultado: %+v", result.Stages[0].Services)
	}
	if w.Error != "" {
		t.Errorf("el servicio sin puerto se reportó como error: %q", w.Error)
	}
	if !w.NoPort {
		t.Error("el servicio sin puerto debe marcarse NoPort, no como health check failed")
	}
	if w.Action != "started" {
		t.Errorf("action = %q, want started", w.Action)
	}
	api := byName["api"]
	if api.NoPort {
		t.Error("el hermano con puerto no debe marcarse NoPort")
	}

	// (b) Los hermanos siguen vivos: abortAndCleanup los habría parado.
	apiMeta, err := store.LoadMeta(sibling.Path)
	if err != nil {
		t.Fatalf("meta del hermano: %v", err)
	}
	if apiMeta.Pid == 0 {
		t.Fatal("el hermano quedó sin PID: el stack se abortó")
	}
	if !process.Alive(apiMeta.Pid, apiMeta.CreationTimeMs) {
		t.Errorf("el hermano %d está muerto: el stack se abortó", apiMeta.Pid)
	}

	workerMeta, err := store.LoadMeta(workerDir)
	if err != nil {
		t.Fatalf("meta del servicio sin puerto: %v", err)
	}
	if !process.Alive(workerMeta.Pid, workerMeta.CreationTimeMs) {
		t.Errorf("el servicio sin puerto %d debe seguir vivo y ser operable", workerMeta.Pid)
	}
	if workerMeta.State != state.StateNoPort {
		t.Errorf("meta.State = %q, want no_port", workerMeta.State)
	}
	if workerMeta.Port != 0 {
		t.Errorf("meta.Port = %d, want 0 (no expone puerto TCP)", workerMeta.Port)
	}
}

// Un servicio ya arrancado sin puerto tampoco puede tumbar la launch en el
// camino "already_running".
func TestLaunchAlreadyRunningNoPortServiceDoesNotAbort(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}

	worker, workerDir := noportFixture(t)
	sibling := scanner.Project{
		Path: t.TempDir(), Name: "api", Configured: true,
		Manifest: &manifest.Manifest{
			Name: "api", Command: "sleep 120", Port: 0, PortMode: manifest.PortModeNone,
		},
	}
	projects := []scanner.Project{sibling, worker}
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(process.NewManager(), store)
	t.Cleanup(func() { _ = engine.StopStack(&Stack{Name: "s"}, projects) })

	// Primera launch: deja ambos vivos.
	stack := &Stack{Name: "s", Stages: []Stage{{
		Name: "stage1", Services: []string{"api", "worker"}, Timeout: 6 * time.Second,
	}}}
	if res, err := engine.Launch(stack, projects); err != nil || !res.OK {
		t.Fatalf("primera launch: %v %s", err, res.Error)
	}

	// Segunda launch: el worker ya está running y en no_port.
	res, err := engine.Launch(stack, projects)
	if err != nil {
		t.Fatalf("segunda launch: %v", err)
	}
	if !res.OK {
		t.Fatalf("el camino already_running no debe abortar: %s", res.Error)
	}

	byName := map[string]ServiceResult{}
	for _, sr := range res.Stages[0].Services {
		byName[sr.Name] = sr
	}
	if w := byName["worker"]; w.Action != "already_running" || !w.NoPort || w.Error != "" {
		t.Errorf("worker = %+v, want already_running sin error y NoPort", w)
	}

	workerMeta, _ := store.LoadMeta(workerDir)
	if !process.Alive(workerMeta.Pid, workerMeta.CreationTimeMs) {
		t.Error("el servicio sin puerto debe seguir vivo")
	}
	siblingMeta, _ := store.LoadMeta(sibling.Path)
	if !process.Alive(siblingMeta.Pid, siblingMeta.CreationTimeMs) {
		t.Error("el hermano debe seguir vivo: la segunda launch abortó")
	}
}

// El caso contrario sigue siendo un fallo: un puerto pendiente SÍ impide dar
// la etapa por buena. El guard no se ha ablandado, sólo se ha separado el
// "sin puerto" del "puerto sin decidir".
func TestLaunchPortPendingServiceStillFails(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(&mockManager{
		evalFunc: func(process.EvalSpec) process.Status { return process.StatusRunning },
	}, store)

	p := scanner.Project{
		Path: t.TempDir(), Name: "api", Configured: true,
		Manifest: &manifest.Manifest{
			Name: "api", Command: "sleep 120",
			Port: 8080, PortMode: manifest.PortModeDynamic,
		},
	}
	// El proceso vive, el puerto está reservado y aún nadie ha hecho bind.
	if _, err := store.EnsureServiceDir(p.Path); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(p.Path, state.Meta{
		Name: "api", ProjectPath: p.Path,
		Port: closedTCPPort(t), Pid: 9999, Pgid: 9999,
		CreationTimeMs: 100, State: state.StatePortPending,
	}); err != nil {
		t.Fatal(err)
	}

	stack := &Stack{Name: "s", Stages: []Stage{{
		Name: "stage1", Services: []string{"api"}, Timeout: 300 * time.Millisecond,
	}}}

	res, err := engine.Launch(stack, []scanner.Project{p})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if res.OK {
		t.Fatal("un puerto pendiente debe fallar la etapa: no se puede dar por buena")
	}
	if !strings.Contains(res.Error, "pending") {
		t.Errorf("la causa debe nombrarse como pendiente, no como timeout genérico: %q", res.Error)
	}
	if strings.Contains(res.Error, "no TCP port") {
		t.Errorf("un puerto pendiente no es lo mismo que no tener puerto: %q", res.Error)
	}
}

// closedTCPPort devuelve un puerto que nadie escucha.
func closedTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
