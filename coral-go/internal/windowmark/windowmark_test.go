package windowmark

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	target := filepath.Join(dir, "elsewhere")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, FileName)))
	assert.False(t, Present(dir), "a link is no mark")
}
