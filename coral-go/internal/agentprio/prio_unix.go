//go:build !windows

package agentprio

import (
	"github.com/subgentic/coral/internal/executil"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// lower sets pid's niceness to Nice. Raising niceness needs no privilege; a
// process already nicer than Nice is left alone.
func lower(pid int) error {
	_, err := lowerChanged(pid)
	return err
}

// lowerChanged is lower, reporting whether pid's niceness was changed (false when it already
// ran at Nice or nicer).
func lowerChanged(pid int) (bool, error) {
	cur, err := syscall.Getpriority(syscall.PRIO_PROCESS, pid)
	if err == nil && niceness(cur) >= Nice {
		return false, nil
	}
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, pid, Nice); err != nil {
		return false, err
	}
	return true, nil
}

// lowerGroup lowers each member of process group pgid on its own, so a member
// already nicer than Nice keeps its niceness (PRIO_PGRP would set them all to
// Nice, raising such a member when Coral runs as root). If the members can't
// be listed, the group leader is lowered.
func lowerGroup(pgid int) error {
	members := groupMembers(pgid)
	if len(members) == 0 {
		return lower(pgid)
	}
	var first error
	for _, p := range members {
		if err := lower(p); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// niceness converts getpriority's result to a nice value: Linux's raw syscall
// returns 20 - nice, other systems the nice value itself.
func niceness(v int) int { return rawToNice(v) }

// psTimeout bounds a ps call: a stuck ps must not stop the loop that asked (#3999).
var psTimeout = 5 * time.Second

// psPairs runs ps for every process and returns (pid, second column) pairs.
func psPairs(col string) [][2]int {
	out, err := executil.Output(psTimeout, "ps", "-A", "-o", "pid=,"+col+"=")
	if err != nil {
		return nil
	}
	var pairs [][2]int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		a, err1 := strconv.Atoi(f[0])
		b, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil {
			pairs = append(pairs, [2]int{a, b})
		}
	}
	return pairs
}

// groupMembers returns the processes in process group pgid, from ps.
func groupMembers(pgid int) []int {
	var members []int
	for _, p := range psPairs("pgid") {
		if p[1] == pgid {
			members = append(members, p[0])
		}
	}
	return members
}

// descendants returns pid and all its descendants, from ps, each with the
// parent it had in that snapshot (0 for pid itself if ps failed).
func descendants(pid int) []proc { return descendantsOf(pid) }

// descendantsOf is descendants for several roots from one ps snapshot (#4129: a ps per pane
// cost ~20 ms each at startup).
func descendantsOf(pids ...int) []proc {
	pairs := psPairs("ppid")
	if pairs == nil {
		out := make([]proc, 0, len(pids))
		for _, pid := range pids {
			out = append(out, proc{pid: pid})
		}
		return out
	}
	children := map[int][]int{}
	parent := map[int]int{}
	for _, p := range pairs {
		children[p[1]] = append(children[p[1]], p[0])
		parent[p[0]] = p[1]
	}
	all, queue := []proc{}, append([]int(nil), pids...)
	seen := map[int]bool{}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		all = append(all, proc{pid: p, ppid: parent[p]})
		queue = append(queue, children[p]...)
	}
	return all
}
