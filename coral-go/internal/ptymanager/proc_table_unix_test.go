//go:build !windows

package ptymanager

import (
	"os"
	"os/exec"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// perPIDSnap is the snapshot as it was taken before #6562: gopsutil's Processes(), each process asked for its parent (a kernel call each on
// macOS), and member times from CreateTime. The new one must say the same of the same processes.
func perPIDSnap(t *testing.T) (parent map[int]int, created map[int]int64) {
	t.Helper()
	procs, err := process.Processes()
	require.NoError(t, err)
	parent, created = map[int]int{}, map[int]int64{}
	for _, p := range procs {
		pp, err := p.Ppid()
		if err != nil {
			continue
		}
		c, err := p.CreateTime()
		if err != nil {
			continue
		}
		parent[int(p.Pid)], created[int(p.Pid)] = int(pp), c
	}
	return parent, created
}

func sortedPIDs(ms []member) []int {
	var out []int
	for _, m := range ms {
		out = append(out, m.pid)
	}
	sort.Ints(out)
	return out
}

// A stand-in tree: a shell with three children. The table scan finds the same processes, parents and start times as
// the per-pid snapshot did, and the tree computed from it is the same.
func TestProcTableMatchesPerPIDSnapshot(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 60 & sleep 60 & sh -c 'sleep 60' & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // a terminal has a group of its own; the tree never includes Coral's
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	var snap *procSnap
	require.Eventually(t, func() bool { // the children exist once the shell has forked them
		snap = takeSnap()
		n := 0
		for _, pp := range snap.parent {
			if pp == cmd.Process.Pid {
				n++
			}
		}
		return n >= 3
	}, 10*time.Second, 20*time.Millisecond)
	oldParent, oldCreated := perPIDSnap(t)

	compared := 0
	for pid, c := range oldCreated {
		if nc, ok := snap.started[pid]; ok { // a process that started or ended between the two looks is in one only
			compared++
			assert.Equal(t, c, nc, "start time of pid %d", pid)
			assert.Equal(t, oldParent[pid], snap.parent[pid], "parent of pid %d", pid)
		}
	}
	assert.Greater(t, compared, 10, "most of the table is in both")

	created, ok := oldCreated[cmd.Process.Pid]
	require.True(t, ok)
	root := member{cmd.Process.Pid, created}
	assert.True(t, snap.has(root))
	assert.False(t, snap.has(member{root.pid, created + 1}), "a pid whose start time differs is another process")
	assert.False(t, snap.has(member{0x7ffffff0, created}), "a pid that is not there")

	got := snap.tree(root, "")
	assert.GreaterOrEqual(t, len(got), 4, "the shell and its three children")
	for _, m := range got {
		assert.Equal(t, oldCreated[m.pid], m.created, "member %d carries the start time the per-pid way gave it", m.pid)
	}
	assert.Contains(t, sortedPIDs(got), root.pid)
	assert.NotContains(t, sortedPIDs(got), os.Getpid())
}

// What the sampler forgets by the snapshot is what it forgot by asking each process: a member that ended is dropped, a live one kept.
func TestPruneBySnapshotMatchesPruneByAsking(t *testing.T) {
	live := exec.Command("sleep", "60")
	require.NoError(t, live.Start())
	t.Cleanup(func() { _ = live.Process.Kill(); _ = live.Wait() })
	dead := exec.Command("sleep", "60")
	require.NoError(t, dead.Start())
	_, created := perPIDSnap(t)
	liveM, deadM := member{live.Process.Pid, created[live.Process.Pid]}, member{dead.Process.Pid, created[dead.Process.Pid]}
	require.NotZero(t, liveM.created)
	require.NotZero(t, deadM.created)
	require.NoError(t, dead.Process.Kill())
	_ = dead.Wait()

	for name, prune := range map[string]func(*seen){
		"asking":   func(s *seen) { s.prune() },
		"snapshot": func(s *seen) { s.pruneBy(takeSnap().has) },
	} {
		set := &seen{}
		set.add([]member{liveM, deadM})
		prune(set)
		assert.Equal(t, []member{liveM}, set.all(), name)
	}
}
