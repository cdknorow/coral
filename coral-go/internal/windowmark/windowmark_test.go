package windowmark

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
)

func TestTheMarkIsSetClearedAndOnlyAPlainFileCounts(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, Present(dir))
	Set(dir)
	assert.True(t, Present(dir))
	Clear(dir)
	assert.False(t, Present(dir))
	assert.False(t, Present(""), "no data folder, no mark")
	Set("")
	Clear("")

	// a folder or a link of that name is no mark, and Clear leaves it alone
	require.NoError(t, os.Mkdir(filepath.Join(dir, FileName), 0o755))
	assert.False(t, Present(dir))
	Clear(dir)
	_, err := os.Stat(filepath.Join(dir, FileName))
	assert.NoError(t, err, "a folder is not removed")
	require.NoError(t, os.Remove(filepath.Join(dir, FileName)))
	t.Run("a link", func(t *testing.T) {
		target := filepath.Join(dir, "elsewhere")
		require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))
		linktest.Symlink(t, target, filepath.Join(dir, FileName)) // skips where this account can't make links (Windows without Developer Mode); fails on CI
		assert.False(t, Present(dir), "a link is no mark")
	})
}

// Set never writes through a link planted at the marker's name (#183): a symbolic link, a junction (a folder) or a hard link is refused and its target is untouched. (The
// symbolic link case skips where this user cannot make one; CI runs it.)
func TestSetNeverWritesThroughALink(t *testing.T) {
	dir := t.TempDir()
	linktest.Each(t, filepath.Join(dir, FileName), func(t *testing.T, p linktest.Planted) {
		Set(dir)
		if p.Kind != "hardlink" { // (a hard link is a plain file by that name: a mark; what matters, and what the harness checks, is that its target is not written through)
			assert.False(t, Present(dir), "a link is no mark, and Set did not turn it into one")
		}
	})
}
