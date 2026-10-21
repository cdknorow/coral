//go:build !windows

package secfile

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H5 outside Windows: a folder its group or everyone can write is reported; this user's own
// folder (an app bundle, an unpacked tarball) is not.
func TestWriters(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755))
	who, err := Writers(dir)
	require.NoError(t, err)
	assert.Empty(t, who)
	require.NoError(t, os.Chmod(dir, 0o777))
	who, err = Writers(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"its group", "everyone"}, who)
}
