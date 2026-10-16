package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3628 F7: the login_token file is removed once its token is used or has expired, and only
// while it still holds that token.
func TestLoginTokenFileIsRemovedAfterUse(t *testing.T) {
	ks := newTestKeyStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, LoginTokenFile)
	token, err := ks.WriteLoginTokenFile(dir)
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.NoError(t, err)
	require.True(t, ks.consumeLoginToken(token))
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "used: the file is gone")
}

func TestLoginTokenFileIsRemovedWhenItExpires(t *testing.T) {
	prev := loginFileExpiry
	loginFileExpiry = func() time.Duration { return 50 * time.Millisecond }
	t.Cleanup(func() { loginFileExpiry = prev })
	ks := newTestKeyStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, LoginTokenFile)
	_, err := ks.WriteLoginTokenFile(dir)
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, err := os.Stat(path); return os.IsNotExist(err) }, 5*time.Second, 10*time.Millisecond, "expired unused: the file is gone")
}

func TestLoginTokenFileKeepsANewerToken(t *testing.T) {
	ks := newTestKeyStore(t)
	dir := t.TempDir()
	path := filepath.Join(dir, LoginTokenFile)
	first, err := ks.WriteLoginTokenFile(dir)
	require.NoError(t, err)
	second, err := ks.WriteLoginTokenFile(dir) // `coral open` or a restart: a newer file
	require.NoError(t, err)
	require.True(t, ks.consumeLoginToken(first))
	b, err := os.ReadFile(path)
	require.NoError(t, err, "using the older token leaves the newer file")
	assert.Equal(t, second+"\n", string(b))

	// A file a person replaced is not Coral's to remove.
	require.NoError(t, os.WriteFile(path, []byte("someone else's\n"), 0o600))
	require.True(t, ks.consumeLoginToken(second))
	_, err = os.Stat(path)
	assert.NoError(t, err, "a replaced file stays")
}
