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
		t.Skip("helper process, not a test")
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
			Name:          "worker",
			Command:       shellQuote(os.Args[0]) + " -test.run=^TestHelperNoPort$",
			Port:          8080,
			URLGeneration: manifest.URLGenByWorkspaceHostname,
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
		t.Skip("integration: real spawn")
	}

	worker, workerDir := noportFixture(t)
	sibling := scanner.Project{
		Path: t.TempDir(), Name: "api", Configured: true,
		Manifest: &manifest.Manifest{
			Name: "api", Command: "sleep 120", Port: 0, URLGeneration: manifest.URLGenNone,
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
		t.Fatalf("a portless service must not fail the launch: %s", result.Error)
	}
	if result.Error != "" {
		t.Errorf("result.Error = %q, want empty", result.Error)
	}

	byName := map[string]ServiceResult{}
	for _, sr := range result.Stages[0].Services {
		byName[sr.Name] = sr
	}
	w, ok := byName["worker"]
	if !ok {
		t.Fatalf("the portless service is missing from the result: %+v", result.Stages[0].Services)
	}
	if w.Error != "" {
		t.Errorf("the portless service was reported as error: %q", w.Error)
	}
	if !w.NoPort {
		t.Error("the portless service must be marked NoPort, not as health check failed")
	}
	if w.Action != "started" {
		t.Errorf("action = %q, want started", w.Action)
	}
	api := byName["api"]
	if api.NoPort {
		t.Error("the sibling with a port must not be marked NoPort")
	}

	apiMeta, err := store.LoadMeta(sibling.Path)
	if err != nil {
		t.Fatalf("sibling meta: %v", err)
	}
	if apiMeta.Pid == 0 {
		t.Fatal("the sibling lost its PID: the stack was aborted")
	}
	if !process.Alive(apiMeta.Pid, apiMeta.CreationTimeMs) {
		t.Errorf("sibling %d is dead: the stack was aborted", apiMeta.Pid)
	}

	workerMeta, err := store.LoadMeta(workerDir)
	if err != nil {
		t.Fatalf("portless service meta: %v", err)
	}
	if !process.Alive(workerMeta.Pid, workerMeta.CreationTimeMs) {
		t.Errorf("portless service %d must stay alive and be operable", workerMeta.Pid)
	}
	if workerMeta.State != state.StateNoPort {
		t.Errorf("meta.State = %q, want no_port", workerMeta.State)
	}
	if workerMeta.Port != 0 {
		t.Errorf("meta.Port = %d, want 0 (exposes no TCP port)", workerMeta.Port)
	}
}

func TestLaunchAlreadyRunningNoPortServiceDoesNotAbort(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real spawn")
	}

	worker, workerDir := noportFixture(t)
	sibling := scanner.Project{
		Path: t.TempDir(), Name: "api", Configured: true,
		Manifest: &manifest.Manifest{
			Name: "api", Command: "sleep 120", Port: 0, URLGeneration: manifest.URLGenNone,
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
		t.Fatalf("first launch: %v %s", err, res.Error)
	}

	res, err := engine.Launch(stack, projects)
	if err != nil {
		t.Fatalf("second launch: %v", err)
	}
	if !res.OK {
		t.Fatalf("the already_running path must not abort: %s", res.Error)
	}

	byName := map[string]ServiceResult{}
	for _, sr := range res.Stages[0].Services {
		byName[sr.Name] = sr
	}
	if w := byName["worker"]; w.Action != "already_running" || !w.NoPort || w.Error != "" {
		t.Errorf("worker = %+v, want already_running with no error and NoPort", w)
	}

	workerMeta, _ := store.LoadMeta(workerDir)
	if !process.Alive(workerMeta.Pid, workerMeta.CreationTimeMs) {
		t.Error("the portless service must stay alive")
	}
	siblingMeta, _ := store.LoadMeta(sibling.Path)
	if !process.Alive(siblingMeta.Pid, siblingMeta.CreationTimeMs) {
		t.Error("the sibling must stay alive: the second launch aborted")
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
			Port: 8080, URLGeneration: manifest.URLGenByWorkspaceHostname,
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
		t.Fatal("a pending port must fail the stage: it cannot be treated as good")
	}
	if !strings.Contains(res.Error, "pending") {
		t.Errorf("the cause must be named as pending, not as a generic timeout: %q", res.Error)
	}
	if strings.Contains(res.Error, "no TCP port") {
		t.Errorf("a pending port is not the same as having no port: %q", res.Error)
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
		t.Skip("integration: real spawn")
	}

	// The unresolved service is seeded as running with its discovery already finished, which is what the state means; provoking a slow discovery would mean waiting out the grace window.
	dir := t.TempDir()
	live := startHelperProcess(t, "sleep 120")

	slow := scanner.Project{
		Path: dir, Name: "slow", Configured: true,
		Manifest: &manifest.Manifest{
			Name: "slow", Command: "sleep 120",
			Port: 8080, URLGeneration: manifest.URLGenByWorkspaceHostname,
		},
	}

	// The sibling is started by THIS launch, so it lands in startedThisSession and is exactly what abortAndCleanup would kill.
	sibling := scanner.Project{
		Path: t.TempDir(), Name: "api", Configured: true,
		Manifest: &manifest.Manifest{
			Name: "api", Command: "sleep 120", Port: 0, URLGeneration: manifest.URLGenNone,
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
		t.Fatalf("an unresolved port must not fail the launch: %s", result.Error)
	}

	byName := map[string]ServiceResult{}
	for _, sr := range result.Stages[0].Services {
		byName[sr.Name] = sr
	}
	slowRes := byName["slow"]

	if !slowRes.PortUnresolved {
		t.Errorf("the service with the undecided port must be marked PortUnresolved: %+v", slowRes)
	}
	if slowRes.NoPort {
		t.Error("unresolved port is not the same as having no TCP port")
	}
	if slowRes.Error != "" {
		t.Errorf("an unresolved port is not a failure: %q", slowRes.Error)
	}
	if byName["api"].PortUnresolved || byName["api"].NoPort {
		t.Errorf("the healthy sibling must not carry any port flag: %+v", byName["api"])
	}

	// "pending" would send the user waiting for a discovery that already finished.
	if strings.Contains(result.Error, "pending") || strings.Contains(slowRes.Error, "pending") {
		t.Errorf("an unresolved port cannot be described as pending: %q / %q", result.Error, slowRes.Error)
	}

	apiMeta, err := store.LoadMeta(sibling.Path)
	if err != nil {
		t.Fatalf("sibling meta: %v", err)
	}
	if apiMeta.Pid == 0 {
		t.Fatal("the sibling lost its PID: the stack was aborted")
	}
	if !process.Alive(apiMeta.Pid, apiMeta.CreationTimeMs) {
		t.Errorf("sibling %d is dead: abortAndCleanup shut it down", apiMeta.Pid)
	}

	if !process.Alive(live.Pid, live.CreationTimeMs) {
		t.Errorf("service %d must stay alive and be stoppable", live.Pid)
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
		t.Error("an unresolved port cannot be confused with a pending one")
	}
	if !strings.Contains(err.Error(), "unresolved") {
		t.Errorf("the message must be named unresolved, not pending: %q", err.Error())
	}
}
