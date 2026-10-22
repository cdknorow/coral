//go:build windows

package routes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A working folder of 259+ characters cannot be started in on Windows (CreateProcess); the launch says so for the dialog to mark, with the length and the limit,
// instead of a 500 "The directory name is invalid" from the spawn (#264).
func TestLaunchDirErrorNamesATooLongFolder(t *testing.T) {
	d := t.TempDir()
	for len(d) < 275 {
		seg := 275 - len(d) - 1
		if seg > 40 {
			seg = 40
		}
		if seg < 1 {
			seg = 1
		}
		d = filepath.Join(d, strings.Repeat("d", seg))
	}
	require.NoError(t, os.MkdirAll(d, 0o755))
	msg := launchDirError(d)
	if msg == "" {
		t.Skip("this volume has 8.3 short names: the folder starts through its short path")
	}
	assert.Contains(t, msg, "characters long")
	assert.Contains(t, msg, "use a shorter path")
	assert.Empty(t, launchDirError(t.TempDir()))
}
