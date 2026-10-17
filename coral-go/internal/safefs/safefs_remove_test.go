package safefs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Root.Remove (#4447): one regular file, by its exact name inside the root, and nothing else.
func TestRootRemove(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRoot(dir)
	require.NoError(t, err)
	defer r.Close()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.log"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.log"), []byte("y"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "folder.log"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "folder.log", "inner"), []byte("z"), 0o600))

	require.NoError(t, r.Remove("a.log"))
	_, err = os.Lstat(filepath.Join(dir, "a.log"))
	assert.True(t, os.IsNotExist(err), "the named file is gone")
	_, err = os.Lstat(filepath.Join(dir, "b.log"))
	assert.NoError(t, err, "a neighbour is not")

	assert.Error(t, r.Remove("a.log"), "a name that is not there is an error")
	assert.ErrorIs(t, r.Remove("folder.log"), ErrNotRegular, "a folder is not removed, empty or not")
	_, err = os.Lstat(filepath.Join(dir, "folder.log", "inner"))
	assert.NoError(t, err)

	// Nothing outside the root, by a relative path or an absolute one.
	outsideDir := t.TempDir()
	victim := filepath.Join(outsideDir, "victim.log")
	require.NoError(t, os.WriteFile(victim, []byte("v"), 0o600))
	rel, err := filepath.Rel(dir, victim)
	require.NoError(t, err)
	assert.Error(t, r.Remove(rel), "../ out of the root")
	assert.Error(t, r.Remove(victim), "an absolute path")
	_, err = os.Lstat(victim)
	assert.NoError(t, err, "the file outside is untouched")

	// A symlink as the last name is not followed and not removed through: it is refused, and its target stays.
	link := filepath.Join(dir, "link.log")
	if err := os.Symlink(victim, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("no symlink here")
		}
		t.Fatal(err)
	}
	assert.ErrorIs(t, r.Remove("link.log"), ErrNotRegular)
	_, err = os.Lstat(victim)
	assert.NoError(t, err, "its target is untouched")
	// A link folder in the middle of the path is not walked through either.
	require.NoError(t, os.Symlink(outsideDir, filepath.Join(dir, "linkdir")))
	assert.Error(t, r.Remove("linkdir/victim.log"))
	_, err = os.Lstat(victim)
	assert.NoError(t, err)
}
