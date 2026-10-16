package logrotate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4112: OnOpen runs with the open file at once and with each new file after a rotation, so the
// tray's stderr (native crash text) follows tray.log into each new file.
func TestOnOpenRunsForEachNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tray.log")
	r, err := Open(path, 64, 2, 0)
	require.NoError(t, err)
	defer r.Close()
	var opened []os.FileInfo
	r.OnOpen(func(f *os.File) {
		st, err := f.Stat()
		require.NoError(t, err)
		opened = append(opened, st)
	})
	require.Len(t, opened, 1, "at once, with the open file")

	line := strings.Repeat("x", 40) + "\n"
	for i := 0; i < 3; i++ {
		_, err := r.Write([]byte(line))
		require.NoError(t, err)
	}
	require.Len(t, opened, 3, "once more for each rotation")
	assert.False(t, os.SameFile(opened[0], opened[1]), "a new file each time")
	cur, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, os.SameFile(opened[2], cur), "the last one is the file at the path now")
}
