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

// One real process for the whole binary. Evaluate needs a genuinely live PID
// with a matching creation_time to reach the port branches; a stub that
// reimplemented Evaluate would only test the stub. It is a plain `sleep`, not
// a service: no discovery, no ports, and it costs a few milliseconds once.
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
			t.Fatalf("proceso vivo compartido: %v", err)
		}
		liveRes = res
	})
	return liveRes
}

// openPort returns a port that is really accepting, so Evaluate's dial
// succeeds and the resolved path is exercised instead of failing closed.
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

// These tests assert on the MARSHALLED project row, not on internal helpers:
// the JSON is the contract agents consume, so a test that only reads
// ProjectInfo fields cannot catch a wrong tag or an omitted field.

// jsonRow builds a service, seeds its meta, and returns the marshalled row.
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

// liveMetaWithState seeds a meta backed by the real live process, in the given
// state, with the given port.
func liveMetaWithState(t *testing.T, s string, port int, verified bool) state.Meta {
	t.Helper()
	res := liveProcess(t)
	return state.Meta{
		Name: "svc", Port: port, PortVerified: verified,
		Pid: res.Pid, Pgid: res.Pgid, CreationTimeMs: res.CreationTimeMs, State: s,
	}
}

// closedPort returns a port nobody is listening on, without a fixed value.
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
		Name: "svc", Command: "run", Port: 8080, PortMode: manifest.PortModeDynamic,
	}
}

// port_unresolved: el puerto NO se emite. Emitir el declarado aquí es lo que
// hace que un agente se conecte al puerto del twin de otro worktree.
func TestJSONPortUnresolvedEmitsNoPort(t *testing.T) {
	row := jsonRow(t, dynamicManifest(),
		liveMetaWithState(t, state.StatePortUnresolved, 0, false), true)

	if row["port"] != float64(0) {
		t.Errorf("port = %v, want 0: un puerto sin confirmar no se emite", row["port"])
	}
	if row["declared_port"] != float64(8080) {
		t.Errorf("declared_port = %v, want 8080: el declarado se publica aparte", row["declared_port"])
	}
	verified, present := row["port_verified"]
	if !present {
		t.Fatal("port_verified ausente: con omitempty sobre un bool, false no puede emitirse")
	}
	if verified != false {
		t.Errorf("port_verified = %v, want false", verified)
	}
	if row["status"] != string(process.StatusPortUnresolved) {
		t.Errorf("status = %v, want port_unresolved", row["status"])
	}
	if row["status"] == string(process.StatusRunning) {
		t.Error("un puerto sin resolver no puede reportarse como running")
	}
}

// Un servicio dinámico resuelto: puerto real, verificado, estado sano.
func TestJSONResolvedDynamicEmitsRealPort(t *testing.T) {
	port := openPort(t)
	row := jsonRow(t, dynamicManifest(),
		liveMetaWithState(t, state.StateRunning, port, true), true)

	if row["port"] != float64(port) {
		t.Errorf("port = %v, want el puerto real %d", row["port"], port)
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

// no_port: vivo, sin puerto TCP, y el estado no afirma que tenga uno.
func TestJSONNoPortIsNotReportedAsRunning(t *testing.T) {
	row := jsonRow(t, dynamicManifest(),
		liveMetaWithState(t, state.StateNoPort, 0, false), true)

	if row["port"] != float64(0) {
		t.Errorf("port = %v, want 0", row["port"])
	}
	if row["status"] != string(process.StatusNoPort) {
		t.Errorf("status = %v, want no_port (no puede afirmar running con puerto 0)", row["status"])
	}
	if row["status"] == string(process.StatusPortUnresolved) {
		t.Error("no_port y port_unresolved no pueden colapsar en el mismo status")
	}
	if row["port_verified"] != false {
		t.Errorf("port_verified = %v, want false", row["port_verified"])
	}
}

// port_pending: discovery en vuelo. Se decide pending, y se fija que NO es lo
// mismo que unresolved — son hechos distintos y el JSON los distingue.
func TestJSONPortPendingIsDistinctFromUnresolved(t *testing.T) {
	reserved := closedPort(t)
	row := jsonRow(t, dynamicManifest(),
		liveMetaWithState(t, state.StatePortPending, reserved, false), true)

	if row["status"] != string(process.StatusPortPending) {
		t.Errorf("status = %v, want port_pending", row["status"])
	}
	if row["status"] == string(process.StatusPortUnresolved) {
		t.Fatal("port_pending no puede emitirse como port_unresolved")
	}
	if row["port"] != float64(reserved) {
		t.Errorf("port = %v, want el puerto reservado %d", row["port"], reserved)
	}
	if row["port_verified"] != false {
		t.Errorf("port_verified = %v, want false: reservado no es verificado", row["port_verified"])
	}
}

// Puerta de no-regresión: un manifiesto legacy sin port_mode y con port = 0
// se comporta exactamente como antes.
func TestJSONLegacyFixedZeroPortIsUnchanged(t *testing.T) {
	m := &manifest.Manifest{Name: "svc", Command: "run"} // sin port_mode, sin port
	if mode := m.EffectivePortMode(); mode != manifest.PortModeNone {
		t.Fatalf("EffectivePortMode = %q, want none", mode)
	}

	// Parado: como hoy, puerto 0 y sin port_verified (nunca hubo contrato).
	stopped := jsonRow(t, m, state.Meta{}, false)
	if stopped["port"] != float64(0) {
		t.Errorf("port = %v, want 0", stopped["port"])
	}
	if _, present := stopped["port_verified"]; present {
		t.Error("un servicio sin PID no tiene contrato de puerto que afirmar")
	}

	// Vivo: port = 0, y ahora port_verified presente y false.
	live := jsonRow(t, m, liveMetaWithState(t, state.StateRunning, 0, false), true)
	if live["port"] != float64(0) {
		t.Errorf("port = %v, want 0", live["port"])
	}
	if live["port_verified"] != false {
		t.Errorf("port_verified = %v, want false", live["port_verified"])
	}
	if live["status"] != string(process.StatusRunning) {
		t.Errorf("status = %v, want running: sin contrato de puerto el estado no cambia", live["status"])
	}
	if live["port_mode"] != manifest.PortModeNone {
		t.Errorf("port_mode = %v, want none", live["port_mode"])
	}
}

// Un manifiesto legacy fixed con puerto y servicio PARADO conserva el puerto
// declarado: es la única información que existe.
func TestJSONStoppedFixedKeepsDeclaredPort(t *testing.T) {
	m := &manifest.Manifest{Name: "svc", Command: "run", Port: 8080} // sin port_mode
	row := jsonRow(t, m, state.Meta{}, false)

	if row["port"] != float64(8080) {
		t.Errorf("port = %v, want 8080: parado, el declarado es lo único que hay", row["port"])
	}
	if row["port_mode"] != manifest.PortModeFixed {
		t.Errorf("port_mode = %v, want fixed", row["port_mode"])
	}
	if _, present := row["port_verified"]; present {
		t.Error("sin PID no hay contrato de puerto que afirmar")
	}
}

// port_verified es tri-estado: ausente en una fila no configurada. Un
// false ahí afirmaría algo sobre un manifiesto que nunca llegó a parsearse.
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
		t.Error("una fila no configurada no debe afirmar nada sobre su puerto")
	}
	if row["port"] != float64(0) {
		t.Errorf("port = %v, want 0", row["port"])
	}
}

// Un manifiesto inválido tampoco emite contrato de puerto, pero sí su error.
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
		t.Error("con el manifiesto sin parsear no hay contrato de puerto")
	}
}

// La superficie JSON y la TUI cuentan la misma historia sobre el mismo meta.
// Antes de este cambio el mismo servicio se leía "port_unresolved" en la TUI
// y "running" en el JSON.
func TestJSONAndTUIAgreeOnTheSameMeta(t *testing.T) {
	store := state.NewStoreAt(t.TempDir())
	dir := t.TempDir()
	meta := liveMetaWithState(t, state.StatePortUnresolved, 0, false)
	if err := store.SaveMeta(dir, meta); err != nil {
		t.Fatal(err)
	}

	status, loaded := evaluateStatus(process.NewManager(), store, dir)
	if loaded.State != state.StatePortUnresolved {
		t.Fatalf("el meta no se leyó: %+v", loaded)
	}
	if status != string(process.StatusPortUnresolved) {
		t.Errorf("evaluateStatus = %q, want port_unresolved (la TUI lee lo mismo)", status)
	}
}
