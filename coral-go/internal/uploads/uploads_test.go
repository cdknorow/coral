package uploads

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func put(t *testing.T, dir, name string, size int, age time.Duration, now time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, make([]byte, size), 0o644))
	require.NoError(t, os.Chtimes(p, now.Add(-age), now.Add(-age)))
	return p
}

func TestSettings(t *testing.T) {
	assert.Equal(t, 30, RetentionDays(nil))
	assert.Equal(t, 7, RetentionDays(map[string]string{RetentionSetting: " 7 "}))
	assert.Equal(t, 1, RetentionDays(map[string]string{RetentionSetting: "0"}))
	assert.Equal(t, 365, RetentionDays(map[string]string{RetentionSetting: "9999"}))
	assert.Equal(t, 30, RetentionDays(map[string]string{RetentionSetting: "x"}))
	assert.Equal(t, int64(1024)<<20, MaxBytes(nil))
	assert.Equal(t, int64(50)<<20, MaxBytes(map[string]string{MaxMBSetting: "1"}))
	assert.Equal(t, int64(200)<<20, MaxBytes(map[string]string{MaxMBSetting: "200"}))
}

func TestPruneRemovesOnlyExpiredFiles(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	old := put(t, dir, "old.png", 10, 40*24*time.Hour, now)
	fresh := put(t, dir, "fresh.png", 10, time.Hour, now)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	files, bytes, err := Prune(dir, now.Add(-30*24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, files)
	assert.EqualValues(t, 10, bytes)
	assert.NoFileExists(t, old)
	assert.FileExists(t, fresh)
	assert.DirExists(t, filepath.Join(dir, "sub"), "only regular files")

	files, _, err = Prune(filepath.Join(dir, "missing"), now)
	require.NoError(t, err, "no folder yet: nothing to do")
	assert.Zero(t, files)
}

// The cap evicts oldest first and never an upload from the last hour.
func TestMakeRoom(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	a := put(t, dir, "a.png", 40, 3*time.Hour, now)
	b := put(t, dir, "b.png", 40, 2*time.Hour, now)
	c := put(t, dir, "c.png", 40, 10*time.Minute, now)

	require.NoError(t, MakeRoom(dir, 30, 150, now), "fits: nothing removed")
	assert.FileExists(t, a)

	require.NoError(t, MakeRoom(dir, 60, 150, now))
	assert.NoFileExists(t, a, "oldest first")
	assert.FileExists(t, b)

	err := MakeRoom(dir, 140, 150, now)
	assert.ErrorIs(t, err, ErrFull, "c is from the last hour: kept")
	assert.FileExists(t, c)
	assert.NoFileExists(t, b, "older ones were evicted on the way")
}

func TestDelete(t *testing.T) {
	dir, now := t.TempDir(), time.Now()
	p := put(t, dir, "x.png", 1, 0, now)
	outside := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(outside, []byte("s"), 0o600))
	for _, bad := range []string{"", ".", "..", "../secret", "a/b", `a\b`} {
		assert.ErrorIs(t, Delete(dir, bad), ErrBadName, "%q", bad)
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, "d"), 0o755))
	assert.ErrorIs(t, Delete(dir, "d"), ErrBadName, "a directory")
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "link.png")))
		assert.ErrorIs(t, Delete(dir, "link.png"), ErrBadName, "a symlink")
		assert.FileExists(t, outside)
	}
	assert.ErrorIs(t, Delete(dir, "nope.png"), os.ErrNotExist)
	require.NoError(t, Delete(dir, "x.png"))
	assert.NoFileExists(t, p)
}
