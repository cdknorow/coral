//go:build !windows

package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3627 F2: a regenerated key that can't be saved fails the call and changes nothing, since a
// key kept only in memory would bring the OLD key back at the next start.
func TestRegenerateKeyFailsWhenItCantBeSaved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	require.NoError(t, err)
	old := ks.Key()
	sess, err := ks.CreateSession("127.0.0.1", "test")
	require.NoError(t, err)

	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	_, err = ks.RegenerateKey()
	require.Error(t, err)
	assert.Equal(t, old, ks.Key(), "the old key stays active")
	assert.True(t, ks.ValidateSession(sess), "sessions are untouched")
	b, _ := os.ReadFile(filepath.Join(dir, "api_key"))
	assert.Equal(t, old, strings.TrimSpace(string(b)), "the file still holds the old key")

	require.NoError(t, os.Chmod(dir, 0o700))
	key, err := ks.RegenerateKey()
	require.NoError(t, err)
	assert.NotEqual(t, old, key)
	assert.False(t, ks.ValidateSession(sess), "a new key ends the old sessions")
	ks2, err := NewKeyStore(dir)
	require.NoError(t, err)
	assert.Equal(t, key, ks2.Key(), "the new key survives a restart")
}

// A key file readable by others is made private on load; one that is a link is refused.
func TestKeyFileModeAndTypeAreCheckedOnLoad(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "api_key")
	key := strings.Repeat("a", keyLength)
	require.NoError(t, os.WriteFile(p, []byte(key+"\n"), 0o644))
	require.NoError(t, os.Chmod(p, 0o644))
	ks, err := NewKeyStore(dir)
	require.NoError(t, err)
	assert.Equal(t, key, ks.Key())
	fi, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "tightened")

	linked := t.TempDir()
	require.NoError(t, os.Symlink(p, filepath.Join(linked, "api_key")))
	_, err = NewKeyStore(linked)
	assert.Error(t, err, "a linked key file is refused")
}

// Regenerating replaces the file atomically: a private regular file, never written through a
// link planted at its name, and no temp files left behind.
func TestRegenerateKeyWritesAPrivateFile(t *testing.T) {
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	require.NoError(t, err)
	p := filepath.Join(dir, "api_key")

	outside := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(outside, []byte("keep"), 0o644))
	require.NoError(t, os.Remove(p))
	require.NoError(t, os.Symlink(outside, p))
	key, err := ks.RegenerateKey()
	require.NoError(t, err)
	b, _ := os.ReadFile(outside)
	assert.Equal(t, "keep", string(b), "the link's target is untouched")
	li, err := os.Lstat(p)
	require.NoError(t, err)
	assert.True(t, li.Mode().IsRegular(), "the link was replaced by a regular file")
	assert.Equal(t, os.FileMode(0o600), li.Mode().Perm())
	b, _ = os.ReadFile(p)
	assert.Equal(t, key, strings.TrimSpace(string(b)))
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".api_key-"), "temp file left: %s", e.Name())
	}
}
