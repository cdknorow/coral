//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4381 (Security's #4371 INFO d): a resumed session's copied files are readable by this user
// only, whatever the umask lets through.
func TestCopiedSessionFilesAreOwnerOnly(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "copy")
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.jsonl"), []byte("{}"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(src, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("b"), 0o644))
	copyDir(src, dst)
	for _, f := range []string{"a.jsonl", filepath.Join("sub", "b.txt")} {
		info, err := os.Stat(filepath.Join(dst, f))
		require.NoError(t, err, f)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), f)
	}
}
