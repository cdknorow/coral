//go:build !windows

package secfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H5 outside Windows: a folder its group or everyone can write is reported; this user's own
// folder (an app bundle, an unpacked tarball) is not.
func TestWriters(t *testing.T) {
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0o755))
	dir := filepath.Join(parent, "tools")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o755))
	who, err := Writers(dir)
	require.NoError(t, err)
	assert.Empty(t, who)
	require.NoError(t, os.Chmod(dir, 0o777))
	who, err = Writers(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"its group", "everyone"}, who)
}

// H5 review N2: a parent its group or everyone can write (without the sticky bit) lets them move
// the folder away and put another at its path.
func TestWritersReadsTheParentFolder(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "tools")
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.Chmod(parent, 0o777))
	who, err := Writers(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"its group (through the parent folder)", "everyone (through the parent folder)"}, who)
	require.NoError(t, os.Chmod(parent, 0o777|os.ModeSticky))
	who, err = Writers(dir)
	require.NoError(t, err)
	assert.Empty(t, who, "a sticky parent (as /tmp): only the folder's owner can move it")
}
