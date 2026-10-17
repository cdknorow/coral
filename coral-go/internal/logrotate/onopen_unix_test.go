//go:build !windows

package logrotate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// #4112: an fd pointed at the log with dup2 from OnOpen (as the tray does with stderr) writes into
// the new file after a rotation, not into the rotated copy.
func TestADupedFdFollowsTheLogAfterARotation(t *testing.T) {
	spare, err := os.Open(os.DevNull) // stands in for fd 2
	require.NoError(t, err)
	defer spare.Close()
	path := filepath.Join(t.TempDir(), "tray.log")
	r, err := Open(path, 64, 2, 0)
	require.NoError(t, err)
	defer r.Close()
	r.OnOpen(func(f *os.File) { require.NoError(t, unix.Dup2(int(f.Fd()), int(spare.Fd()))) })

	_, err = r.Write([]byte(strings.Repeat("a", 60) + "\n")) // fills the first file
	require.NoError(t, err)
	_, err = r.Write([]byte(strings.Repeat("b", 10) + "\n")) // past 64 bytes: rotates
	require.NoError(t, err)
	_, err = unix.Write(int(spare.Fd()), []byte("crash text\n"))
	require.NoError(t, err)

	now, err := os.ReadFile(path)
	require.NoError(t, err)
	old, err := os.ReadFile(path + ".1")
	require.NoError(t, err)
	assert.Contains(t, string(now), "crash text", "in the new file")
	assert.NotContains(t, string(old), "crash text", "not in the rotated copy")
}
