package orchestrate

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// Not a test: this is the portless service (UDP only), re-executed as the command of a real launch.
func TestHelperNoPort(t *testing.T) {
	if os.Getenv("VROOM_NOPORT_HELPER") != "1" {
		t.Skip("proceso helper, no un test")
	}
	port, err := strconv.Atoi(os.Getenv("VROOM_NOPORT_PORT"))
	if err != nil {
		os.Exit(2)
	}
	// A UDP socket never shows up as a TCP listener, which is exactly the "alive with no TCP port" case.
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

	stack := &Stack{
		Name: "s",
		Stages: []Stage{{
			Name:     "stage1",
			Services: []string{"api", "worker"},
			Timeout:  6 * time.Second,
		}},
	}
	// The cleanup reuses the same stack: StopStack only walks services listed in some stage, so a stack with no stages stops nothing.
	t.Cleanup(func() { _ = engine.StopStack(stack, projects) })

	result, err := engine.Launch(stack, projects)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	if !result.OK {
		t.Fatalf("un servicio sin puerto no debe fallar la launch: %s", result.Error)
	}
	if result.Error != "" {
		t.Errorf("result.Error = %q, want vacío", result.Error)
	}

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

	stack := &Stack{Name: "s", Stages: []Stage{{
		Name: "stage1", Services: []string{"api", "worker"}, Timeout: 6 * time.Second,
	}}}
	t.Cleanup(func() { _ = engine.StopStack(stack, projects) })
	if res, err := engine.Launch(stack, projects); err != nil || !res.OK {
		t.Fatalf("primera launch: %v %s", err, res.Error)
	}

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

// The guard was not softened, only the two cases separated: a pending port still fails the stage.
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
	// The process is alive, the port is reserved and nobody has bound yet.
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

// M-A: an unresolved port is a third non-fatal result; it used to fall into the generic Port <= 0 -> ErrPortPending, a hard error whose stageErr and abortAndCleanup killed the healthy sibling, so the assertion is behavioural: the launch does not fail and the sibling is still alive with its CreationTimeMs intact.
func TestLaunchPortUnresolvedDoesNotAbortStack(t *testing.T) {
	if testing.Short() {
		t.Skip("integración: spawn real")
	}

	// The unresolved service is seeded as running with its discovery already finished, which is what the state means; provoking a slow discovery would mean waiting out the grace window.
	dir := t.TempDir()
	live := startHelperProcess(t, "sleep 120")

	slow := scanner.Project{
		Path: dir, Name: "slow", Configured: true,
		Manifest: &manifest.Manifest{
			Name: "slow", Command: "sleep 120",
			Port: 8080, PortMode: manifest.PortModeDynamic,
		},
	}

	// The sibling is started by THIS launch, so it lands in startedThisSession and is exactly what abortAndCleanup would kill.
	sibling := scanner.Project{
		Path: t.TempDir(), Name: "api", Configured: true,
		Manifest: &manifest.Manifest{
			Name: "api", Command: "sleep 120", Port: 0, PortMode: manifest.PortModeNone,
		},
	}
	projects := []scanner.Project{sibling, slow}

	store := state.NewStoreAt(t.TempDir())
	engine := NewEngine(process.NewManager(), store)
	stack := &Stack{
		Name:   "s",
		Stages: []Stage{{Name: "stage1", Services: []string{"api", "slow"}, Timeout: 2 * time.Second}},
	}
	t.Cleanup(func() {
		_ = engine.StopStack(stack, projects)
		_ = syscall.Kill(-live.Pgid, syscall.SIGKILL)
	})

	if _, err := store.EnsureServiceDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMeta(dir, state.Meta{
		Name: "slow", ProjectPath: dir, Port: 0,
		Pid: live.Pid, Pgid: live.Pgid, CreationTimeMs: live.CreationTimeMs,
		State: state.StatePortUnresolved,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := engine.Launch(stack, projects)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	if !result.OK {
		t.Fatalf("un puerto sin resolver no debe fallar la launch: %s", result.Error)
	}

	byName := map[string]ServiceResult{}
	for _, sr := range result.Stages[0].Services {
		byName[sr.Name] = sr
	}
	slowRes := byName["slow"]

	if !slowRes.PortUnresolved {
		t.Errorf("el servicio con el puerto sin decidir debe marcarse PortUnresolved: %+v", slowRes)
	}
	if slowRes.NoPort {
		t.Error("puerto sin resolver no es lo mismo que no tener puerto TCP")
	}
	if slowRes.Error != "" {
		t.Errorf("un puerto sin resolver no es un fallo: %q", slowRes.Error)
	}
	if byName["api"].PortUnresolved || byName["api"].NoPort {
		t.Errorf("el hermano sano no debe llevar ninguna marca de puerto: %+v", byName["api"])
	}

	// "pending" would send the user waiting for a discovery that already finished.
	if strings.Contains(result.Error, "pending") || strings.Contains(slowRes.Error, "pending") {
		t.Errorf("un puerto sin resolver no puede describirse como pending: %q / %q", result.Error, slowRes.Error)
	}

	apiMeta, err := store.LoadMeta(sibling.Path)
	if err != nil {
		t.Fatalf("meta del hermano: %v", err)
	}
	if apiMeta.Pid == 0 {
		t.Fatal("el hermano quedó sin PID: el stack se abortó")
	}
	if !process.Alive(apiMeta.Pid, apiMeta.CreationTimeMs) {
		t.Errorf("el hermano %d está muerto: abortAndCleanup lo apagó", apiMeta.Pid)
	}

	if !process.Alive(live.Pid, live.CreationTimeMs) {
		t.Errorf("el servicio %d debe seguir vivo y ser detenible", live.Pid)
	}
}

func startHelperProcess(t *testing.T, cmd string) process.StartResult {
	t.Helper()
	dir := t.TempDir()
	res, err := process.NewManager().Start(process.StartSpec{
		Command:    cmd,
		WorkDir:    dir,
		StdoutPath: dir + "/out.log",
		StderrPath: dir + "/err.log",
	})
	if err != nil {
		t.Fatalf("helper: %v", err)
	}
	return res
}

func TestAwaitPortUnresolvedIsNotPending(t *testing.T) {
	err := AwaitPort(PortWait{
		Mode:       manifest.PortModeDynamic,
		Unresolved: true,
	}, time.Second)

	if !errors.Is(err, ErrPortUnresolved) {
		t.Fatalf("AwaitPort = %v, want ErrPortUnresolved", err)
	}
	if errors.Is(err, ErrPortPending) {
		t.Error("un puerto sin resolver no puede confundirse con uno pendiente")
	}
	if !strings.Contains(err.Error(), "unresolved") {
		t.Errorf("el mensaje debe nombrarse unresolved, no pending: %q", err.Error())
	}
}
