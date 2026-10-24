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
