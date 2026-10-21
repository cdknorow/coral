package inputlog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4167: turning the log off, or moving it to another data folder, stops the writer (what it
// queued is written first): it was left running with its file open, one per folder or toggle.
func TestTurningTheLogOffOrMovingItStopsTheWriter(t *testing.T) {
	t.Cleanup(func() { Setup("", false); SetSetting(false) })
	writers := func() int {
		buf := make([]byte, 1<<20)
		return strings.Count(string(buf[:runtime.Stack(buf, true)]), "(*fileWriter).loop")
	}
	before := writers()
	a, b := t.TempDir(), t.TempDir()
	Setup(a, true)
	Write("claude-x:0.0", Text, "first")
	Setup(b, true) // moved: a's writer ends, after writing what it had
	Write("claude-x:0.0", Text, "second")
	Flush()
	require.Eventually(t, func() bool { return writers() == before+1 }, 2*time.Second, 10*time.Millisecond, "one writer, for b")
	got, err := os.ReadFile(filepath.Join(a, FileName))
	require.NoError(t, err)
	assert.Contains(t, string(got), "first", "a's queued line was written before its writer stopped")

	Setup(b, false) // off
	require.Eventually(t, func() bool { return writers() == before }, 2*time.Second, 10*time.Millisecond, "no writer once off")
	got, err = os.ReadFile(filepath.Join(b, FileName))
	require.NoError(t, err)
	assert.Contains(t, string(got), "second")
	Flush() // no writer: returns at once
}
