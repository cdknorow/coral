//go:build !windows

package agentprio

import (
	"os"
	"os/exec"
	"runtime"
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
// maxNice is the highest nice a process can have: 20 on macOS and the BSDs, 19 on Linux.
func maxNice() int {
	if runtime.GOOS == "linux" {
		return 19
	}
	return 20
}

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
	if niceOf(t, os.Getpid()) < Nice {
		assert.GreaterOrEqual(t, n, 3, "the pane and its children")
	} else {
		assert.Zero(t, n, "already at Nice (tests run niced): nothing changed, nothing counted (#4129)")
	}
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
	// A member nicer than what ApplyGroup sets, relative to it (#4136): a fixed 15 is below the
	// members' own nice when the tests run niced (epoch, or inside an agent), and lowering nice
	// takes privileges. At the highest nice nothing can be nicer, so there is nothing to check.
	target := min(want+5, maxNice())
	if target <= want {
		t.Skipf("the tests run at nice %d: no member can be nicer than what ApplyGroup sets", want)
	}
	require.NoError(t, syscall.Setpriority(syscall.PRIO_PROCESS, nicer, target))
	ApplyGroup(pid)
	for _, p := range members {
		if p == nicer {
			assert.Equal(t, target, niceOf(t, p), "the member already nicer keeps its nice")
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
	wantN := 1
	if self >= Nice {
		wantN = 0 // already at Nice: lowered means changed (#4129)
	}
	assert.Equal(t, wantN, lowerTree([]proc{{pid: pid, ppid: ppid}}), "the same parent: lowered")
	assert.Equal(t, max(self, Nice), niceOf(t, pid))
}

// #4129: ApplyTrees lowers several panes' trees from one process snapshot, and counts only the
// processes whose priority it changed: a second pass over the same panes changes nothing.
func TestApplyTreesCountsOnlyWhatItChanged(t *testing.T) {
	reset(t)
	self := niceOf(t, os.Getpid())
	a := startSleep(t, exec.Command("sleep", "30"))
	b := startSleep(t, exec.Command("sleep", "30"))
	n := ApplyTrees([]int{a, b})
	if self < Nice {
		assert.Equal(t, 2, n, "both panes lowered")
	} else {
		assert.Zero(t, n, "already at Nice")
	}
	for _, p := range []int{a, b} {
		assert.Equal(t, max(self, Nice), niceOf(t, p))
	}
	assert.Zero(t, ApplyTrees([]int{a, b}), "a second pass changes nothing and says so")
	assert.Zero(t, ApplyTrees(nil))
}
