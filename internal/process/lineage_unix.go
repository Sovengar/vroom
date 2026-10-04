//go:build unix

package process

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// procInfo carries the pgid because a descendant that called setsid no longer shares it, which is exactly what makes kill(-pgid) miss it.
type procInfo struct {
	pid   int
	ppid  int
	pgid  int
	state string
}

// A zombie has exited: its /proc entry lingers until the parent reaps it, so it must not read as alive.
func (p procInfo) running() bool { return p.pid > 0 && p.state != "Z" }

// comm may contain spaces and parentheses ("Web Content (tab)"), so the fields are recovered from the last ')'.
func procStatAt(dir string) (procInfo, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return procInfo{}, err
	}
	s := string(raw)
	lparen := strings.IndexByte(s, '(')
	rparen := strings.LastIndexByte(s, ')')
	if lparen < 0 || rparen <= lparen || rparen+2 > len(s) {
		return procInfo{}, fmt.Errorf("stat malformado en %s", dir)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(s[:lparen])) // stat field 1
	if err != nil {
		return procInfo{}, fmt.Errorf("pid inválido en %s: %w", dir, err)
	}
	f := strings.Fields(s[rparen+2:])
	if len(f) < 3 {
		return procInfo{}, fmt.Errorf("stat incompleto en %s", dir)
	}
	ppid, err := strconv.Atoi(f[1]) // stat field 4
	if err != nil {
		return procInfo{}, fmt.Errorf("ppid inválido en %s: %w", dir, err)
	}
	pgid, err := strconv.Atoi(f[2]) // stat field 5 (pgrp)
	if err != nil {
		return procInfo{}, fmt.Errorf("pgrp inválido en %s: %w", dir, err)
	}
	return procInfo{pid: pid, state: f[0], ppid: ppid, pgid: pgid}, nil
}

// The /proc root is injected so the tree can be read from synthetic fixtures instead of the real /proc.
func procSnapshotAt(root string) (map[int]procInfo, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("proc snapshot: %w", err)
	}
	out := make(map[int]procInfo, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() {
			continue
		}
		info, err := procStatAt(filepath.Join(root, e.Name()))
		if err != nil {
			continue // the process is dying
		}
		out[pid] = info
	}
	return out, nil
}

func descendantsAt(root string, pid int) []int {
	snap, err := procSnapshotAt(root)
	if err != nil {
		return nil
	}
	return descendantsFrom(snap, pid)
}

func descendantsFrom(snap map[int]procInfo, pid int) []int {
	children := make(map[int][]int, len(snap))
	for p, info := range snap {
		if info.ppid == p {
			continue // hangs off nobody
		}
		children[info.ppid] = append(children[info.ppid], p)
	}

	seen := map[int]bool{pid: true}
	queue := []int{pid}
	out := make([]int, 0, 8)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, kid := range children[cur] {
			// seen guards against a ppid cycle, impossible in a real /proc but not discardable from a bare map: without it a hung Stop wedges the session.
			if seen[kid] {
				continue
			}
			seen[kid] = true
			out = append(out, kid)
			queue = append(queue, kid)
		}
	}
	sort.Ints(out)
	return out
}

// Root is the recorded pid only while it is alive and still in the recorded group, otherwise the group leader, whose pid equals the pgid by construction of setsid.
func captureLineage(spec StopSpec) (int, []int) {
	return captureLineageWith(spec, procRoot)
}

// The root is injected so the (0, nil) degradation is testable with an empty directory; it is a contract, not a detail, because Stop then falls back to signalling the group alone and must not hit the wrong service.
func captureLineageWith(spec StopSpec, root string) (int, []int) {
	if spec.Pgid <= 0 && spec.Pid <= 0 {
		return 0, nil
	}
	snap, err := procSnapshotAt(root)
	if err != nil {
		return 0, nil
	}

	root0 := spec.Pid
	if info, ok := snap[root0]; !ok || (spec.Pgid > 0 && info.pgid != spec.Pgid) {
		if leader, ok := snap[spec.Pgid]; !ok || leader.pgid != spec.Pgid {
			return 0, nil
		}
		root0 = spec.Pgid
	}

	lineage := descendantsFrom(snap, root0)
	return root0, append([]int{root0}, lineage...)
}

// Zombies count as gone: they no longer hold a port.
func lineageRunning(lineage []int) bool {
	for _, pid := range lineage {
		if pid <= 0 {
			continue
		}
		info, err := procStatAt(filepath.Join(procRoot, strconv.Itoa(pid)))
		if err == nil && info.running() {
			return true
		}
	}
	return false
}
