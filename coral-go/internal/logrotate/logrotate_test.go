package logrotate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3628 F5: the log rotates at its size, keeps its copies, never splits a line, and every file
// is 0600.
func TestRotatesAtItsSizeAndKeepsCopies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coral.log")
	r, err := Open(path, 100, 3)
	require.NoError(t, err)
	defer r.Close()
	line := strings.Repeat("x", 39) + "\n" // 40 bytes: two lines fit, the third rotates
	for i := 0; i < 20; i++ {
		_, err := r.Write([]byte(line))
		require.NoError(t, err)
	}
	for _, name := range []string{"coral.log", "coral.log.1", "coral.log.2", "coral.log.3"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err, name)
		assert.LessOrEqual(t, len(b), 100, "%s is within the size", name)
		assert.Zero(t, len(b)%len(line), "%s holds whole lines", name)
		if runtime.GOOS != "windows" {
			st, _ := os.Stat(filepath.Join(dir, name))
			assert.Equal(t, os.FileMode(0o600), st.Mode().Perm(), name)
		}
	}
	_, err = os.Stat(filepath.Join(dir, "coral.log.4"))
	assert.True(t, os.IsNotExist(err), "only keep copies")
}

// A log already over its size (coral.log was 457 MB) rotates on the first write, a looser file
// is made 0600, and copies beyond keep from an older setting are removed.
func TestOpenTakesOverAnOldLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "coral.log")
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("old\n", 100)), 0o644))
	for _, extra := range []string{"coral.log.6", "coral.log.9"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, extra), []byte("x"), 0o600))
	}
	r, err := Open(path, 100, 2)
	require.NoError(t, err)
	defer r.Close()
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(path)
		assert.Equal(t, os.FileMode(0o600), st.Mode().Perm(), "tightened on open")
	}
	_, err = r.Write([]byte("new\n"))
	require.NoError(t, err)
	b, _ := os.ReadFile(path)
	assert.Equal(t, "new\n", string(b), "the old log moved to .1")
	old, _ := os.ReadFile(path + ".1")
	assert.Equal(t, strings.Repeat("old\n", 100), string(old))
	for _, extra := range []string{"coral.log.6", "coral.log.9"} {
		_, err := os.Stat(filepath.Join(dir, extra))
		assert.True(t, os.IsNotExist(err), "%s removed", extra)
	}
}

// A symlink planted at the log's path is never followed.
func TestOpenDoesNotFollowASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no O_NOFOLLOW on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	require.NoError(t, os.WriteFile(target, []byte("keep"), 0o644))
	path := filepath.Join(dir, "coral.log")
	require.NoError(t, os.Symlink(target, path))
	_, err := Open(path, 100, 2)
	assert.Error(t, err)
	b, _ := os.ReadFile(target)
	assert.Equal(t, "keep", string(b))
}
