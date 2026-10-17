package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4381 (Security's #4371 INFO d): coral-board export --output writes a file only its owner can
// read, a new one or one that was there.
func TestExportFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	dir := t.TempDir()
	fresh := filepath.Join(dir, "new.md")
	require.NoError(t, writeExport(fresh, []byte("# board")))
	old := filepath.Join(dir, "old.md")
	require.NoError(t, os.WriteFile(old, []byte("x"), 0o644))
	require.NoError(t, writeExport(old, []byte("# board")))
	for _, p := range []string{fresh, old} {
		info, err := os.Stat(p)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), p)
	}
	data, err := os.ReadFile(old)
	require.NoError(t, err)
	assert.Equal(t, "# board", string(data))
}
