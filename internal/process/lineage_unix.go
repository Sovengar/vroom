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

// procInfo es la identidad mínima que necesita el linaje: quién es el padre
// y en qué process group vive el proceso. El pgid es precisamente lo que un
// descendiente que hizo setsid deja de compartir con su padre, y por eso
// kill(-pgid) no lo alcanza.
type procInfo struct {
	pid   int
	ppid  int
	pgid  int
	state string
}

// running reports whether the process is actually running. A zombie has
// exited: its /proc entry lingers until its parent reaps it, which must not
// be read as "still alive".
func (p procInfo) running() bool { return p.pid > 0 && p.state != "Z" }

// procStatAt parses /proc/<pid>/stat, where dir is the process directory.
//
// The comm field (2) can contain spaces and parentheses ("Web Content
// (tab)"), so the fields are recovered from the last ')'.
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
	pid, err := strconv.Atoi(strings.TrimSpace(s[:lparen])) // campo 1
	if err != nil {
		return procInfo{}, fmt.Errorf("pid inválido en %s: %w", dir, err)
	}
	f := strings.Fields(s[rparen+2:])
	if len(f) < 3 {
		return procInfo{}, fmt.Errorf("stat incompleto en %s", dir)
	}
	ppid, err := strconv.Atoi(f[1]) // campo 4
	if err != nil {
		return procInfo{}, fmt.Errorf("ppid inválido en %s: %w", dir, err)
	}
	pgid, err := strconv.Atoi(f[2]) // campo 5 (pgrp)
	if err != nil {
		return procInfo{}, fmt.Errorf("pgrp inválido en %s: %w", dir, err)
	}
	return procInfo{pid: pid, state: f[0], ppid: ppid, pgid: pgid}, nil
}

// procSnapshotAt reads the pid -> procInfo tree of a /proc root. Injecting
// the root is what makes this testable with synthetic fixtures instead of
// the real /proc.
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
			continue // el proceso se está muriendo
		}
		out[pid] = info
	}
	return out, nil
}

// descendantsAt returns the transitive descendants of pid, sorted ascending.
// Processes from other lineages and init are excluded.
//
// It must be captured BEFORE signalling anything: once the root dies its
// descendants are reparented to init and the relationship is lost.
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
			continue // padre de sí mismo: no cuelga de nadie
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

// captureLineage resolves the root of the service lineage and every pid
// below it, as seen in /proc at this instant.
//
// root resolution order: the recorded pid when it is still alive and still
// in the recorded process group; otherwise the process group leader, whose
// pid equals the pgid by construction of setsid. Returns (0, nil) when the
// process tree cannot be read or the service is not running: Stop then
// degrades to signalling the group alone, which is the pre-existing
// behaviour.
func captureLineage(spec StopSpec) (int, []int) {
	if spec.Pgid <= 0 && spec.Pid <= 0 {
		return 0, nil
	}
	snap, err := procSnapshotAt(procRoot)
	if err != nil {
		return 0, nil
	}

	root := spec.Pid
	if info, ok := snap[root]; !ok || (spec.Pgid > 0 && info.pgid != spec.Pgid) {
		if leader, ok := snap[spec.Pgid]; !ok || leader.pgid != spec.Pgid {
			return 0, nil
		}
		root = spec.Pgid
	}

	lineage := descendantsFrom(snap, root)
	return root, append([]int{root}, lineage...)
}

// lineageRunning reports whether any pid of the captured lineage is still
// running. Zombies count as gone: they no longer hold a port.
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
