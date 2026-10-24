//go:build windows

package routes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// #7069: a working folder named in its 8.3 short form (what a runner's TEMP is) is the same folder as its long form, and what is under it is inside it, both ways round.
func TestPathInsideAWorkingFolderInItsShortForm(t *testing.T) {
	long := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(long, "a-folder-with-a-long-name"), 0o755))
	in, err := windows.UTF16PtrFromString(long)
	require.NoError(t, err)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(in, &buf[0], uint32(len(buf)))
	require.NoError(t, err)
	short := windows.UTF16ToString(buf[:n])
	if short == long {
		t.Skip("this volume makes no 8.3 names")
	}
	t.Logf("long %s short %s", long, short)
	for _, wd := range []string{long, short} {
		for _, cmd := range []string{"rm -rf build", "echo hi > notes.txt", "rm -r " + filepath.ToSlash(short) + "/out", "rm -r " + filepath.ToSlash(long) + "/out"} {
			assert.Empty(t, shellRisk(cmd, wd), "%s in %s", cmd, wd)
		}
		assert.Empty(t, fileRisk("Write", filepath.Join(short, "a.txt"), wd), "short path in "+wd)
		assert.Empty(t, fileRisk("Write", filepath.Join(long, "a.txt"), wd), "long path in "+wd)
		assert.NotEmpty(t, fileRisk("Write", filepath.Join(filepath.Dir(short), "elsewhere.txt"), wd), "next to it is outside, in "+wd)
	}
}
