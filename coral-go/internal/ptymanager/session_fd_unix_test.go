//go:build !windows

package ptymanager

import (
	"fmt"
	"os"
	"path/filepath"
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

// settledFDs is the open descriptor count once it has stopped changing: the same value on five reads in a row, 100 ms apart. After a session ended and was drained its descriptors go in the order the
// exit watcher and the reader run them, which on a loaded machine takes longer than a fixed sleep (#7163); a count that has held still is what "the earlier session is gone" means. The ceiling only
// ends a count that never settles.
func settledFDs(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	last, same := -1, 0
	for time.Now().Before(deadline) {
		n := openFDs(t)
		if n == last {
			same++
		} else {
			last, same = n, 0
		}
		if same >= 5 {
			return n
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the open descriptor count never settled (last %d)", last)
	return last
}

// #6821 (Codex Test #6796): a session whose process ends on its own left its PTY master open until something killed it, one descriptor per scheduled run that ended naturally (about 144 a day for a 10-minute
// cron). After the exit and the drain the descriptor count is back where it was.
func TestSessionsThatEndOnTheirOwnLeaveNoDescriptorsOpen(t *testing.T) {
	canFork(t)
	backend := NewPTYBackend()
	defer backend.Close()
	// one throwaway run first: the first spawn opens a few process-wide descriptors of its own (the guardian, /dev/ptmx's first use)
	require.NoError(t, backend.Spawn("fd-warm", "test", t.TempDir(), "sid-fd-warm", "true", 80, 24))
	before := settledFDs(t)

	const runs = 12
	for i := 0; i < runs; i++ {
		require.NoError(t, backend.Spawn(fmt.Sprintf("fd-%d", i), "test", t.TempDir(), fmt.Sprintf("sid-fd-%d", i), "true", 80, 24))
	}
	require.Eventually(t, func() bool { return openFDs(t) <= before+2 }, 2*time.Minute, 100*time.Millisecond,
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
	before := settledFDs(t)

	// The shell starts a sleeper with the terminal as its output, waits for the test's signal (a file), then exits. The test holds the shell until it has seen the terminal held, so how long it takes to look
	// is no race with the shell's exit; and the sleeper outlives the whole test, so what frees the descriptor is the exit and not the child's end (#7163).
	release := filepath.Join(t.TempDir(), "release")
	command := "sleep 300 & while [ ! -e '" + release + "' ]; do sleep 0.05; done; exit 0"
	require.NoError(t, backend.Spawn("fd-child", "test", t.TempDir(), "sid-fd-child", command, 80, 24))
	assert.Greater(t, openFDs(t), before, "the session holds its terminal while it runs")
	require.NoError(t, os.WriteFile(release, []byte("go"), 0o644))
	require.Eventually(t, func() bool { return openFDs(t) <= before+1 }, 2*time.Minute, 50*time.Millisecond,
		"descriptors while the left-behind child still runs: %d (before %d)", openFDs(t), before)
}
