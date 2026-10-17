package startup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4381: the removed proxy's CA files go from the data folder at startup, by exact name and only
// as regular files; anything else is left as it is.
func TestRemoveOrphanProxyCA(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"proxy-ca-key.pem", "proxy-ca.pem", "proxy-ca-bundle.pem", "proxy-ca-key.pem.bak", "other.pem"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}
	assert.ElementsMatch(t, []string{"proxy-ca-key.pem", "proxy-ca.pem", "proxy-ca-bundle.pem"}, removeOrphanProxyCA(dir))
	for _, name := range []string{"proxy-ca-key.pem", "proxy-ca.pem", "proxy-ca-bundle.pem"} {
		_, err := os.Lstat(filepath.Join(dir, name))
		assert.True(t, os.IsNotExist(err), name)
	}
	for _, name := range []string{"proxy-ca-key.pem.bak", "other.pem"} {
		_, err := os.Lstat(filepath.Join(dir, name))
		assert.NoError(t, err, "%s is kept", name)
	}
	assert.Empty(t, removeOrphanProxyCA(dir), "nothing left to remove the second time")

	// A symlink by that name is not a file Coral wrote: it, and what it points to, stay.
	target := filepath.Join(t.TempDir(), "elsewhere.pem")
	require.NoError(t, os.WriteFile(target, []byte("keep"), 0o600))
	if err := os.Symlink(target, filepath.Join(dir, "proxy-ca.pem")); err != nil {
		t.Skipf("symlinks unavailable here: %v", err) // Windows without the privilege
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, "proxy-ca-key.pem"), 0o700))
	assert.Empty(t, removeOrphanProxyCA(dir))
	_, err := os.Lstat(filepath.Join(dir, "proxy-ca.pem"))
	assert.NoError(t, err, "the symlink stays")
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data))
}
