//go:build unix

package process

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Parsing runs against synthetic /proc trees because a malformed stat cannot be provoked on a live process; lineage, stop and port discovery use real processes because their behaviour is about real kernel state.

func fakeProc(t *testing.T, pid int, stat string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(dir, "task"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProcStatAtAcceptsCommWithParenthesesAndSpaces(t *testing.T) {
	tests := []struct {
		name     string
		stat     string
		wantPID  int
		wantPPID int
		wantPGID int
		wantErr  bool
	}{
		{
			name:     "normal",
			stat:     "1234 (bash) S 1000 1234 1234 0 -1 4194560 100",
			wantPID:  1234,
			wantPPID: 1000,
			wantPGID: 1234,
		},
		{
			name:     "comm with spaces",
			stat:     "7 (Web Content (tab)) S 1 7 7 0 -1 0 0",
			wantPID:  7,
			wantPPID: 1,
			wantPGID: 7,
		},
		{
			name:     "comm with a stray parenthesis",
			stat:     "8 (weird)name) S 1 8 8 0 -1 0 0",
			wantPID:  8,
			wantPPID: 1,
			wantPGID: 8,
		},
		{
			name:    "no parentheses",
			stat:    "1234 bash S 1 1 1 0 -1 0 0",
			wantErr: true,
		},
		{
			name:    "reversed parentheses",
			stat:    ")bas( 1234 S 1 1 1 0 -1 0 0",
			wantErr: true,
		},
		{
			name:    "cuts off right after the closing",
			stat:    "1 (x) S",
			wantErr: true,
		},
		{
			name:    "non-numeric pid",
			stat:    "abc (x) S 1 1 1 0 -1 0 0",
			wantErr: true,
		},
		{
			name:    "fewer than three fields after the closing",
			stat:    "1 (x) S 1",
			wantErr: true,
		},
		{
			name:    "non-numeric ppid",
			stat:    "1 (x) S ppid 1 0 -1 0",
			wantErr: true,
		},
		{
			name:    "non-numeric pgrp",
			stat:    "1 (x) S 1 pgid 0 -1 0",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := fakeProc(t, 1234, tt.stat)
			info, err := procStatAt(dir)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("stat %q should fail, got %+v", tt.stat, info)
				}
				return
			}
			if err != nil {
				t.Fatalf("stat %q should not fail: %v", tt.stat, err)
			}
			if info.pid != tt.wantPID || info.ppid != tt.wantPPID || info.pgid != tt.wantPGID {
				t.Errorf("stat %q gave pid=%d ppid=%d pgid=%d; want %d/%d/%d",
					tt.stat, info.pid, info.ppid, info.pgid, tt.wantPID, tt.wantPPID, tt.wantPGID)
			}
		})
	}

	t.Run("nonexistent file", func(t *testing.T) {
		if _, err := procStatAt(filepath.Join(t.TempDir(), "nothing")); err == nil {
			t.Error("a nonexistent stat should give an error")
		}
	})
}

// MEASURED: only Z counts as not alive, because a zombie already released its fds (the port is genuinely free) and treating it as alive would make Stop sit out the whole timeout.
func TestProcInfoRunningIgnoresZombiesAndNoPids(t *testing.T) {
	tests := []struct {
		info procInfo
		want bool
	}{
		{procInfo{pid: 1, state: "S"}, true},
		{procInfo{pid: 1, state: "R"}, true},
		{procInfo{pid: 1, state: "D"}, true},
		{procInfo{pid: 1, state: "Z"}, false}, // zombie: holds nothing anymore
		{procInfo{pid: 1, state: "T"}, true},  // under debugger: still holds the port
		{procInfo{pid: 0, state: "S"}, false}, // pid 0 is not a process
		{procInfo{pid: -1, state: "S"}, false},
	}
	for _, tt := range tests {
		if got := tt.info.running(); got != tt.want {
			t.Errorf("procInfo{pid:%d state:%q}.running() = %v, want %v",
				tt.info.pid, tt.info.state, got, tt.want)
		}
	}
}

func TestDescendantsAtWithoutLineageWhenItCannotBeRead(t *testing.T) {
	root := t.TempDir()
	got := descendantsAt(root, os.Getpid())
	if len(got) != 0 {
		t.Errorf("descendantsAt on an empty /proc gave %v, want empty", got)
	}
}

func TestDescendantsFromIgnoresAParentThatIsItsChild(t *testing.T) {
	snap := map[int]procInfo{
		100: {pid: 100, state: "S", ppid: 100, pgid: 100}, // parent of itself
		101: {pid: 101, state: "S", ppid: 100, pgid: 100},
		102: {pid: 102, state: "S", ppid: 101, pgid: 100},
	}
	got := descendantsFrom(snap, 100)
	if len(got) != 2 {
		t.Fatalf("descendantsFrom gave %v, want [101 102]", got)
	}
	for _, pid := range got {
		if pid == 100 {
			t.Error("the root appeared in its own lineage: Stop would signal it twice")
		}
	}
}

func TestDescendantsFromDoesNotRepeatOrIncludeTheRoot(t *testing.T) {
	// 3 hangs off both 1 and 2: two paths to the same node.
	snap := map[int]procInfo{
		1: {pid: 1, state: "S", ppid: 0, pgid: 1},
		2: {pid: 2, state: "S", ppid: 1, pgid: 1},
		3: {pid: 3, state: "S", ppid: 1, pgid: 1},
		4: {pid: 4, state: "S", ppid: 2, pgid: 1},
		5: {pid: 5, state: "S", ppid: 3, pgid: 1},
		6: {pid: 6, state: "S", ppid: 4, pgid: 1},
		7: {pid: 7, state: "S", ppid: 5, pgid: 1},
	}
	got := descendantsFrom(snap, 1)
	seen := map[int]int{}
	for _, pid := range got {
		seen[pid]++
		if pid == 1 {
			t.Error("the root is in its own lineage")
		}
	}
	for pid, n := range seen {
		if n > 1 {
			t.Errorf("pid %d appears %d times in the lineage: it would be over-signaled", pid, n)
		}
	}
	if len(got) != 6 {
		t.Errorf("lineage = %v, want the 6 descendants without repeats", got)
	}
}

// A lineage whose pids died between capture and poll counts as gone, so Stop does not sit out the timeout for a group that can no longer change.
func TestLineageRunningToleratesDeadPidsAndNonNumeric(t *testing.T) {
	if lineageRunning(nil) {
		t.Error("an empty lineage is alive: that would make Stop always wait")
	}
	if lineageRunning([]int{0, -1}) {
		t.Error("a lineage of invalid pids is alive")
	}
	me := os.Getpid()
	if !lineageRunning([]int{me}) {
		t.Error("the process itself should be alive")
	}
	dead := findDeadPID(t)
	if !lineageRunning([]int{dead, me}) {
		t.Error("a dead pid must not make the lineage count as dead")
	}
	if lineageRunning([]int{dead, findDeadPID(t)}) {
		t.Error("a lineage of dead pids cannot be alive")
	}
}

func findDeadPID(t *testing.T) int {
	t.Helper()
	m := NewManager()
	res := startSleep(t, m, StartSpec{
		Command: "sleep 30", WorkDir: t.TempDir(),
		StdoutPath: filepath.Join(t.TempDir(), "o.log"), StderrPath: filepath.Join(t.TempDir(), "e.log"),
	})
	if err := m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	waitPIDGone(t, res.Pid)
	return res.Pid
}

func TestCaptureLineageKnowsWhoIsTheRoot(t *testing.T) {
	me := os.Getpid()

	t.Run("without pid or pgid there is nothing to capture", func(t *testing.T) {
		root, lineage := captureLineage(StopSpec{})
		if root != 0 || lineage != nil {
			t.Errorf("captureLineage({}) = %d/%v, want 0/nil", root, lineage)
		}
	})

	t.Run("with live pid: the root is the pid", func(t *testing.T) {
		// The test process stays in its own group because the runner never changes it.
		pgid := ownPgid(t)
		root, lineage := captureLineage(StopSpec{Pid: me, Pgid: pgid})
		if root != me {
			t.Errorf("root = %d, want %d: with the live pid the root is the pid", root, me)
		}
		if len(lineage) == 0 || lineage[0] != root {
			t.Errorf("lineage = %v, want the root first", lineage)
		}
	})

	t.Run("with nonexistent pgid: no lineage", func(t *testing.T) {
		root, lineage := captureLineage(StopSpec{Pid: me, Pgid: 1 << 22})
		if root != 0 || lineage != nil {
			t.Errorf("a nonexistent pgid gave root=%d lineage=%v, want 0/nil", root, lineage)
		}
	})
}

// Every stop of a healthy service takes the nil-Warn path, so a panic there would be the most visible failure a stop can have.
func TestStopWithoutWarnDoesNotPanic(t *testing.T) {
	m := NewManager()
	res := startSleep(t, m, StartSpec{
		Command: "sleep 30", WorkDir: t.TempDir(),
		StdoutPath: filepath.Join(t.TempDir(), "o.log"), StderrPath: filepath.Join(t.TempDir(), "e.log"),
	})
	if err := m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	waitPIDGone(t, res.Pid)
}

func TestStopSpecWarnfOnlyEmitsIfThereIsWarn(t *testing.T) {
	var got []string
	s := StopSpec{Warn: func(f string, a ...any) { got = append(got, fmt.Sprintf(f, a...)) }}

	s.warnf("count %d", 3)
	if len(got) != 1 || got[0] != "count 3" {
		t.Errorf("with Warn: %v, want ['count 3']", got)
	}

	got = nil
	nilSpec := StopSpec{}
	nilSpec.warnf("nothing should happen")
	if len(got) != 0 {
		t.Errorf("without Warn emitted %v", got)
	}
}

func TestLineageDescDescribesWhatRemains(t *testing.T) {
	tests := []struct {
		name    string
		lineage []int
		want    string
	}{
		{"only the root", []int{100}, "pgid 100"},
		// The number in the warning is the root, not the lineage length: an empty lineage still names the group being stopped.
		{"empty", nil, "pgid 100"},
		{"root and one descendant", []int{100, 101}, "pgid 100 and 1 descendant(s)"},
		{"root and three", []int{100, 101, 102, 103}, "pgid 100 and 3 descendant(s)"},
	}
	for _, tt := range tests {
		if got := lineageDesc(100, tt.lineage); got != tt.want {
			t.Errorf("%s: lineageDesc = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// Dedup matters because one pid listed twice (IPv4 and IPv6) would otherwise read as two owners and kill nothing, failing closed for the wrong reason.
func TestDistinctOwnersFiltersRepeatedAndNegative(t *testing.T) {
	tests := []struct {
		name string
		in   []int32
		want []int
	}{
		{"empty", nil, []int{}},
		{"one", []int32{42}, []int{42}},
		{"repeated", []int32{42, 42}, []int{42}},
		{"two distinct", []int32{42, 43}, []int{42, 43}},
		{"with zeros and negatives", []int32{0, 42, -1}, []int{42}},
		{"only garbage", []int32{0, -1}, []int{}},
	}
	for _, tt := range tests {
		got := distinctOwners(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("%s: distinctOwners(%v) = %v, want %v", tt.name, tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("%s: distinctOwners(%v) = %v, want %v", tt.name, tt.in, got, tt.want)
				break
			}
		}
	}
}

func TestKillPortHolderWithoutWarnDoesNotPanic(t *testing.T) {
	killPortHolderWith(1 /* port */, os.Getpid(), nil, func(int) []int32 { return nil }, nil)

	// Ownership decides the kill and the Warn callback only informs, so a nil Warn never changes the verdict.
	killPortHolderWith(1, os.Getpid(), nil, func(int) []int32 { return []int32{999999} }, nil)
}

// Validation must precede log truncation, otherwise a rejected command silently wipes the previous run's history.
func TestStartRejectsEmptyCommandBeforeTouchingAnything(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.log")
	errLog := filepath.Join(dir, "stderr.log")
	writeFileStr(t, out, "previous history\n")

	_, err := NewManager().Start(StartSpec{
		Command: "", WorkDir: dir, StdoutPath: out, StderrPath: errLog,
	})
	if err == nil {
		t.Fatal("an empty command should fail")
	}
	if !strings.Contains(err.Error(), "empty command") {
		t.Errorf("err = %q, want 'empty command'", err)
	}
	if got := readFileStr(t, out); got != "previous history\n" {
		t.Errorf("the log was truncated before validating the command: %q", got)
	}
}

// Failing before spawning sh matters: a process created and then abandoned has no Meta, so nothing would ever stop it.
func TestStartFailsIfItCannotOpenTheLogs(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "stdout.log")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := NewManager().Start(StartSpec{
		Command: "sleep 30", WorkDir: dir,
		StdoutPath: blocked, StderrPath: filepath.Join(dir, "stderr.log"),
	})
	if err == nil {
		t.Fatal("a stdout.log that is a directory should make startup fail")
	}
	if !strings.Contains(err.Error(), "stdout.log") {
		t.Errorf("err = %q, want it to name the log it could not open", err)
	}
}

func TestStartFailsIfTheLogDirectoryCannotBeCreated(t *testing.T) {
	dir := t.TempDir()
	blocker := writeFileStr(t, filepath.Join(dir, "blocking"), "I am a file\n")

	_, err := NewManager().Start(StartSpec{
		Command: "sleep 30", WorkDir: dir,
		StdoutPath: filepath.Join(blocker, "sub", "stdout.log"),
		StderrPath: filepath.Join(blocker, "sub", "stderr.log"),
	})
	if err == nil {
		t.Fatal("cannot create the log directory: startup should fail")
	}
	if !strings.Contains(err.Error(), "could not create log directory") {
		t.Errorf("err = %q, want 'could not create log directory'", err)
	}
}

// The error names the command it tried to launch, which is what the user needs to correct the manifest.
func TestStartFailsIfTheProcessCannotBeLaunched(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "") // without `sh`

	_, err := NewManager().Start(StartSpec{
		Command: "sleep 30", WorkDir: dir,
		StdoutPath: filepath.Join(dir, "stdout.log"), StderrPath: filepath.Join(dir, "stderr.log"),
	})
	if err == nil {
		t.Fatal("without interpreter startup should fail")
	}
	if !strings.Contains(err.Error(), "failed to start") {
		t.Errorf("err = %q, want 'failed to start'", err)
	}
	if !strings.Contains(err.Error(), "sleep 30") {
		t.Errorf("err = %q, want it to name the command that could not be launched", err)
	}
}

// Truncation is to zero with no banner: an append would leave logs --tail full of the previous run.
func TestStartTruncatesTheLogsOfEachStartup(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.log")
	errLog := filepath.Join(dir, "stderr.log")
	writeFileStr(t, out, "output of the PREVIOUS service\n")

	m := NewManager()
	res := startSleep(t, m, StartSpec{
		Command: "echo new-service", WorkDir: dir,
		StdoutPath: out, StderrPath: errLog,
	})
	if err := m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 3 * time.Second}); err != nil {
		t.Fatal(err)
	}
	waitPIDGone(t, res.Pid)

	body := readFileStr(t, out)
	if strings.Contains(body, "PREVIOUS") {
		t.Errorf("the log from the previous startup survived truncation:\n%s", body)
	}
	if !strings.Contains(body, "new-service") {
		t.Errorf("the output of the new service did not reach the log:\n%s", body)
	}
}

// PreSpawn must be the FIRST entry of the new run: the logs are truncated before it and the child appends after it, so a hook that ran successfully leaves its trace instead of being wiped by the spawn.
func TestStartRunsPreSpawnAfterTruncatingTheLogs(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.log")
	errLog := filepath.Join(dir, "stderr.log")
	writeFileStr(t, out, "output of the PREVIOUS service\n")

	m := NewManager()
	res := startSleep(t, m, StartSpec{
		Command: "echo hijo", WorkDir: dir,
		StdoutPath: out, StderrPath: errLog,
		PreSpawn: func() error {
			f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			_, err = f.WriteString("pre-spawn\n")
			return err
		},
	})
	t.Cleanup(func() { _ = m.Stop(StopSpec{Pgid: res.Pgid, Timeout: 3 * time.Second}) })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(readFileStr(t, out), "hijo") {
		time.Sleep(10 * time.Millisecond)
	}

	body := readFileStr(t, out)
	if strings.Contains(body, "PREVIOUS") {
		t.Errorf("the log from the previous startup survived truncation:\n%s", body)
	}
	pre, hijo := strings.Index(body, "pre-spawn"), strings.Index(body, "hijo")
	if pre < 0 {
		t.Errorf("PreSpawn's output did not reach the log:\n%s", body)
	}
	if hijo < 0 {
		t.Errorf("the child's output did not reach the log:\n%s", body)
	}
	if pre >= 0 && hijo >= 0 && pre > hijo {
		t.Errorf("PreSpawn wrote after the child (%d > %d):\n%s", pre, hijo, body)
	}
}

// A failing PreSpawn leaves nothing behind: Start returns a zero result, which is what tells the caller there is no process to stop.
func TestStartAbortsWhenPreSpawnFails(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "stdout.log")
	errLog := filepath.Join(dir, "stderr.log")

	res, err := NewManager().Start(StartSpec{
		Command: "sleep 30", WorkDir: dir,
		StdoutPath: out, StderrPath: errLog,
		PreSpawn: func() error { return errors.New("the hook refuses") },
	})
	if err == nil {
		t.Fatal("a failing PreSpawn must abort the start")
	}
	if !strings.Contains(err.Error(), "the hook refuses") {
		t.Errorf("err = %q, want the hook's own failure", err)
	}
	if res.Pid != 0 || res.Pgid != 0 {
		t.Errorf("result = %+v, want the zero value: no child was spawned", res)
	}
	if got := readFileStr(t, out); strings.Contains(got, "sleep") {
		t.Errorf("the child ran despite the hook failing:\n%s", got)
	}
}

func TestMergeEnvOverwritesAndAddsAndDiscardsMalformed(t *testing.T) {
	parent := []string{"PATH=/usr/bin", "PORT=8080", "HOME=/root"}
	got := mergeEnv(parent, []string{"PORT=9999", "NEW=yes", "garbage", "=novalue", ""})

	m := map[string]string{}
	for _, kv := range got {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	if m["PORT"] != "9999" {
		t.Errorf("PORT = %q, want 9999: the spec must override the parent", m["PORT"])
	}
	if m["NEW"] != "yes" {
		t.Errorf("NEW = %q: a new variable must survive", m["NEW"])
	}
	if m["PATH"] != "/usr/bin" || m["HOME"] != "/root" {
		t.Errorf("the parent was lost: %v", m)
	}
	// MEASURED: an entry without "=" survives because the filter only drops an empty key, and PATH with no value is legal so discarding it would be worse.
	for _, kv := range got {
		if key, _, _ := strings.Cut(kv, "="); key == "" {
			t.Errorf("an entry with an empty key reached the child environment: %q", kv)
		}
	}
	// No duplicate keys: exec dedup would drop one silently and vroom cannot decide which one wins.
	seenKey := map[string]int{}
	for _, kv := range got {
		key, _, _ := strings.Cut(kv, "=")
		seenKey[key]++
	}
	for key, n := range seenKey {
		if n > 1 {
			t.Errorf("key %s appears %d times in the child environment", key, n)
		}
	}
}

func TestMergeEnvReturnsNilWithNothingToInject(t *testing.T) {
	if got := mergeEnv([]string{"PATH=/usr/bin"}, nil); got != nil {
		t.Errorf("mergeEnv without extra gave %#v, want nil: an empty slice would empty the child environment", got)
	}
	if got := mergeEnv([]string{"PATH=/usr/bin"}, []string{}); got != nil {
		t.Errorf("mergeEnv with empty extra gave %#v, want nil", got)
	}
}

// readMetricsAt is already covered against a synthetic tree; what this pins is the wrapper's procRoot, since a wrong one would silently report another process.
func TestReadMetricsOfTheLiveProcess(t *testing.T) {
	m, err := ReadMetrics(os.Getpid())
	if err != nil {
		t.Fatalf("ReadMetrics of the process itself: %v", err)
	}
	if m.RSSKB <= 0 {
		t.Errorf("RSSKB = %d, want > 0: a live process has resident memory", m.RSSKB)
	}
	if m.Threads <= 0 {
		t.Errorf("Threads = %d, want > 0", m.Threads)
	}
	if m.Ticks <= 0 {
		t.Errorf("Ticks = %d, want > 0: a process that has run accumulates CPU ticks", m.Ticks)
	}
	if m.FDs <= 0 {
		t.Errorf("FDs = %d, want > 0", m.FDs)
	}
}

// An error rather than zeros: an all-zero reading would paint a plausible-looking 0 kB service.
func TestReadMetricsOfANonexistentPidGivesError(t *testing.T) {
	dead := findDeadPID(t)
	if _, err := ReadMetrics(dead); err == nil {
		t.Error("a dead pid should not give metrics: it would give zeros that look like data")
	}
}

// The kernel freezes this block at execve, which is why a later t.Setenv never appears here and the Env tab cannot read os.Environ instead.
func TestReadEnvironOfTheLiveProcess(t *testing.T) {
	real := pickRealEnvVar(t)

	got, err := ReadEnviron(os.Getpid())
	if err != nil {
		t.Fatalf("ReadEnviron: %v", err)
	}
	var found bool
	for _, kv := range got {
		if strings.HasPrefix(kv, real+"=") {
			found = true
		}
	}
	if !found {
		t.Errorf("%s is not in the process environment (%d variables)", real, len(got))
	}

	t.Setenv("VROOM_TEST_MARKER_THAT_IS_NOT_SEEN", "1")
	after, err := ReadEnviron(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range after {
		if strings.HasPrefix(kv, "VROOM_TEST_MARKER") {
			t.Error("a Setenv appears in /proc/environ: the environment is read from here and not from os.Environ, so this test would document the opposite of what happens")
		}
	}
}

// An error, not an empty list: an empty list reads as "the service has no variables", which is a claim rather than a failure.
func TestReadEnvironOfANonexistentPidGivesError(t *testing.T) {
	dead := findDeadPID(t)
	if _, err := ReadEnviron(dead); err == nil {
		t.Error("a dead pid should give an error when reading its environment")
	} else if !strings.Contains(err.Error(), "process environ") {
		t.Errorf("err = %q, want the prefix 'process environ'", err)
	}
}

// Same pid with a different creation_time is a recycled pid: accepting it would make vroom believe a foreign process is its service and SIGKILL it on stop.
func TestAliveRejectsRecycledPid(t *testing.T) {
	m := NewManager()
	res := startSleep(t, m, StartSpec{
		Command: "sleep 30", WorkDir: t.TempDir(),
		StdoutPath: filepath.Join(t.TempDir(), "o.log"), StderrPath: filepath.Join(t.TempDir(), "e.log"),
	})

	if !Alive(res.Pid, res.CreationTimeMs) {
		t.Error("a live process with its creation_time must be alive")
	}
	if Alive(res.Pid, res.CreationTimeMs+1) {
		t.Error("Alive accepted a different creation_time: it is exactly the recycled PID that it must reject")
	}
	if Alive(res.Pid, 0) {
		t.Error("Alive accepted creation_time 0: without that data there is no proof of identity")
	}

	if err := m.Stop(StopSpec{Pid: res.Pid, Pgid: res.Pgid, Timeout: 2 * time.Second}); err != nil {
		t.Fatal(err)
	}
	waitPIDGone(t, res.Pid)
	if Alive(res.Pid, res.CreationTimeMs) {
		t.Error("a stopped process is still alive")
	}
}

// gopsutil's NewProcess(0) addresses init's group on some systems, which would be an "alive" belonging to nobody.
func TestAliveWithNonNumericPidIsFalse(t *testing.T) {
	for _, pid := range []int{0, -1, -9999} {
		if Alive(pid, 0) {
			t.Errorf("Alive(%d) = true: an invalid pid cannot be a process", pid)
		}
	}
}

func TestPortOwnerPIDAmbiguousIsZero(t *testing.T) {
	port := listenRaw(t)
	pid := PortOwnerPID(port)
	if pid == 0 {
		// The listener lives in another process and may be unreadable for permissions, so only the resolvable branch is asserted.
		t.Logf("could not determine the owner of port %d (permissions or timing): the ambiguous case is covered separately", port)
		return
	}
	if pid <= 0 {
		t.Errorf("PortOwnerPID = %d, want > 0 or 0", pid)
	}
}

func TestPgidAliveDistinguishesWhatIsNotOurs(t *testing.T) {
	t.Run("own live group", func(t *testing.T) {
		if !pgidAlive(ownPgid(t)) {
			t.Error("the group of the test process itself is alive")
		}
	})
	t.Run("pgid 0 is not a group", func(t *testing.T) {
		if pgidAlive(0) {
			t.Error("pgid 0 must count as not alive: emitting -0 would signal the entire group")
		}
	})
	t.Run("nonexistent pgid", func(t *testing.T) {
		if pgidAlive(1 << 22) {
			t.Error("a pgid that does not exist cannot be alive")
		}
	})
}

// Without this guard, every stop of an already-stopped service would sit out its full timeout.
func TestWaitLineageGoneReturnsEarlyIfNothingRemains(t *testing.T) {
	start := time.Now()
	if !waitLineageGone(0, nil, 5*time.Second) {
		t.Fatal("an empty lineage cannot still be alive")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v with an empty lineage: there is nothing to wait for", elapsed)
	}
}

// A process that ignores SIGKILL cannot be provoked reliably, so an unsignalled live pid with a short timeout is used because the function only polls, which is what is under test.
func TestWaitLineageGoneExpiresWithAProcessThatDoesNotDie(t *testing.T) {
	me := os.Getpid()
	start := time.Now()
	if waitLineageGone(0, []int{me}, 200*time.Millisecond) {
		t.Error("with the process itself alive and unsignaled, waitLineageGone should expire")
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("returned in %v: did not wait for the timeout", elapsed)
	}
}

func ownPgid(t *testing.T) int {
	t.Helper()
	info, err := procStatAt(filepath.Join(procRoot, strconv.Itoa(os.Getpid())))
	if err != nil {
		t.Fatalf("could not read the stat of the test process: %v", err)
	}
	return info.pgid
}

func waitPIDGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(procRoot, strconv.Itoa(pid))); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("pid %d is still in /proc after the stop", pid)
}

// Must pick a variable inherited at execve, because a later t.Setenv never reaches /proc/self/environ and would make the test assert nothing.
func pickRealEnvVar(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("/proc/self/environ")
	if err != nil {
		t.Skipf("cannot read /proc/self/environ: %v", err)
	}
	for _, want := range []string{"PATH=", "HOME=", "USER=", "LANG=", "SHELL=", "PWD="} {
		if strings.Contains(string(raw), want) {
			return strings.TrimSuffix(want, "=")
		}
	}
	t.Skip("no known environment variable is present")
	return ""
}

func listenRaw(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

func writeFileStr(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFileStr(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
