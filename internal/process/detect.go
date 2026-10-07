package process

import (
	"fmt"
	"net"
	"time"

	gopsnet "github.com/shirou/gopsutil/v3/net"
	gopsprocess "github.com/shirou/gopsutil/v3/process"
)

// gopsutil is pure Go and portable, unlike os.FindProcess, which succeeds on Linux even when the process does not exist.
func Alive(pid int, creationTimeMs int64) bool {
	return aliveWith(func(pid int) (int64, error) {
		p, err := gopsprocess.NewProcess(int32(pid))
		if err != nil {
			return 0, err
		}
		return p.CreateTime()
	}, pid, creationTimeMs)
}

// The lookup is injected because both of Alive's error branches are races; above all, "could not ask" must never answer true for a process that was live.
func aliveWith(creationTime func(int) (int64, error), pid int, creationTimeMs int64) bool {
	ct, err := creationTime(pid)
	if err != nil {
		return false // pid free, gone, or it died mid-lookup
	}
	return ct == creationTimeMs // mismatch means a recycled pid
}

func PortOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf(":%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Ambiguous and unknown are deliberately the same answer: whoever asks needs proof, not a candidate.
func PortOwnerPID(port int) int32 {
	owners := PortOwnerPIDs(port)
	if len(owners) == 1 {
		return owners[0]
	}
	return 0
}

// More than one owner means the port is shared (same number in IPv4 and IPv6, or two processes), so ownership is NOT proven.
func PortOwnerPIDs(port int) []int32 {
	return ownersWith(func() ([]gopsnet.ConnectionStat, error) {
		return gopsnet.ConnectionsPid("tcp", 0)
	}, port)
}

// Injected because this value decides whether vroom kills a process, and "no owner" has to mean the same as "could not ask".
func ownersWith(read func() ([]gopsnet.ConnectionStat, error), port int) []int32 {
	conns, err := read()
	if err != nil {
		return nil
	}
	var out []int32
	for _, c := range conns {
		if c.Status == "LISTEN" && c.Laddr.Port == uint32(port) && c.Pid > 0 {
			out = append(out, c.Pid)
		}
	}
	return out
}
