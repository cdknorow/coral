//go:build !windows

package ptymanager

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4013: a command that prints nothing is running while its process is alive, and ended
// promptly after it exits (an agent silent while it thinks is not stopped).
func TestSilentCommandStaysRunningUntilItExits(t *testing.T) {
	t.Parallel()
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()
	require.NoError(t, backend.Spawn("silent", "test", t.TempDir(), "sid-silent", "sh -c 'sleep 60'", 80, 24))
	time.Sleep(1 * time.Second)
	assert.True(t, backend.IsRunning("silent"), "a silent command is still running a second later")

	require.NoError(t, backend.Spawn("short", "test", t.TempDir(), "sid-short", "sh -c 'sleep 1'", 80, 24))
	assert.True(t, backend.IsRunning("short"))
	assert.Eventually(t, func() bool { return !backend.IsRunning("short") }, 5*time.Second, 20*time.Millisecond, "ended once it exits")
}
