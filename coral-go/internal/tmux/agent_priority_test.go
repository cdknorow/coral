//go:build !windows

package tmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/agentprio"
)

func niceOf(t *testing.T, pid string) int {
	t.Helper()
	out, err := exec.Command("ps", "-o", "ni=", "-p", strings.TrimSpace(pid)).Output()
	require.NoError(t, err)
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	require.NoError(t, err)
	return n
}

// A new agent pane, and so everything started in it, runs at nice 10; the tmux
// server keeps normal priority (Coral's own capture and send-keys go through
// it). With agent_priority normal a new pane isn't lowered (#3437).
func TestNewAgentPanesRunAtLowPriority(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	// Relative to this process (a run under nice -n 10 starts there).
	base := niceOf(t, strconv.Itoa(os.Getpid()))
	low := max(base, agentprio.Nice)
	agentprio.SetSetting(agentprio.Low)
	t.Cleanup(func() { agentprio.SetSetting(agentprio.Low) })

	require.NoError(t, c.NewSession(ctx, "claude-prio-low", t.TempDir()))
	time.Sleep(2 * agentPriorityRecheck)
	pid, err := c.DisplayMessage(ctx, "claude-prio-low", "#{pane_pid}")
	require.NoError(t, err)
	assert.Equal(t, low, niceOf(t, pid), "the agent pane")
	server, err := c.DisplayMessage(ctx, "claude-prio-low", "#{pid}")
	require.NoError(t, err)
	assert.Equal(t, base, niceOf(t, server), "the tmux server")

	agentprio.SetSetting(agentprio.Normal)
	require.NoError(t, c.NewSession(ctx, "claude-prio-normal", t.TempDir()))
	time.Sleep(2 * agentPriorityRecheck)
	pid2, err := c.DisplayMessage(ctx, "claude-prio-normal", "#{pane_pid}")
	require.NoError(t, err)
	assert.Equal(t, base, niceOf(t, pid2), "normal leaves the pane alone")

	// An already-running pane (an older Coral's) is lowered once at startup.
	agentprio.SetSetting(agentprio.Low)
	assert.GreaterOrEqual(t, c.LowerExistingPanes(ctx), 1)
	assert.Equal(t, low, niceOf(t, pid2), "lowered by the startup pass")
}

// With Coral's own tmux server not running (after a reboot), the startup pass
// lowers nothing, even with the fallback to the default socket on and a tmux
// server with panes there: those are the user's own (#3437 review).
func TestLowerExistingPanesNeverTouchesTheDefaultServer(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	agentprio.SetSetting(agentprio.Low)
	t.Cleanup(func() { agentprio.SetSetting(agentprio.Low) })

	// The default socket lives under TMUX_TMPDIR; give this test its own.
	tmpdir, err := os.MkdirTemp("", "ct-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpdir) })
	t.Setenv("TMUX_TMPDIR", tmpdir)
	t.Setenv("TMUX", "")
	userSock := filepath.Join(tmpdir, fmt.Sprintf("tmux-%d", os.Getuid()), "default")
	t.Cleanup(func() { exec.Command(c.TmuxBin, "-S", userSock, "kill-server").Run() })
	userPane, err := c.runOnSocket(ctx, "", "new-session", "-d", "-P", "-F", "#{pane_pid}", "-s", "user-own", "sleep 30")
	require.NoError(t, err)
	base := niceOf(t, strconv.Itoa(os.Getpid()))

	c.FallbackToDefault = true
	exec.Command(c.TmuxBin, "-S", c.SocketPath, "kill-server").Run()
	_, statErr := os.Stat(c.SocketPath)
	if statErr == nil {
		require.NoError(t, os.Remove(c.SocketPath))
	}
	_, err = c.run(ctx, "list-panes", "-a", "-F", "#{pane_pid}")
	require.NoError(t, err, "the fallback does reach the user's server")

	assert.Equal(t, 0, c.LowerExistingPanes(ctx))
	assert.Equal(t, base, niceOf(t, userPane), "the user's pane keeps its priority")
}
