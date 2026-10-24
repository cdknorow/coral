//go:build !windows

package ptymanager

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openFDs(t *testing.T) int {
	t.Helper()
	for _, dir := range []string{"/dev/fd", "/proc/self/fd"} {
		if entries, err := os.ReadDir(dir); err == nil {
			return len(entries)
		}
	}
	t.Skip("cannot count open descriptors here")
	return 0
}

// #6821 (Codex Test #6796): a session whose process ends on its own left its PTY master open until something killed it, one descriptor per scheduled run that ended naturally (about 144 a day for a 10-minute
// cron). After the exit and the drain the descriptor count is back where it was.
func TestSessionsThatEndOnTheirOwnLeaveNoDescriptorsOpen(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()
	// one throwaway run first: the first spawn opens a few process-wide descriptors of its own (the guardian, /dev/ptmx's first use)
	require.NoError(t, backend.Spawn("fd-warm", "test", t.TempDir(), "sid-fd-warm", "true", 80, 24))
	time.Sleep(exitDrain + 500*time.Millisecond)
	before := openFDs(t)

	const runs = 12
	for i := 0; i < runs; i++ {
		require.NoError(t, backend.Spawn(fmt.Sprintf("fd-%d", i), "test", t.TempDir(), fmt.Sprintf("sid-fd-%d", i), "true", 80, 24))
	}
	require.Eventually(t, func() bool { return openFDs(t) <= before+2 }, 10*time.Second, 100*time.Millisecond,
		"descriptors after %d sessions ended on their own: %d (before %d)", runs, openFDs(t), before)
	assert.LessOrEqual(t, openFDs(t), before+2)
}

// Security's #6826 LOW (the exit watcher's own case): the shell exits but a child it left behind keeps the terminal open, so readLoop never sees the end of the output. The descriptor is closed by the watcher after
// the drain, while that child is still running. On Unix this does not tell the watcher's close from readLoop's (removing the watcher's alone still passes): the guardian ends what the shell left behind (its group and the
// processes carrying its run tag) as the shell exits, so the output ends and readLoop frees the descriptor first. The case the watcher exists for is Windows, whose pseudo console's pipe only ends when it is closed:
// this test pins the outcome there too (Security Reviewer Windows runs it) and documents why the Unix run cannot.
func TestADescriptorIsFreedWhenTheShellExitsButAChildKeepsTheTerminal(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()
	require.NoError(t, backend.Spawn("fd-warm2", "test", t.TempDir(), "sid-fd-warm2", "true", 80, 24))
	time.Sleep(exitDrain + 500*time.Millisecond)
	before := openFDs(t)

	// the shell starts a sleeper with the terminal as its output, then exits; the sleeper ends by itself after 6 s
	require.NoError(t, backend.Spawn("fd-child", "test", t.TempDir(), "sid-fd-child", "sleep 6 & exit 0", 80, 24))
	assert.Greater(t, openFDs(t), before, "the session holds its terminal while it runs")
	require.Eventually(t, func() bool { return openFDs(t) <= before+1 }, 4*time.Second, 50*time.Millisecond,
		"descriptors while the left-behind child still runs: %d (before %d)", openFDs(t), before)
}
