package process

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	gopsnet "github.com/shirou/gopsutil/v3/net"
)

// Each of these consults something outside the process, so the lookup was injected rather than the check relaxed: the verdict stays, only the source of the datum moves.

// EPERM and ESRCH cannot both be provoked without a second user on the machine, so the whole errno map is pinned here as a table.
func TestGroupExistsTranslatesEachErrno(t *testing.T) {
	cases := []struct {
		err     error
		want    bool
		because string
	}{
		{nil, true, "kill with success: the group exists and is queryable"},
		{syscall.ESRCH, false, "ESRCH: there is no process in that group"},
		{syscall.EPERM, true, "EPERM: the group exists, but belongs to another user. Saying false " +
			"would make Stop believe there is nothing left to do"},
		{syscall.EINVAL, false, "an errno that is not a verdict about the group does not translate to " +
			"'exists': the query failed, and nothing is asserted"},
		{errors.New("other"), false, "an error that is not an errno either"},
	}
	for _, c := range cases {
		if got := groupExists(c.err); got != c.want {
			t.Errorf("groupExists(%v) = %v, want %v: %s", c.err, got, c.want, c.because)
		}
	}

	if pgidAlive(0) {
		t.Error("pgidAlive(0) = true: it would ask for vroom's group, which always exists")
	}
	if pgidAlive(-1) {
		t.Error("pgidAlive(-1) = true: a negative pgid is not a group")
	}
}

func TestAliveWithDistinguishesTheThreeVerdicts(t *testing.T) {
	const saved = 1_700_000_000_000

	if !aliveWith(func(int) (int64, error) { return saved, nil }, 42, saved) {
		t.Error("aliveWith = false with creation_time intact: a running service would be declared stopped")
	}
	if aliveWith(func(int) (int64, error) { return saved + 1, nil }, 42, saved) {
		t.Error("aliveWith = true with a different creation_time: the PID was recycled and is not " +
			"our service")
	}
	if aliveWith(func(int) (int64, error) { return 0, syscall.ESRCH }, 42, saved) {
		t.Error("aliveWith = true for a PID that does not exist")
	}
	if aliveWith(func(int) (int64, error) { return 0, errors.New("it left while looking at it") }, 42, saved) {
		t.Error("aliveWith = true without being able to ask: asserting that something is alive without having seen it is the " +
			"worst possible error here")
	}
}

// nil and not an empty slice is what makes it explicit: nil is "could not ask", [] is "asked and nobody owns it".
func TestOwnersWithTreatsSameNotSeeAndNotKnow(t *testing.T) {
	const port = 4321

	listening := func(status string, pid int32, p uint32) gopsnet.ConnectionStat {
		return gopsnet.ConnectionStat{Status: status, Pid: pid, Laddr: gopsnet.Addr{Port: p}}
	}
	conns := []gopsnet.ConnectionStat{
		listening("LISTEN", 100, port),
		listening("LISTEN", 200, port+1), // another port
		listening("ESTABLISHED", 300, port),
		listening("LISTEN", 0, port), // without owner: does not count
	}

	got := ownersWith(func() ([]gopsnet.ConnectionStat, error) { return conns, nil }, port)
	if len(got) != 1 || got[0] != 100 {
		t.Errorf("ownersWith = %v, want [100]: only LISTEN, only that port and only with pid", got)
	}

	if g := ownersWith(func() ([]gopsnet.ConnectionStat, error) { return nil, errors.New("proc restricted") },
		port); g != nil {
		t.Errorf("ownersWith with error = %v, want nil: without ownership proof nothing is killed", g)
	}
	if g := ownersWith(func() ([]gopsnet.ConnectionStat, error) { return nil, nil }, port); g != nil {
		t.Errorf("ownersWith without connections = %v, want nil", g)
	}
}

func TestDescendantsFromEndsWithACycle(t *testing.T) {
	snap := map[int]procInfo{
		1: {ppid: 2},
		2: {ppid: 1},
		3: {ppid: 1},
	}

	got := descendantsFrom(snap, 1)
	// From 1 the children are 2 and 3; 2's child is 1, already seen, so the walk stops and the result is [2, 3].
	if len(got) != 2 {
		t.Fatalf("descendantsFrom = %v, want [2 3]: the cycle must cut at the already seen node", got)
	}
	for _, pid := range got {
		if pid != 2 && pid != 3 {
			t.Errorf("descendantsFrom = %v, want only the real descendants of 1", got)
		}
	}
}

// An empty directory as the /proc root is exactly what a process in a container without proc mounted sees.
func TestCaptureLineDegradesWithoutProcs(t *testing.T) {
	empty := t.TempDir() // exists and is empty: ReadDir works, there are no processes

	cases := []struct {
		name string
		spec StopSpec
	}{
		{"without pid or pgid", StopSpec{}},
		{"without snapshot", StopSpec{Pid: 12345, Pgid: 12345}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root, lineage := captureLineageWith(c.spec, empty)
			if root != 0 || lineage != nil {
				t.Errorf("captureLineageWith = (%d, %v), want (0, nil): without lineage proof no one is "+
					"pointed at", root, lineage)
			}
		})
	}

	// A root that is not a directory: ReadDir fails with ENOTDIR, the other path to the same error.
	file := filepath.Join(t.TempDir(), "not-proc")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if root, lineage := captureLineageWith(StopSpec{Pid: 1, Pgid: 1}, file); root != 0 || lineage != nil {
		t.Errorf("captureLineageWith with an unreadable root = (%d, %v), want (0, nil)", root, lineage)
	}
}

func TestReserveWithDoesNotMarkAPortThatCouldNotBeReleased(t *testing.T) {
	// An already-closed listener: Close on it fails with "use of closed".
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	// The set is snapshotted first because the exact port attempted depends on what the rest of the suite already reserved; what matters is that the set does not grow.
	before := reservedPortsSnapshot()

	_, err = reserveWith(func(string, string) (net.Listener, error) { return closed, nil })
	if err == nil {
		t.Fatal("reserveWith = nil with a listener that cannot be closed: the port may remain " +
			"occupied by it")
	}
	if !strings.Contains(err.Error(), "could not release the reserved port") {
		t.Errorf("err = %q, want it to say the port could not be returned", err)
	}
	after := reservedPortsSnapshot()
	for p := range after {
		if !before[p] {
			t.Errorf("port %d was marked as reserved after a failed close: the range "+
				"would shrink by one slot per failure, without explanation", p)
		}
	}
}

// MEASURED: a fake pgrep on the PATH controls only the shape of its output, which is what this function promises to understand; the "another process exists" result comes from a real pid.
func TestPatternMatchToleratesAPgrepOutputThatIsNotJustPids(t *testing.T) {
	other := testProcesses(t, 1) // a real `sleep` with a recognizable name
	pid := strconv.Itoa(other[0])

	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"a blank line and a pid", "\n" + pid + "\n\n", true},
		{"a line that is not a number", "pgrep: something weird\n" + pid + "\n", true},
		{"only garbage", "this is not a pid\n", false},
		{"nothing, but with exit 0", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withFakePgrep(t, c.output)
			if got := PatternMatch(testPattern); got != c.want {
				t.Errorf("PatternMatch with output %q = %v, want %v", c.output, got, c.want)
			}
		})
	}
}

// MEASURED: the last-resort killer is fuser -k, so a fake fuser that kills nothing stands in for a container without fuser; the port outlives the wait and a warning is owed.
func TestKillPortHolderWarnsWhenThePortIsNotFreed(t *testing.T) {
	// An open port with a known owner, which is what lets killPortHolderWith reach the last resort instead of refusing outright.
	ln := listenOn(t)
	port := portOf(t, ln)
	pid := os.Getpid()

	var warnings []string
	withFakeFuser(t) // kills nothing
	killPortHolderWith(port, pid, []int{pid}, func(int) []int32 { return []int32{int32(pid)} },
		func(f string, a ...any) { warnings = append(warnings, fmt.Sprintf(f, a...)) })

	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one: without attributable owner there are two "+
			"renunciations, and with own owner but occupied port there is one", warnings)
	}
	if !strings.Contains(warnings[0], "still occupied") {
		t.Errorf("warning = %q, want it to say the port is still occupied after the kill", warnings[0])
	}
}

// It does not have to match anything real: what is controlled is what the fake prints.
const testPattern = "vroom-test-pattern-that-does-not-exist"

// It exits 0 on purpose even with no matches, which is the rare case that lets an empty output reach the parser.
func withFakePgrep(t *testing.T, output string) {
	t.Helper()
	dir := t.TempDir()
	// The output goes in a file the fake cats: a heredoc looked nicer but cat reads stdin, and exec.Command gives the child /dev/null, not the heredoc.
	if err := os.WriteFile(filepath.Join(dir, "output.txt"), []byte(output), 0o644); err != nil {
		t.Fatal(err)
	}
	// PATH is prepended rather than replaced: with a single-directory PATH the script itself cannot find cat, and that is exactly how a real pgrep fails.
	if err := os.WriteFile(filepath.Join(dir, "pgrep"),
		[]byte("#!/bin/sh\ncat "+filepath.Join(dir, "output.txt")+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func withFakeFuser(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fuser"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func testProcesses(t *testing.T, n int) []int {
	t.Helper()
	var pids []int
	for range n {
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pids = append(pids, cmd.Process.Pid)
		// No Wait: the test cleanup reaps them.
		t.Cleanup(func() { _ = cmd.Process.Kill() })
	}
	return pids
}

func reservedPortsSnapshot() map[int]bool {
	reserveMu.Lock()
	defer reserveMu.Unlock()
	out := make(map[int]bool, len(reservedPorts))
	for p, v := range reservedPorts {
		out[p] = v
	}
	return out
}

func listenOn(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func portOf(t *testing.T, ln net.Listener) int {
	t.Helper()
	return ln.Addr().(*net.TCPAddr).Port
}

// MEASURED: the surviving-SIGKILL case cannot be built here (setpriv --reuid gives EPERM and unshare -U leaves children with the same uid), so what is asserted is WHEN the ladder warns.
func TestTheLadderWarningDependsOnWhetherThereWasRootAndWhetherItDidNotDie(t *testing.T) {
	cases := []struct {
		name        string
		root        int
		didNotDie   bool
		wantWarning bool
		because     string
	}{
		{"there was root and someone survived", 100, true, true, "the ladder was exhausted: it must be said"},
		{"there was root and everything died", 100, false, false, "the service stopped well: a warning here " +
			"would be noise"},
		{"there was no root", 0, true, false, "without root there was nothing to stop and `terminate` is not even " +
			"called; warning about an empty lineage would be lying"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var warnings []string
			StopSpec{Warn: func(f string, a ...any) {
				warnings = append(warnings, fmt.Sprintf(f, a...))
			}}.warnIfExhausted(c.root, []int{c.root, 200}, c.didNotDie)

			if c.wantWarning {
				if len(warnings) != 1 {
					t.Fatalf("warnings = %v, want exactly one", warnings)
				}
				if !strings.Contains(warnings[0], "SIGKILL") {
					t.Errorf("warning = %q, want it to name SIGKILL: if the ladder was exhausted, "+
						"the user has to know it was not courtesy's fault", warnings[0])
				}
				if !strings.Contains(warnings[0], strconv.Itoa(c.root)) {
					t.Errorf("warning = %q, want the root's pid: without it the user cannot search "+
						"for the resisting process", warnings[0])
				}
				return
			}
			if len(warnings) != 0 {
				t.Errorf("warnings = %v, want none: %s", warnings, c.because)
			}
		})
	}
}

// MEASURED: a kernel thread fits exactly - /proc reports state R so lineageRunning counts it alive, while a normal user gets EPERM on SIGKILL.
func TestTerminateReturnsFalseWithAProcessThatCannotBeKilled(t *testing.T) {
	kthread := kernelThread()
	if kthread == 0 {
		t.Skip("this machine has no readable kernel thread in /proc")
	}
	// If signalling ever succeeds the scenario no longer applies and terminate would go back to returning true.
	if err := syscall.Kill(kthread, syscall.SIGKILL); err == nil {
		t.Skip("this user can kill kernel threads: the test scenario no longer applies")
	}

	root := launch(t, "sleep", "30")

	if terminate(root, 0, []int{root, kthread}, 200*time.Millisecond) {
		t.Error("terminate = true with an unkillable process in the lineage: the ladder was exhausted and " +
			"`Stop` would not warn about anything")
	}
}

// Picks a kernel thread: a live state with ppid 0 or 2, or 0 if none is readable.
func kernelThread() int {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid < 2 {
			continue
		}
		info, err := procStatAt(filepath.Join(procRoot, e.Name()))
		if err != nil || info.ppid > 2 || !info.running() {
			continue
		}
		return pid
	}
	return 0
}

func launch(t *testing.T, name string, args ...string) int {
	t.Helper()
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}
