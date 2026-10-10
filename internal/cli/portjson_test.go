package cli

import (
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/process"
	"vroom/internal/scanner"
	"vroom/internal/state"
)

// One shared real process: Evaluate needs a genuinely live PID with a matching creation_time to reach the port branches, and a stub would only test the stub.
var (
	liveOnce sync.Once
	liveRes  process.StartResult
)

func liveProcess(t *testing.T) process.StartResult {
	t.Helper()
	liveOnce.Do(func() {
		dir := t.TempDir()
		res, err := process.NewManager().Start(process.StartSpec{
			Command:    "sleep 120",
			WorkDir:    dir,
			StdoutPath: filepath.Join(dir, "out.log"),
			StderrPath: filepath.Join(dir, "err.log"),
		})
		if err != nil {
			t.Fatalf("shared live process: %v", err)
		}
		liveRes = res
	})
	return liveRes
}

// Returns a port that is really accepting, so Evaluate's dial succeeds and the resolved path runs instead of failing closed.
func openPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// These tests assert on the marshalled row, not on helpers: the JSON is the contract agents consume, so reading ProjectInfo fields cannot catch a wrong tag or an omitted field.

func jsonRow(t *testing.T, m *manifest.Manifest, meta state.Meta, live bool) map[string]any {
	t.Helper()
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()

	p := scanner.Project{
		Path: dir, Name: "svc", Configured: true, Manifest: m,
	}
	if live {
		if err := store.SaveMeta(dir, meta); err != nil {
			t.Fatal(err)
		}
	}

	data, err := json.Marshal(buildProjectInfo(process.NewManager(), store, map[string]bool{}, p))
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}
	return row
}

func liveMetaWithState(t *testing.T, s string, port int, verified bool) state.Meta {
	t.Helper()
	res := liveProcess(t)
	return state.Meta{
		Name: "svc", Port: port, PortVerified: verified,
		Pid: res.Pid, Pgid: res.Pgid, CreationTimeMs: res.CreationTimeMs, State: s,
	}
}

func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func dynamicManifest() *manifest.Manifest {
	return &manifest.Manifest{
		Name: "svc", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "run"}}, Port: 8080, URLGeneration: manifest.URLGenByWorkspaceHostname,
	}
}

// An unresolved port must not emit the declared one, or an agent connects to the twin worktree's port.
func TestJSONPortUnresolvedEmitsNoPort(t *testing.T) {
	row := jsonRow(t, dynamicManifest(),
		liveMetaWithState(t, state.StatePortUnresolved, 0, false), true)

	if row["port"] != float64(0) {
		t.Errorf("port = %v, want 0: an unconfirmed port is not emitted", row["port"])
	}
	if row["declared_port"] != float64(8080) {
		t.Errorf("declared_port = %v, want 8080: the declared one is published separately", row["declared_port"])
	}
	verified, present := row["port_verified"]
	if !present {
		t.Fatal("port_verified missing: with omitempty on a bool, false cannot be emitted")
	}
	if verified != false {
		t.Errorf("port_verified = %v, want false", verified)
	}
	if row["status"] != string(process.StatusPortUnresolved) {
		t.Errorf("status = %v, want port_unresolved", row["status"])
	}
	if row["status"] == string(process.StatusRunning) {
		t.Error("an unresolved port cannot be reported as running")
	}
}

func TestJSONResolvedDynamicEmitsRealPort(t *testing.T) {
	port := openPort(t)
	row := jsonRow(t, dynamicManifest(),
		liveMetaWithState(t, state.StateRunning, port, true), true)

	if row["port"] != float64(port) {
		t.Errorf("port = %v, want the real port %d", row["port"], port)
	}
	if row["port_verified"] != true {
		t.Errorf("port_verified = %v, want true", row["port_verified"])
	}
	if row["status"] != string(process.StatusRunning) {
		t.Errorf("status = %v, want running", row["status"])
	}
	if row["declared_port"] != float64(8080) {
		t.Errorf("declared_port = %v, want 8080", row["declared_port"])
	}
}

// The recorded generation (the agent's s choice) outranks the manifest's, so the JSON shows how the service actually starts.
func TestJSONRecordedGenerationOutranksManifest(t *testing.T) {
	port := openPort(t)
	meta := liveMetaWithState(t, state.StateRunning, port, true)
	meta.URLGeneration = manifest.URLGenByWorkspaceHostname

	row := jsonRow(t, &manifest.Manifest{Name: "svc", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "run"}}, Port: 8080, URLGeneration: manifest.URLGenByPort}, meta, true)

	if row["url_generation"] != manifest.URLGenByWorkspaceHostname {
		t.Errorf("url_generation = %v, want the recorded %q", row["url_generation"], manifest.URLGenByWorkspaceHostname)
	}
}

// Without a recorded generation the manifest decides, as before.
func TestJSONGenerationFallsBackToManifest(t *testing.T) {
	port := openPort(t)
	row := jsonRow(t, &manifest.Manifest{Name: "svc", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "run"}}, Port: 8080, URLGeneration: manifest.URLGenByPort},
		liveMetaWithState(t, state.StateRunning, port, true), true)

	if row["url_generation"] != manifest.URLGenByPort {
		t.Errorf("url_generation = %v, want the manifest's %q", row["url_generation"], manifest.URLGenByPort)
	}
}

func TestJSONNoPortIsNotReportedAsRunning(t *testing.T) {
	row := jsonRow(t, dynamicManifest(),
		liveMetaWithState(t, state.StateNoPort, 0, false), true)

	if row["port"] != float64(0) {
		t.Errorf("port = %v, want 0", row["port"])
	}
	if row["status"] != string(process.StatusNoPort) {
		t.Errorf("status = %v, want no_port (cannot claim running with port 0)", row["status"])
	}
	if row["status"] == string(process.StatusPortUnresolved) {
		t.Error("no_port and port_unresolved cannot collapse into the same status")
	}
	if row["port_verified"] != false {
		t.Errorf("port_verified = %v, want false", row["port_verified"])
	}
}

func TestJSONPortPendingIsDistinctFromUnresolved(t *testing.T) {
	reserved := closedPort(t)
	row := jsonRow(t, dynamicManifest(),
		liveMetaWithState(t, state.StatePortPending, reserved, false), true)

	if row["status"] != string(process.StatusPortPending) {
		t.Errorf("status = %v, want port_pending", row["status"])
	}
	if row["status"] == string(process.StatusPortUnresolved) {
		t.Fatal("port_pending cannot be emitted as port_unresolved")
	}
	if row["port"] != float64(reserved) {
		t.Errorf("port = %v, want the reserved port %d", row["port"], reserved)
	}
	if row["port_verified"] != false {
		t.Errorf("port_verified = %v, want false: reserved is not verified", row["port_verified"])
	}
}

// Regression gate: a legacy manifest with no url_generation and port = 0 must behave exactly as before (the headless door resolves to none).
func TestJSONLegacyFixedZeroPortIsUnchanged(t *testing.T) {
	m := &manifest.Manifest{Name: "svc", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "run"}}}
	if gen := m.EffectiveURLGeneration(false); gen != manifest.URLGenNone {
		t.Fatalf("EffectiveURLGeneration = %q, want none", gen)
	}

	stopped := jsonRow(t, m, state.Meta{}, false)
	if stopped["port"] != float64(0) {
		t.Errorf("port = %v, want 0", stopped["port"])
	}
	if _, present := stopped["port_verified"]; present {
		t.Error("a service without a PID has no port contract to assert")
	}

	live := jsonRow(t, m, liveMetaWithState(t, state.StateRunning, 0, false), true)
	if live["port"] != float64(0) {
		t.Errorf("port = %v, want 0", live["port"])
	}
	if live["port_verified"] != false {
		t.Errorf("port_verified = %v, want false", live["port_verified"])
	}
	if live["status"] != string(process.StatusRunning) {
		t.Errorf("status = %v, want running: without a port contract the status does not change", live["status"])
	}
	if live["url_generation"] != manifest.URLGenNone {
		t.Errorf("url_generation = %v, want none", live["url_generation"])
	}
}

func TestJSONStoppedFixedKeepsDeclaredPort(t *testing.T) {
	m := &manifest.Manifest{Name: "svc", Commands: manifest.Commands{Start: manifest.StartCommand{Run: "run"}}, Port: 8080}
	row := jsonRow(t, m, state.Meta{}, false)

	if row["port"] != float64(8080) {
		t.Errorf("port = %v, want 8080: stopped, the declared one is all there is", row["port"])
	}
	if row["url_generation"] != manifest.URLGenByPort {
		t.Errorf("url_generation = %v, want by_port", row["url_generation"])
	}
	if _, present := row["port_verified"]; present {
		t.Error("without a PID there is no port contract to assert")
	}
}

// port_verified is tri-state: a false on an unconfigured row would assert something about a manifest that was never parsed.
func TestJSONUnconfiguredRowHasNoPortContract(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	p := scanner.Project{Path: "/nope", Name: "svc"}

	data, err := json.Marshal(buildProjectInfo(process.NewManager(), store, map[string]bool{}, p))
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}

	if row["configured"] != false {
		t.Fatalf("configured = %v, want false", row["configured"])
	}
	if _, present := row["port_verified"]; present {
		t.Error("an unconfigured row must not assert anything about its port")
	}
	if row["port"] != float64(0) {
		t.Errorf("port = %v, want 0", row["port"])
	}
}

func TestJSONManifestErrorRowHasNoPortContract(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	p := scanner.Project{
		Path: "/broken", Name: "svc", ManifestErr: "invalid manifest",
	}

	data, err := json.Marshal(buildProjectInfo(process.NewManager(), store, map[string]bool{}, p))
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}
	if row["manifest_error"] != "invalid manifest" {
		t.Errorf("manifest_error = %v", row["manifest_error"])
	}
	if _, present := row["port_verified"]; present {
		t.Error("with the manifest unparsed there is no port contract")
	}
}

// The JSON and the TUI must tell the same story about the same meta: before this change one service read port_unresolved in the TUI and running in the JSON.
func TestJSONAndTUIAgreeOnTheSameMeta(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	meta := liveMetaWithState(t, state.StatePortUnresolved, 0, false)
	if err := store.SaveMeta(dir, meta); err != nil {
		t.Fatal(err)
	}

	status, loaded := evaluateStatus(process.NewManager(), store, dir)
	if loaded.State != state.StatePortUnresolved {
		t.Fatalf("meta was not read: %+v", loaded)
	}
	if status != string(process.StatusPortUnresolved) {
		t.Errorf("evaluateStatus = %q, want port_unresolved (the TUI reads the same)", status)
	}
}
