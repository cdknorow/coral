//go:build !windows

package agentprio

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// niceOf reads a process's niceness the way the task's check does: ps -o ni.
func niceOf(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("ps", "-o", "ni=", "-p", strconv.Itoa(pid)).Output()
	require.NoError(t, err)
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	require.NoError(t, err)
	return n
}

// startSleep starts a process the test owns and kills it at the end.
func startSleep(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	return cmd.Process.Pid
}

// With agents at low priority, an agent process is at nice 10 and this
// process (standing in for the server) keeps its own; with normal, nothing
// changes (#3437).
func TestApplyLowersOnlyTheAgent(t *testing.T) {
	reset(t)
	self := niceOf(t, os.Getpid())
	pid := startSleep(t, exec.Command("sleep", "30"))
	Apply(pid)
	assert.Equal(t, max(self, Nice), niceOf(t, pid), "the agent runs at nice 10")
	assert.Equal(t, self, niceOf(t, os.Getpid()), "the server keeps its priority")

	SetSetting(Normal)
	other := startSleep(t, exec.Command("sleep", "30"))
	Apply(other)
	assert.Equal(t, self, niceOf(t, other), "normal leaves agents at the server's priority")
}

// A workflow step's process group, and everything a pane already started
// (an older tmux server's), are lowered too.
func TestApplyGroupAndTree(t *testing.T) {
	reset(t)
	want := max(niceOf(t, os.Getpid()), Nice)
	step := exec.Command("sh", "-c", "sleep 30 & sleep 30; wait")
	step.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pid := startSleep(t, step)
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	time.Sleep(200 * time.Millisecond)
	ApplyGroup(pid)
	for _, p := range descendants(pid) {
		assert.Equal(t, want, niceOf(t, p.pid), "pid %d of the step's group", p.pid)
	}

	pane := exec.Command("sh", "-c", "sleep 30 & sleep 30; wait")
	pane.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	ppid := startSleep(t, pane)
	t.Cleanup(func() { _ = syscall.Kill(-ppid, syscall.SIGKILL) })
	time.Sleep(200 * time.Millisecond)
	n := ApplyTree(ppid)
	assert.GreaterOrEqual(t, n, 3, "the pane and its children")
	for _, p := range descendants(ppid) {
		assert.Equal(t, want, niceOf(t, p.pid), "pid %d under the pane", p.pid)
	}
}

// ApplyGroup lowers each member on its own: a member already nicer than 10
// keeps its niceness instead of being set back to 10.
func TestApplyGroupNeverRaisesAMember(t *testing.T) {
	reset(t)
	want := max(niceOf(t, os.Getpid()), Nice)
	step := exec.Command("sh", "-c", "sleep 30 & sleep 30; wait")
	step.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pid := startSleep(t, step)
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	require.Eventually(t, func() bool { return len(groupMembers(pid)) >= 3 }, 5*time.Second, 50*time.Millisecond)
	members := groupMembers(pid)
	nicer := members[len(members)-1]
	require.NoError(t, syscall.Setpriority(syscall.PRIO_PROCESS, nicer, 15))
	ApplyGroup(pid)
	for _, p := range members {
		if p == nicer {
			assert.Equal(t, 15, niceOf(t, p), "the member already at 15 keeps it")
		} else {
			assert.Equal(t, want, niceOf(t, p), "pid %d of the group", p)
		}
	}
}

// A process whose parent no longer matches the snapshot (its pid was reused
// after the snapshot) is skipped; one whose parent matches is lowered.
func TestApplyTreeSkipsAReusedPid(t *testing.T) {
	reset(t)
	self := niceOf(t, os.Getpid())
	pid := startSleep(t, exec.Command("sleep", "30"))
	ppid, ok := parentOf(pid)
	require.True(t, ok)
	require.Equal(t, os.Getpid(), ppid, "parentOf reads the live parent")

	assert.Equal(t, 0, lowerTree([]proc{{pid: pid, ppid: ppid + 1}}), "a different parent: skipped")
	assert.Equal(t, self, niceOf(t, pid))
	assert.Equal(t, 1, lowerTree([]proc{{pid: pid, ppid: ppid}}), "the same parent: lowered")
	assert.Equal(t, max(self, Nice), niceOf(t, pid))
}
