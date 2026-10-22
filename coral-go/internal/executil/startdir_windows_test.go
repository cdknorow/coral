//go:build windows

package executil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func deepDir(t *testing.T, n int) string {
	d := t.TempDir()
	for len(d) < n {
		seg := n - len(d) - 1
		if seg > 40 {
			seg = 40
		}
		if seg < 1 {
			seg = 1
		}
		d = filepath.Join(d, strings.Repeat("d", seg))
	}
	require.NoError(t, os.MkdirAll(d, 0o755))
	return d
}

func TestStartDirLeavesShortPathsAlone(t *testing.T) {
	d := t.TempDir()
	got, err := StartDir(d)
	require.NoError(t, err)
	assert.Equal(t, d, got)
	d = deepDir(t, maxStartDirUnits)
	got, err = StartDir(d)
	require.NoError(t, err)
	assert.Equal(t, d, got)
	c := exec.Command("cmd.exe", "/c", "echo ok")
	c.Dir = got
	assert.NoError(t, c.Run(), "the longest accepted folder starts a program")
}

// A folder of 259+ characters is started in through its short path when the volume has one, else refused with the length and the limit.
func TestStartDirTooLongGivesTheShortPathOrAPlainError(t *testing.T) {
	d := deepDir(t, maxStartDirUnits+5)
	got, err := StartDir(d)
	if err != nil {
		assert.Contains(t, err.Error(), "characters long")
		assert.Contains(t, err.Error(), "258")
		assert.NotContains(t, err.Error(), "directory name is invalid")
		return
	}
	assert.LessOrEqual(t, len(got), maxStartDirUnits)
	c := exec.Command("cmd.exe", "/c", "echo ok")
	c.Dir = got
	assert.NoError(t, c.Run(), "the short path starts a program")
}
