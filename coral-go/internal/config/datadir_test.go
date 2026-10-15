package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3637: a new data folder is private (0700 on Unix); an existing one is left as it is.
func TestEnsureDataDirCreatesAPrivateFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "coral-data")
	require.NoError(t, EnsureDataDir(dir))
	fi, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, fi.IsDir())
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
		existing := t.TempDir()
		require.NoError(t, os.Chmod(existing, 0o755))
		require.NoError(t, EnsureDataDir(existing))
		fi, _ = os.Stat(existing)
		assert.Equal(t, os.FileMode(0o755), fi.Mode().Perm(), "an existing folder is left as it is")
	}
	assert.Error(t, EnsureDataDir(""))
}
