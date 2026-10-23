//go:build !windows

package ptymanager

import (
	"os/exec"
	"testing"

	"github.com/shirou/gopsutil/v4/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/tmux"
)

// startMember starts a process that outlives the test body and returns it as a member.
func startMember(t *testing.T) member {
	t.Helper()
	c := exec.Command("sleep", "600")
	require.NoError(t, c.Start())
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	p, err := process.NewProcess(int32(c.Process.Pid))
	require.NoError(t, err)
	created, err := p.CreateTime()
	require.NoError(t, err)
	return member{c.Process.Pid, created}
}

// #6584: the sampler judges what a set held before it read the process table, by that table. A member a terminal records while the table is read
// (Terminate, or the guardian's merge) is newer than the table, so the table lacking it does not mean it ended: it stays, and a later kill finds it.
func TestSamplerKeepsAMemberRecordedWhileTheTableWasRead(t *testing.T) {
	old := startMember(t) // in the set before the look, still running
	d := &descendantSampler{pty: map[member]*seen{}, clients: map[*tmux.Client]struct{}{}, panes: map[member]*seen{}}
	set := &seen{}
	set.add([]member{old})
	d.pty[member{pid: 424242, created: 1}] = set // a terminal whose shell is gone: only what was remembered is in play

	var newer member
	d.afterSnap = func() { // the table is read; now a process starts and is recorded, as the kill paths and the guardian do
		newer = startMember(t)
		set.add([]member{newer})
	}
	d.sample()
	assert.ElementsMatch(t, []member{old, newer}, set.all(), "the member recorded after the table was read is kept (and the one that was running is too)")
}

// What the set held before the look and has since ended is still forgotten.
func TestSamplerStillForgetsAMemberThatEndedBeforeTheLook(t *testing.T) {
	gone := exec.Command("sleep", "600")
	require.NoError(t, gone.Start())
	p, err := process.NewProcess(int32(gone.Process.Pid))
	require.NoError(t, err)
	created, err := p.CreateTime()
	require.NoError(t, err)
	goneM := member{gone.Process.Pid, created}
	require.NoError(t, gone.Process.Kill())
	_ = gone.Wait()

	live := startMember(t)
	d := &descendantSampler{pty: map[member]*seen{}, clients: map[*tmux.Client]struct{}{}, panes: map[member]*seen{}}
	set := &seen{}
	set.add([]member{goneM, live})
	d.pty[member{pid: 424242, created: 1}] = set
	d.sample()
	assert.Equal(t, []member{live}, set.all(), "what ended is dropped, what runs stays")
}

// pruneAmong judges only the candidates it is given.
func TestPruneAmongLeavesWhatItWasNotGiven(t *testing.T) {
	a, b := member{1001, 1}, member{1002, 1}
	s := &seen{}
	s.add([]member{a, b})
	s.pruneAmong([]member{a}, func(member) bool { return false })
	assert.Equal(t, []member{b}, s.all(), "a was judged and ended; b was not looked at")
}
