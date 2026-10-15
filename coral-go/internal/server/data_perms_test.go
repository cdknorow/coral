package server

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3552: a data folder an older Coral left world-readable is made this user's only at
// startup: folders 0700, the databases, coral.log and artifact files 0600. A symlink is
// never followed, and a tighter mode is never loosened.
func TestTightenDataPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755))
	mk := func(rel string, mode os.FileMode) string {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), mode))
		require.NoError(t, os.Chmod(p, mode))
		return p
	}
	files := []string{
		mk("sessions.db", 0o644), mk("messageboard.db", 0o644), mk("messageboard.db-wal", 0o644),
		mk("coral.log", 0o644), mk("artifacts/objects/abc", 0o644), mk("artifacts/objects/abc.json", 0o644),
		mk("artifacts/uploads/sess/abc.json", 0o644), mk("artifacts/sessions/s1/changes.diff", 0o644),
	}
	tight := mk("artifacts/objects/def", 0o400)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	require.NoError(t, os.WriteFile(outside, []byte("x"), 0o644))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "artifacts", "objects", "link")))

	tightenDataPermissions(dir)

	mode := func(p string) os.FileMode { fi, err := os.Stat(p); require.NoError(t, err); return fi.Mode().Perm() }
	assert.Equal(t, os.FileMode(0o700), mode(dir))
	for _, d := range []string{"artifacts", "artifacts/objects", "artifacts/uploads", "artifacts/uploads/sess", "artifacts/sessions/s1"} {
		assert.Equal(t, os.FileMode(0o700), mode(filepath.Join(dir, d)), d)
	}
	for _, f := range files {
		assert.Equal(t, os.FileMode(0o600), mode(f), f)
	}
	assert.Equal(t, os.FileMode(0o400), mode(tight), "a tighter mode stays")
	assert.Equal(t, os.FileMode(0o644), mode(outside), "a symlink's target is never touched")

	// A second start changes nothing.
	tightenDataPermissions(dir)
	assert.Equal(t, os.FileMode(0o600), mode(files[0]))
}
