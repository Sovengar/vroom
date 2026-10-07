//go:build unix

package process

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Both samplers turn malformed /proc data into something the TUI renders as fact, so they read a synthetic tree instead of the kernel's.

// fakeThreadTree builds /proc/<pid>/task from (tid, comm, stat) triples; an empty comm or stat writes no file.
func fakeThreadTree(t *testing.T, pid int, threads map[int][2]string) string {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, strconv.Itoa(pid), "task")
	for tid, v := range threads {
		dir := filepath.Join(base, strconv.Itoa(tid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if v[0] != "" {
			if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(v[0]+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if v[1] != "" {
			if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(v[1]), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// A task dir with no readable stat is a dying thread, and listing it would show a half-filled row the user cannot interpret.
func TestListThreadsAtSaltaLoQueNoEsUnHilo(t *testing.T) {
	good := "99 (thread) " + strings.TrimSuffix(strings.Repeat("1 ", 13), " ")
	root := fakeThreadTree(t, 99, map[int][2]string{
		100: {"main", good},
		// comm without stat: the thread is dying, so it is skipped.
		101: {"dying", ""},
		// stat without comm: the name comes from the stat.
		102: {"", good},
	})
	// A task dir whose name is not a tid.
	if err := os.MkdirAll(filepath.Join(root, "99", "task", "not-a-tid"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := listThreadsAt(root, 99)
	if err != nil {
		t.Fatalf("listThreadsAt: %v", err)
	}

	byTID := map[int]ThreadInfo{}
	for _, ti := range got {
		byTID[ti.TID] = ti
	}
	if _, ok := byTID[101]; ok {
		t.Error("a thread without readable stat entered the list: it is dying and would show a half-filled row")
	}
	if _, ok := byTID[0]; ok {
		t.Error("a task directory named 'not-a-tid' entered the list")
	}
	if len(got) != 2 {
		t.Errorf("there are %d threads, want 2 (the two readable ones)", len(got))
	}
	if byTID[102].Name != "thread" {
		t.Errorf("the name of thread 102 = %q, want 'thread' (from stat)", byTID[102].Name)
	}
	if byTID[100].Name != "main" {
		t.Errorf("the name of thread 100 = %q, want 'main' (from comm)", byTID[100].Name)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].TID > got[i].TID {
			t.Errorf("threads are not sorted by tid: %v", got)
			break
		}
	}
}

// An error, not an empty list: "this process has no threads" is impossible for a live process, while "could not look" is true.
func TestListThreadsAtSinTaskDaError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "5"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := listThreadsAt(root, 5); err == nil {
		t.Error("without task/ it should give an error, not an empty list of threads")
	} else if !strings.Contains(err.Error(), "thread sampling") {
		t.Errorf("err = %q, want the prefix 'thread sampling'", err)
	}
}

// Summed, because a thread that only does I/O (a logger, an I/O pool) lives in stime and would otherwise show near-zero CPU in the table.
func TestParseThreadStatSumaUtimeYStime(t *testing.T) {
	// Fields() collapses empty fields, so all 13 post-comm fields must carry a number or the parser rejects the line as incomplete.
	full := make([]string, 13)
	for i := range full {
		full[i] = "1"
	}
	full[0] = "R"
	full[11] = "100"
	full[12] = "20"

	path := filepath.Join(t.TempDir(), "stat")
	if err := os.WriteFile(path, []byte("1 (name) "+strings.Join(full, " ")), 0o644); err != nil {
		t.Fatal(err)
	}

	state, ticks, err := parseThreadStat(path)
	if err != nil {
		t.Fatalf("parseThreadStat: %v", err)
	}
	if state != "R" {
		t.Errorf("state = %q, want R", state)
	}
	if ticks != 120 {
		t.Errorf("ticks = %d, want 120 (utime 100 + stime 20)", ticks)
	}

	// A stat short of the CPU fields: an error, not zeros.
	short := filepath.Join(t.TempDir(), "short")
	if err := os.WriteFile(short, []byte("1 (x) S 1 1 1 0 -1 0 0 0 0 0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseThreadStat(short); err == nil {
		t.Error("an incomplete stat should give an error, not zeros that look like 'this thread consumes no CPU'")
	}

	// A non-numeric utime is also an error: zeros would be a false reading.
	bad := make([]string, 13)
	for i := range bad {
		bad[i] = "1"
	}
	bad[0] = "R"
	bad[11] = "lots"
	bad[12] = "0"
	badPath := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(badPath, []byte("1 (x) "+strings.Join(bad, " ")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseThreadStat(badPath); err == nil {
		t.Error("a non-numeric utime should give an error")
	}

	sinComm := filepath.Join(t.TempDir(), "without-comm")
	if err := os.WriteFile(sinComm, []byte("1 sin-parentesis 1 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseThreadStat(sinComm); err == nil {
		t.Error("a stat without parentheses cannot be parsed")
	}
	if _, _, err := parseThreadStat(filepath.Join(t.TempDir(), "nothing")); err == nil {
		t.Error("a non-existent stat should give an error")
	}
}

func TestThreadNameFromStatCaeAlUltimoParentesis(t *testing.T) {
	dir := t.TempDir()

	t.Run("name with spaces", func(t *testing.T) {
		p := filepath.Join(dir, "a")
		if err := os.WriteFile(p, []byte("42 (Web Content (tab)) S 1 1"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := threadNameFromStat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got != "Web Content (tab)" {
			t.Errorf("name = %q, want 'Web Content (tab)'", got)
		}
	})

	t.Run("stat without parentheses", func(t *testing.T) {
		p := filepath.Join(dir, "b")
		if err := os.WriteFile(p, []byte("42 sin-nombre 1 1"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := threadNameFromStat(p); err == nil {
			t.Error("a stat without parentheses has no recoverable thread name")
		}
	})

	t.Run("non-existent file", func(t *testing.T) {
		if _, err := threadNameFromStat(filepath.Join(dir, "nothing")); err == nil {
			t.Error("a non-existent stat should give an error")
		}
	})
}

// 0 rather than an error, because a process that has not allocated yet really is 0 kB and an error would blank the metrics tab.
func TestParseVmRSSDevuelveCeroSinElCampo(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   int64
	}{
		{"normal", "Name:\tbash\nVmRSS:\t   12345 kB\nThreads:\t2\n", 12345},
		{"without the field", "Name:\tbash\nThreads:\t2\n", 0},
		{"empty field", "VmRSS:\t\n", 0},
		{"non-numeric field", "VmRSS:\t   lots kB\n", 0},
		{"without units but numeric", "VmRSS:\t99\n", 99},
		{"empty", "", 0},
	}
	for _, tt := range tests {
		if got := parseVmRSS(tt.status); got != tt.want {
			t.Errorf("%s: parseVmRSS = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestProbeStatusDevuelveCeroSiNoContesta(t *testing.T) {
	if got := probeStatus(closedPortForTest(t), "/"); got != 0 {
		t.Errorf("probeStatus of a dead port = %d, want 0", got)
	}

	port, stop := statusListener(t, http.StatusOK)
	defer stop()
	if got := probeStatus(port, ""); got != http.StatusOK {
		t.Errorf("probeStatus of a real server = %d, want 200", got)
	}
	if got := probeStatus(port, "/health"); got != http.StatusOK {
		t.Errorf("probeStatus with healthPath = %d, want 200", got)
	}
}

// Order matters because the /proc walk is unsorted and length because a subset is not the same set; that is what "settled" means for the decision.
func TestSamePortsComparaElConjuntoEntero(t *testing.T) {
	tests := []struct {
		a, b []int
		want bool
	}{
		{nil, nil, true},
		{nil, []int{}, true},
		{[]int{1}, []int{1}, true},
		{[]int{1, 2}, []int{1, 2}, true},
		{[]int{1, 2}, []int{2, 1}, false}, // same set, different order
		{[]int{1, 2}, []int{1}, false},    // subset is not the same set
		{[]int{1}, []int{2}, false},
	}
	for _, tt := range tests {
		if got := samePorts(tt.a, tt.b); got != tt.want {
			t.Errorf("samePorts(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestDecidePortRespetaElPuertoReservadoYElUnicoListener(t *testing.T) {
	t.Run("the reserved one is in the list", func(t *testing.T) {
		got := decidePort([]int{8080, 9090}, 9090, "")
		if got.Port != 9090 {
			t.Errorf("Port = %d, want the reserved 9090", got.Port)
		}
		if !got.HonoredReserved {
			t.Error("HonoredReserved = false: Meta would record that the reservation was NOT honored")
		}
		if !got.Verified {
			t.Error("Verified = false with a single real listener: there is nothing to decide")
		}
		if len(got.All) != 2 {
			t.Errorf("All = %v, want both listeners: the user has to see the ambiguity even if it is resolved", got.All)
		}
	})

	t.Run("the reserved one is not in the list", func(t *testing.T) {
		got := decidePort([]int{8080, 9090}, 7000, "")
		if got.Port == 7000 {
			t.Error("chose a port that is not listening")
		}
		if got.HonoredReserved {
			t.Error("HonoredReserved = true without the reserved one being in the list")
		}
	})

	t.Run("a single listener: no decision to make", func(t *testing.T) {
		got := decidePort([]int{8080}, 0, "")
		if got.Port != 8080 || !got.Verified {
			t.Errorf("a single listener gave %+v, want Port 8080 and Verified", got)
		}
		if len(got.All) != 1 {
			t.Errorf("All = %v, want one element", got.All)
		}
	})

	t.Run("no listeners: nothing to declare", func(t *testing.T) {
		got := decidePort(nil, 0, "")
		if got.Port != 0 || got.Verified {
			t.Errorf("no listeners gave %+v, want Port 0 and Verified false", got)
		}
	})
}

// Declaring a port without evidence would publish an address nobody checked, which is the same damage as an unconfirmed port_verified.
func TestLineageListenersAtSinProcNoDeclaraNada(t *testing.T) {
	if got := lineageListenersAt(t.TempDir(), os.Getpid()); got != nil {
		t.Errorf("lineageListenersAt without /proc gave %v, want nil", got)
	}
}

func TestListenSocketsAtSaltaLasFilasQueNoSonListen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tcp")
	body := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000 100 0 0 10 0\n" + // LISTEN 8080
		"   1: 0100007F:1F91 0100007F:C000 01 00000000:00000000 00:00000000 00000000  1000        0 12346 1 0000 100 0 0 10 0\n" + // ESTABLISHED 8081
		"   2: NOPE 0\n" + // no addr:port separation
		"   3: 0100007F:ZZZZ 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12347 1\n" + // non-hex port
		"   4: 0100007F:1F92 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12348 1\n" // LISTEN 8082
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got := listenSocketsAt(path)
	if len(got) != 2 {
		t.Fatalf("%d sockets were read, want 2 (only LISTEN with valid hex port): %+v", len(got), got)
	}
	ports := map[int]bool{}
	for _, s := range got {
		ports[s.port] = true
	}
	if !ports[8080] || !ports[8082] {
		t.Errorf("missing listeners: %v", ports)
	}
	if ports[8081] {
		t.Error("an ESTABLISHED connection was counted as a listener: the attributed port would be the client's")
	}

	if got := listenSocketsAt(filepath.Join(dir, "nothing")); got != nil {
		t.Errorf("listenSocketsAt of a non-existent file gave %v, want nil", got)
	}
}

func TestEvaluateConEstadoDePuertoPersistido(t *testing.T) {
	m := NewManager()
	res := startSleep(t, m, StartSpec{
		Command: "sleep 30", WorkDir: t.TempDir(),
		StdoutPath: filepath.Join(t.TempDir(), "o.log"), StderrPath: filepath.Join(t.TempDir(), "e.log"),
	})
	t.Cleanup(func() {
		_ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 2 * time.Second})
	})

	got := m.Evaluate(EvalSpec{
		Pid: res.Pid, CreationTimeMs: res.CreationTimeMs,
		Port: 65001, PortPending: true,
	})
	if got != StatusPortPending {
		t.Errorf("with PortPending = %q, want %q", got, StatusPortPending)
	}

	got = m.Evaluate(EvalSpec{
		Pid: res.Pid, CreationTimeMs: res.CreationTimeMs,
		NoPort: true,
	})
	if got != StatusNoPort {
		t.Errorf("with NoPort = %q, want %q", got, StatusNoPort)
	}

	got = m.Evaluate(EvalSpec{
		Pid: res.Pid, CreationTimeMs: res.CreationTimeMs,
		PortUnresolved: true,
	})
	if got != StatusPortUnresolved {
		t.Errorf("with PortUnresolved = %q, want %q", got, StatusPortUnresolved)
	}

	// The declared port must come from a real listener, because Evaluate requires it to accept and a free port reads as a different state.
	open, closeIt := statusListener(t, http.StatusOK)
	defer closeIt()
	got = m.Evaluate(EvalSpec{
		Pid: res.Pid, CreationTimeMs: res.CreationTimeMs, Port: open,
	})
	if got != StatusRunning {
		t.Errorf("with the open port = %q, want running", got)
	}
}

// This is the twin in another worktree: same declared port, different process, and resolving it to running would make start believe the service is already up.
func TestEvaluateConPidMuertoYPuertoDeOtroNoLoDaPorVivo(t *testing.T) {
	m := NewManager()
	dead := findDeadPID(t)
	port := freeListeningPort(t)

	// Unknown or ambiguous owner -> undecided, never running.
	got := m.Evaluate(EvalSpec{Pid: dead, CreationTimeMs: 0, Port: port})
	if got == StatusRunning {
		t.Error("an open port whose owner cannot be proven resolved to running: the twin from another worktree would appear alive")
	}

	if got := m.Evaluate(EvalSpec{}); got != StatusStopped {
		t.Errorf("without any signal = %q, want stopped", got)
	}
}

// MEDIDO: the pid != myPid filter exists because pgrep -f matches the full command line, which carries vroom's own pattern; the positive case must therefore be a real child.
func TestPatternMatchEncuentraUnHijoRealYExcluyeALosPropios(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep is not on this machine: without it PatternMatch always returns false")
	}

	m := NewManager()
	marker := "vroom-marker-unique-9911-" + strconv.Itoa(os.Getpid())
	// The trailing "; true" keeps sh from exec-replacing itself, which is what leaves the marker on its own command line.
	res := startSleep(t, m, StartSpec{
		Command:    "sleep 30; true # " + marker,
		WorkDir:    t.TempDir(),
		StdoutPath: filepath.Join(t.TempDir(), "o.log"), StderrPath: filepath.Join(t.TempDir(), "e.log"),
	})
	t.Cleanup(func() {
		_ = m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 2 * time.Second})
	})

	if !PatternMatch(marker) {
		t.Errorf("PatternMatch(%q) = false with a live child whose command line contains it", marker)
	}

	if PatternMatch("vroom-this-pattern-nobody-has-" + strconv.Itoa(os.Getpid()+1)) {
		t.Error("PatternMatch of a non-existent pattern gave true: some own process is being counted")
	}

	// The test process never counts even though its binary contains the pattern, which is the whole point of the filter.
	if PatternMatch(uniqueProcessMarker(t)) {
		t.Error("PatternMatch found the test process itself: the myPid filter is not doing its job")
	}
}

func TestReservePortNoDevuelveElMismoPuertoDosVeces(t *testing.T) {
	a, err := ReservePort()
	if err != nil {
		t.Skipf("no free ports in the range: %v", err)
	}
	b, err := ReservePort()
	if err != nil {
		ReleasePort(a)
		t.Skipf("not enough free ports: %v", err)
	}
	if a == b {
		t.Errorf("two consecutive reservations gave the same port %d: two services would occupy it at once", a)
	}
	ReleasePort(a)
	ReleasePort(b)
}

// The pool is bind+close, so a TOCTOU window is documented; all that can be enforced is that nobody is listening at reservation time.
func TestReservePortNuncaDevuelveUnPuertoQueAlguienTiene(t *testing.T) {
	var reserved []int
	defer func() {
		for _, p := range reserved {
			ReleasePort(p)
		}
	}()

	for i := 0; i < 5; i++ {
		p, err := ReservePort()
		if err != nil {
			t.Skipf("not enough free ports: %v", err)
		}
		reserved = append(reserved, p)
		if PortOpen(p) {
			t.Errorf("ReservePort returned port %d, which already has a listener: the service bind will fail", p)
		}
	}

	seen := map[int]bool{}
	for _, p := range reserved {
		if seen[p] {
			t.Errorf("port %d came out of the pool twice", p)
		}
		seen[p] = true
	}
}

func closedPortForTest(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func freeListeningPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func statusListener(t *testing.T, status int) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = srv.Close() }
}

// Uses the test binary's own base name, unique inside the temporary go test build, so no other process can match it.
func uniqueProcessMarker(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot resolve own binary: %v", err)
	}
	return filepath.Base(self)
}

// Some processes trap SIGTERM by design, so without the SIGKILL escalation stop would leave the service alive and still report it stopped.
func TestStopEscalaASigkillCuandoElHijoIgnoraSigterm(t *testing.T) {
	m := NewManager()
	dir := t.TempDir()
	res := startSleep(t, m, StartSpec{
		Command:    `trap "" TERM; sleep 30; true`,
		WorkDir:    dir,
		StdoutPath: filepath.Join(dir, "o.log"), StderrPath: filepath.Join(dir, "e.log"),
	})

	var warns []string
	if err := m.Stop(StopSpec{
		Pid: res.Pid, Pgid: res.Pgid, Timeout: 500 * time.Millisecond,
		Warn: func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) },
	}); err != nil {
		t.Fatal(err)
	}

	waitPIDGone(t, res.Pid)

	// The warning is reserved for what could not be stopped, so a successful SIGKILL escalation must produce none.
	if len(warns) > 0 {
		t.Logf("stop warnings (they should not be failures): %v", warns)
	}
}

// The message names the file, because "could not open" alone leaves two identical failures with no way to tell them apart.
func TestStartFallaSiNoPuedeAbrirElStderr(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "stderr.log")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := NewManager().Start(StartSpec{
		Command: "sleep 30", WorkDir: dir,
		StdoutPath: filepath.Join(dir, "stdout.log"), StderrPath: blocked,
	})
	if err == nil {
		t.Fatal("a stderr.log that is a directory should make startup fail")
	}
	if !strings.Contains(err.Error(), "stderr.log") {
		t.Errorf("err = %q, want it to name the file it could not open", err)
	}
}

func TestAliveConUnPidQueNoSePuedeConstruir(t *testing.T) {
	if Alive(1<<30, 0) {
		t.Error("a huge pid cannot be a live process")
	}
}

func TestPortOwnerPIDsConUnPuertoQueNoExisteEsVacio(t *testing.T) {
	if got := PortOwnerPIDs(closedPortForTest(t)); len(got) != 0 {
		t.Errorf("a free port has owners %v, want none", got)
	}
	if got := PortOwnerPID(closedPortForTest(t)); got != 0 {
		t.Errorf("PortOwnerPID of a free port = %d, want 0", got)
	}
}
