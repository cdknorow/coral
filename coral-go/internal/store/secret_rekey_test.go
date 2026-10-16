//go:build !windows

package store

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/secretbox"
)

// #3698: a refused master key is replaced by a re-key, which discards what the old key sealed
// (the remote servers' API keys, the Coral Hub enrollment and one in progress); a usable key is
// never replaced. Here the refusal is a mode other accounts can read (Unix); on Windows it is the
// access list (secfile), with the same store path.
func TestRekeyReplacesARefusedKeyAndDiscardsItsSecrets(t *testing.T) {
	rs, db, dir, _ := newRemoteStore(t)
	ctx := context.Background()
	cs := NewCloudStore(db, rs)
	_, err := rs.Add(ctx, "r1", "Remote", "https://r1.example.com", plainKey, false)
	require.NoError(t, err)
	require.NoError(t, cs.Save(ctx, "https://hub.example.com", "srv_1", "Laptop", "hub-token"))
	other := NewRemoteServerStore(db, dir) // another store in this process, with the old key cached
	_, err = other.DecryptedKey(ctx, "r1")
	require.NoError(t, err)

	assert.ErrorIs(t, rs.Rekey(ctx), ErrRekeyNotNeeded, "a usable key is never replaced")
	st, err := rs.KeyState(ctx)
	require.NoError(t, err)
	assert.Equal(t, "ok", st.State)

	require.NoError(t, os.Chmod(secretbox.KeyFilePath(dir), 0o644)) // other accounts can read it
	st, err = rs.KeyState(ctx)
	require.NoError(t, err)
	assert.Equal(t, "refused", st.State)
	assert.Equal(t, 1, st.RemoteServerKeys)
	assert.Equal(t, "https://hub.example.com", st.HubURL)
	_, err = secretbox.LoadKey(dir)
	require.ErrorIs(t, err, secretbox.ErrKeyPermissions, "refused before")

	require.NoError(t, rs.Rekey(ctx))

	_, err = secretbox.LoadKey(dir)
	require.NoError(t, err, "works after")
	fi, err := os.Stat(secretbox.KeyFilePath(dir))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	st, err = rs.KeyState(ctx)
	require.NoError(t, err)
	assert.Equal(t, SecretKeyState{State: "ok"}, st, "nothing sealed with the old key is left")

	var apiKey string
	require.NoError(t, db.GetContext(ctx, &apiKey, "SELECT api_key FROM remote_servers WHERE id = 'r1'"))
	assert.Empty(t, apiKey, "the old API key is gone")
	r, err := rs.Get(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, RemoteStatusKeyUnreadable, r.Status, "the server stays, its key to be entered again")
	_, err = cs.Get(ctx)
	assert.ErrorIs(t, err, ErrNoCloudConnection, "the Coral Hub enrollment is gone")

	// A new key for r1 is sealed with the new master key, also by the store that had the old
	// one cached, and both read it.
	newKey := "NEW-API-KEY"
	_, err = other.Update(ctx, "r1", RemoteServerUpdate{APIKey: &newKey})
	require.NoError(t, err)
	got, err := rs.DecryptedKey(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, newKey, got)
}

// A key from CORAL_SECRET_KEY is managed outside Coral: never re-keyed here.
func TestRekeyLeavesAnEnvironmentKeyAlone(t *testing.T) {
	rs, _, _, _ := newRemoteStore(t)
	t.Setenv(secretbox.EnvKey, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	err := rs.Rekey(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), secretbox.EnvKey)
	st, err := rs.KeyState(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env", st.State)
}

// A key file this user can't open (an access error, not a refusal of the key) is "unreadable":
// no re-key, nothing discarded. A malformed key is refused and re-keyed.
func TestRekeyOnlyForARefusalNotAReadError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	rs, db, dir, _ := newRemoteStore(t)
	ctx := context.Background()
	_, err := rs.Add(ctx, "r1", "Remote", "https://r1.example.com", plainKey, false)
	require.NoError(t, err)
	path := secretbox.KeyFilePath(dir)
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { os.Chmod(path, 0o600) })

	st, err := rs.KeyState(ctx)
	require.NoError(t, err)
	assert.Equal(t, "unreadable", st.State)
	assert.ErrorIs(t, rs.Rekey(ctx), ErrKeyNotRefused)
	var apiKey string
	require.NoError(t, db.GetContext(ctx, &apiKey, "SELECT api_key FROM remote_servers WHERE id = 'r1'"))
	assert.NotEmpty(t, apiKey, "nothing discarded")

	require.NoError(t, os.Chmod(path, 0o600))
	require.NoError(t, os.WriteFile(path, []byte("not a key\n"), 0o600))
	st, err = rs.KeyState(ctx)
	require.NoError(t, err)
	assert.Equal(t, "refused", st.State, "a malformed key is refused")
	require.NoError(t, rs.Rekey(ctx))
}
