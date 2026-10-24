//go:build windows

package ptymanager

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/naming"
)

// A record (no assertion) for the review of #6821 on Windows: where the handles of sessions that ended on their own go. N sessions end at once; the count after a wait; then each session is killed
// and the count again. A session that ended on its own stays in the backend's table until something kills it.
func TestRecordWhereWindowsHandlesGo(t *testing.T) {
	naming.SetDefaultLogDir(t.TempDir())
	b := NewPTYBackend()
	defer b.Close()
	require.NoError(t, b.Spawn("diag-warm", "test", t.TempDir(), "sid-diag-warm", "powershell.exe -NoProfile -Command exit", 120, 40))
	time.Sleep(exitDrain + 4*time.Second)
	base := handleCount(t)
	const runs = 8
	for i := 0; i < runs; i++ {
		require.NoError(t, b.Spawn(fmt.Sprintf("diag-%d", i), "test", t.TempDir(), fmt.Sprintf("sid-diag-%d", i), "powershell.exe -NoProfile -Command exit", 120, 40))
	}
	time.Sleep(exitDrain + 6*time.Second)
	afterExit := handleCount(t)
	time.Sleep(10 * time.Second)
	later := handleCount(t)
	for i := 0; i < runs; i++ {
		_ = b.Kill(fmt.Sprintf("diag-%d", i))
	}
	time.Sleep(4 * time.Second)
	afterKill := handleCount(t)
	_ = b.Kill("diag-warm")
	time.Sleep(2 * time.Second)
	t.Logf("RECORD handles: base %d; %d sessions ended on their own: %d after 6 s, %d after 16 s (%.1f per session); after killing them one by one: %d (%.1f per session left)",
		base, runs, afterExit, later, float64(later-base)/runs, afterKill, float64(int(afterKill)-int(base))/runs)
}
