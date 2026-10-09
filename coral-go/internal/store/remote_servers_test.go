package store

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/secretbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const plainKey = "PLAINTEXT-API-KEY-1234567890"

func newRemoteStore(t *testing.T) (*RemoteServerStore, *DB, string, string) {
	t.Helper()
	t.Setenv(secretbox.EnvKey, "")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sessions.db")
	db, err := Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return NewRemoteServerStore(db, dir), db, dir, dbPath
}

func TestRemoteServers_CRUDNoPlaintext(t *testing.T) {
	s, db, dir, dbPath := newRemoteStore(t)
	ctx := context.Background()

	srv, err := s.Add(ctx, "work", "Workstation", "http://10.0.0.5:8420/", plainKey, true)
	require.NoError(t, err)
	assert.Equal(t, "http://10.0.0.5:8420", srv.URL)
	assert.True(t, srv.AllowPrivate)
	assert.Equal(t, RemoteStatusUnknown, srv.Status)

	var stored string
	require.NoError(t, db.Get(&stored, "SELECT api_key FROM remote_servers WHERE id='work'"))
	assert.True(t, strings.HasPrefix(stored, "v1:"))
	assert.NotContains(t, stored, plainKey)

	// Raw DB file bytes (incl. WAL) contain no plaintext.
	_, _ = db.Exec("PRAGMA wal_checkpoint(FULL)")
	for _, p := range []string{dbPath, dbPath + "-wal"} {
		if b, err := os.ReadFile(p); err == nil {
			assert.False(t, bytes.Contains(b, []byte(plainKey)), p)
		}
	}
	// Master key lives outside the DB, mode 0600.
	fi, err := os.Stat(filepath.Join(dir, ".remote_secret_key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())

	got, err := s.DecryptedKey(ctx, "work")
	require.NoError(t, err)
	assert.Equal(t, plainKey, got)

	_, err = s.Add(ctx, "work", "dup", "http://x", "k", false)
	assert.ErrorIs(t, err, ErrRemoteExists)
	for _, bad := range []string{"local", "UPPER", "", "a_b", strings.Repeat("a", 33), "a/b"} {
		_, err = s.Add(ctx, bad, "x", "http://x", "k", false)
		assert.ErrorIs(t, err, ErrInvalidRemoteID, bad)
	}

	label := "New"
	newKey := "ANOTHER-KEY"
	srv, err = s.Update(ctx, "work", RemoteServerUpdate{Label: &label, APIKey: &newKey})
	require.NoError(t, err)
	assert.Equal(t, "New", srv.Label)
	got, _ = s.DecryptedKey(ctx, "work")
	assert.Equal(t, newKey, got)

	list, err := s.List(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 1)

	require.NoError(t, s.SetStatus(ctx, "work", RemoteStatusUnreachable, "boom"))
	srv, _ = s.Get(ctx, "work")
	assert.Equal(t, RemoteStatusUnreachable, srv.Status)
	assert.Equal(t, "boom", *srv.LastError)
	require.NoError(t, s.SetStatus(ctx, "work", RemoteStatusOnline, ""))
	srv, _ = s.Get(ctx, "work")
	assert.Nil(t, srv.LastError)
	assert.NotNil(t, srv.LastSeen)

	require.NoError(t, s.Delete(ctx, "work"))
	assert.ErrorIs(t, s.Delete(ctx, "work"), ErrRemoteNotFound)
	_, err = s.Get(ctx, "work")
	assert.ErrorIs(t, err, ErrRemoteNotFound)
	_, err = s.DecryptedKey(ctx, "work")
	assert.ErrorIs(t, err, ErrRemoteNotFound)
}

func TestRemoteServers_StructsNeverCarryKey(t *testing.T) {
	s, _, _, _ := newRemoteStore(t)
	ctx := context.Background()
	_, err := s.Add(ctx, "a", "A", "http://x", plainKey, false)
	require.NoError(t, err)
	srv, _ := s.Get(ctx, "a")
	list, _ := s.List(ctx)
	// The type has no key field; also check its fmt output.
	assert.NotContains(t, strings.ToLower(strings.Join([]string{
		asString(srv), asString(list)}, " ")), strings.ToLower(plainKey))
	assert.NotContains(t, asString(srv), "v1:")
}

func asString(v any) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(fmt.Sprintf("%+v", v)), "\n", " "))
}

func TestRemoteServers_SwappedCiphertextUnreadable(t *testing.T) {
	s, db, _, _ := newRemoteStore(t)
	ctx := context.Background()
	_, _ = s.Add(ctx, "a", "A", "http://a", "key-a", false)
	_, _ = s.Add(ctx, "b", "B", "http://b", "key-b", false)
	_, err := db.Exec("UPDATE remote_servers SET api_key = (SELECT api_key FROM remote_servers WHERE id='a') WHERE id='b'")
	require.NoError(t, err)

	_, err = s.DecryptedKey(ctx, "b")
	assert.ErrorIs(t, err, ErrKeyUnreadable)
	list, err := s.List(ctx)
	require.NoError(t, err)
	byID := map[string]string{}
	for _, r := range list {
		byID[r.ID] = r.Status
	}
	assert.Equal(t, RemoteStatusKeyUnreadable, byID["b"])
	assert.NotEqual(t, RemoteStatusKeyUnreadable, byID["a"])
	k, err := s.DecryptedKey(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, "key-a", k)
}

func TestRemoteServers_TamperedRowUnreadable(t *testing.T) {
	s, db, _, _ := newRemoteStore(t)
	ctx := context.Background()
	_, _ = s.Add(ctx, "a", "A", "http://a", "key-a", false)
	var env string
	require.NoError(t, db.Get(&env, "SELECT api_key FROM remote_servers WHERE id='a'"))
	flipped := env[:len(env)-3] + map[bool]string{true: "AAA", false: "BBB"}[!strings.HasSuffix(env, "AAA")]
	_, err := db.Exec("UPDATE remote_servers SET api_key=? WHERE id='a'", flipped)
	require.NoError(t, err)
	_, err = s.DecryptedKey(ctx, "a")
	assert.ErrorIs(t, err, ErrKeyUnreadable)
}

func TestRemoteServers_WrongMasterKey(t *testing.T) {
	s, db, dir, _ := newRemoteStore(t)
	ctx := context.Background()
	_, _ = s.Add(ctx, "a", "A", "http://a", "key-a", false)

	// Replace the key file with a different valid key; a fresh store reads it.
	require.NoError(t, os.Remove(filepath.Join(dir, ".remote_secret_key")))
	_, err := secretbox.CreateKeyFile(dir)
	require.NoError(t, err)
	s2 := NewRemoteServerStore(db, dir)
	_, err = s2.DecryptedKey(ctx, "a")
	assert.ErrorIs(t, err, ErrKeyUnreadable)
	list, err := s2.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, RemoteStatusKeyUnreadable, list[0].Status)
}

func TestRemoteServers_MissingKeyFileWithRowsDoesNotRecreate(t *testing.T) {
	s, db, dir, _ := newRemoteStore(t)
	ctx := context.Background()
	_, _ = s.Add(ctx, "a", "A", "http://a", "key-a", false)
	keyPath := filepath.Join(dir, ".remote_secret_key")
	require.NoError(t, os.Remove(keyPath))

	s2 := NewRemoteServerStore(db, dir)
	_, err := s2.DecryptedKey(ctx, "a")
	assert.ErrorIs(t, err, ErrKeyUnreadable)
	assert.ErrorIs(t, err, secretbox.ErrKeyMissing)
	list, err := s2.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, RemoteStatusKeyUnreadable, list[0].Status)

	// Adding must not silently mint a new key either.
	_, err = s2.Add(ctx, "b", "B", "http://b", "k", false)
	assert.ErrorIs(t, err, ErrKeyUnreadable)
	_, statErr := os.Stat(keyPath)
	assert.True(t, os.IsNotExist(statErr))

	// Explicit re-entry of the key recovers that server.
	newKey := "re-entered"
	_, err = s2.Update(ctx, "a", RemoteServerUpdate{APIKey: &newKey})
	require.NoError(t, err)
	got, err := s2.DecryptedKey(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, newKey, got)
}

func TestRemoteServers_PermissionRefusal(t *testing.T) {
	s, db, dir, _ := newRemoteStore(t)
	ctx := context.Background()
	_, _ = s.Add(ctx, "a", "A", "http://a", "key-a", false)
	require.NoError(t, os.Chmod(filepath.Join(dir, ".remote_secret_key"), 0644))

	s2 := NewRemoteServerStore(db, dir)
	_, err := s2.DecryptedKey(ctx, "a")
	assert.ErrorIs(t, err, ErrKeyUnreadable)
	assert.ErrorIs(t, err, secretbox.ErrKeyPermissions)
	// Must not be "fixed" by re-entering a key either.
	k := "x"
	_, err = s2.Update(ctx, "a", RemoteServerUpdate{APIKey: &k})
	assert.ErrorIs(t, err, ErrKeyUnreadable)
}

func TestRemoteServers_EnvOverride(t *testing.T) {
	t.Setenv(secretbox.EnvKey, strings.Repeat("ab", 32))
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "s.db"))
	require.NoError(t, err)
	defer db.Close()
	s := NewRemoteServerStore(db, dir)
	ctx := context.Background()
	_, err = s.Add(ctx, "a", "A", "http://a", "key-a", false)
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(dir, ".remote_secret_key"))
	assert.True(t, os.IsNotExist(statErr))
	got, err := NewRemoteServerStore(db, dir).DecryptedKey(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, "key-a", got)

	t.Setenv(secretbox.EnvKey, strings.Repeat("cd", 32))
	_, err = NewRemoteServerStore(db, dir).DecryptedKey(ctx, "a")
	assert.ErrorIs(t, err, ErrKeyUnreadable)
	// Rotation is refused when the key is injected.
	t.Setenv(secretbox.EnvKey, strings.Repeat("ab", 32))
	assert.Error(t, NewRemoteServerStore(db, dir).RotateMasterKey(ctx))
}

func TestRemoteServers_Rotation(t *testing.T) {
	s, db, dir, _ := newRemoteStore(t)
	ctx := context.Background()
	_, _ = s.Add(ctx, "a", "A", "http://a", "key-a", false)
	_, _ = s.Add(ctx, "b", "B", "http://b", "key-b", false)
	keyPath := filepath.Join(dir, ".remote_secret_key")
	oldKey, _ := os.ReadFile(keyPath)
	var oldRows []string
	require.NoError(t, db.Select(&oldRows, "SELECT api_key FROM remote_servers ORDER BY id"))

	require.NoError(t, s.RotateMasterKey(ctx))
	newKey, _ := os.ReadFile(keyPath)
	assert.NotEqual(t, oldKey, newKey)
	fi, _ := os.Stat(keyPath)
	assert.Equal(t, os.FileMode(0600), fi.Mode().Perm())
	_, statErr := os.Stat(keyPath + ".new")
	assert.True(t, os.IsNotExist(statErr))
	var newRows []string
	require.NoError(t, db.Select(&newRows, "SELECT api_key FROM remote_servers ORDER BY id"))
	assert.NotEqual(t, oldRows, newRows)

	for _, st := range []*RemoteServerStore{s, NewRemoteServerStore(db, dir)} {
		for id, want := range map[string]string{"a": "key-a", "b": "key-b"} {
			got, err := st.DecryptedKey(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		}
	}
}

func TestRemoteServers_RotationMidFailureKeepsOldKeyValid(t *testing.T) {
	s, db, dir, _ := newRemoteStore(t)
	ctx := context.Background()
	_, _ = s.Add(ctx, "a", "A", "http://a", "key-a", false)
	_, _ = s.Add(ctx, "b", "B", "http://b", "key-b", false)
	keyPath := filepath.Join(dir, ".remote_secret_key")
	oldKey, _ := os.ReadFile(keyPath)
	var oldRows []string
	require.NoError(t, db.Select(&oldRows, "SELECT api_key FROM remote_servers ORDER BY id"))

	s.rotateFailAfter = 1 // first row updated, second fails
	err := s.RotateMasterKey(ctx)
	require.Error(t, err)

	cur, _ := os.ReadFile(keyPath)
	assert.Equal(t, oldKey, cur, "old key file untouched")
	_, statErr := os.Stat(keyPath + ".new")
	assert.True(t, os.IsNotExist(statErr), "staged key cleaned up")
	var rows []string
	require.NoError(t, db.Select(&rows, "SELECT api_key FROM remote_servers ORDER BY id"))
	assert.Equal(t, oldRows, rows, "transaction rolled back")
	for id, want := range map[string]string{"a": "key-a", "b": "key-b"} {
		got, err := NewRemoteServerStore(db, dir).DecryptedKey(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestRemoteServers_RotationUnreadableRowAborts(t *testing.T) {
	s, db, dir, _ := newRemoteStore(t)
	ctx := context.Background()
	_, _ = s.Add(ctx, "a", "A", "http://a", "key-a", false)
	_, err := db.Exec("UPDATE remote_servers SET api_key='v1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' WHERE id='a'")
	require.NoError(t, err)
	keyPath := filepath.Join(dir, ".remote_secret_key")
	oldKey, _ := os.ReadFile(keyPath)
	assert.Error(t, s.RotateMasterKey(ctx))
	cur, _ := os.ReadFile(keyPath)
	assert.Equal(t, oldKey, cur)
}
