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
	"golang.org/x/sys/windows"
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

// A folder of 259+ characters is started in through its short path when the volume has 8.3 names (Security's probe on #116: the first version of shortPath never
// worked), else refused with the length and the limit.
func TestStartDirTooLongGivesTheShortPathOrAPlainError(t *testing.T) {
	d := deepDir(t, maxStartDirUnits+50)
	short := shortPath(d)
	// Whether the volume has 8.3 names, asked without shortPath (a broken shortPath must not decide its own test).
	hasShortNames := false
	if lp, err := windows.UTF16PtrFromString(string([]rune{92, 92, 63, 92}) + d); err == nil {
		buf := make([]uint16, windows.MAX_LONG_PATH)
		if n, err := windows.GetShortPathName(lp, &buf[0], uint32(len(buf))); err == nil && n > 0 && int(n) <= len(buf) {
			hasShortNames = int(n)-4 <= maxStartDirUnits
		}
	}
	got, err := StartDir(d)
	if !hasShortNames {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "characters long")
		assert.Contains(t, err.Error(), "258")
		assert.NotContains(t, err.Error(), "directory name is invalid")
		return
	}
	require.NoError(t, err, "the volume has 8.3 names: the short path is used")
	assert.Equal(t, short, got)
	assert.Less(t, len(got), len(d))
	c := exec.Command("cmd.exe", "/c", "echo ok")
	c.Dir = got
	assert.NoError(t, c.Run(), "the short path starts a program")
}

// The temp folder is on a volume with 8.3 names here and on the CI runners; if a machine has none, the test above only checks the plain error, so say which it was.
func TestShortPathOfAFolderOver259Characters(t *testing.T) {
	d := deepDir(t, maxStartDirUnits+50)
	short := shortPath(d)
	if short == "" || len(short) >= len(d) {
		t.Skipf("no 8.3 short names under %s", os.TempDir())
	}
	assert.NotContains(t, short, extendedPrefix)
	fi, err := os.Stat(short)
	require.NoError(t, err)
	want, err := os.Stat(d)
	require.NoError(t, err)
	assert.True(t, os.SameFile(want, fi), "the short path is the same folder")
}
