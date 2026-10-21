//go:build windows

package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secfile/secfiletest"
)

// #3905: the API key and login token files get an access list of their own (only this user and
// SYSTEM), not the data folder's.
func TestKeyAndLoginTokenFilesArePrivate(t *testing.T) {
	dir := t.TempDir()
	ks, err := NewKeyStore(dir)
	require.NoError(t, err)
	secfiletest.RequirePrivate(t, filepath.Join(dir, "api_key"))
	_, err = ks.RegenerateKey()
	require.NoError(t, err)
	secfiletest.RequirePrivate(t, filepath.Join(dir, "api_key"))

	_, err = ks.WriteLoginTokenFile(dir)
	require.NoError(t, err)
	secfiletest.RequirePrivate(t, filepath.Join(dir, LoginTokenFile))
}

// #3905: a key file another account can read is treated as known: a new key replaces it, in a
// private file. One that inherits a private access list is kept and made explicit.
func TestExposedKeyFileIsReplaced(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "api_key")
	old := strings.Repeat("a", keyLength)
	require.NoError(t, os.WriteFile(p, []byte(old+"\n"), 0o600))
	secfiletest.GrantUsersRead(t, p)
	ks, err := NewKeyStore(dir)
	require.NoError(t, err)
	assert.NotEqual(t, old, ks.Key(), "a new key")
	secfiletest.RequirePrivate(t, p)
	ks2, err := NewKeyStore(dir)
	require.NoError(t, err)
	assert.Equal(t, ks.Key(), ks2.Key(), "the new key is kept")

	kept := t.TempDir()
	secfiletest.OwnerOnlyFolder(t, kept) // the file inherits a private access list
	p = filepath.Join(kept, "api_key")
	require.NoError(t, os.WriteFile(p, []byte(old+"\n"), 0o600))
	ks, err = NewKeyStore(kept)
	require.NoError(t, err)
	assert.Equal(t, old, ks.Key())
	secfiletest.RequirePrivate(t, p)
}
