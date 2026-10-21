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

// #5698 (review of #96): a folder is tightened, with every file in it, only when it is Coral's own.
func TestLooksLikeCoralDataDir(t *testing.T) {
	mk := func(names ...string) string {
		dir := t.TempDir()
		for _, n := range names {
			require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600))
		}
		return dir
	}
	for name, names := range map[string][]string{"empty": nil, "a database": {"sessions.db"}, "an old database": {"old.db"}, "the key": {"api-key", "notes.txt"}, "a wal": {"x.DB-WAL"}} {
		ok, _ := looksLikeCoralDataDir(mk(names...))
		assert.True(t, ok, name)
	}
	for name, names := range map[string][]string{"a project": {"main.go", "README.md"}, "documents": {"report.docx"}, "only a log": {"coral.log"}} {
		ok, other := looksLikeCoralDataDir(mk(names...))
		assert.False(t, ok, name)
		assert.NotEmpty(t, other, name)
	}
	ok, _ := looksLikeCoralDataDir(filepath.Join(t.TempDir(), "missing"))
	assert.False(t, ok, "a folder that cannot be read is not judged Coral's")
}
