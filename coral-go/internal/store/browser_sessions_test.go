package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/auth"
)

// #3949: browser sessions are rows in sessions.db (only the cookie's hash), so they survive a
// restart; expired rows and the least recently used past the cap are swept.
func TestBrowserSessions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.db")
	db, err := OpenWithKey(ctx, path, "")
	require.NoError(t, err)
	b := NewBrowserSessions(db)
	now := time.Now().UTC().Truncate(time.Second)
	rec := func(id string, lastSeen, expires time.Time) auth.SessionRecord {
		return auth.SessionRecord{IDHash: id, Label: "Chrome on macOS", Method: auth.SessionByPassword, Persistent: true,
			CreatedAt: now, LastSeen: lastSeen, ExpiresAt: expires}
	}
	require.NoError(t, b.PutSession(ctx, rec("a", now, now.Add(time.Hour))))
	require.NoError(t, b.PutSession(ctx, rec("b", now.Add(-time.Minute), now.Add(time.Hour))))
	require.NoError(t, b.PutSession(ctx, rec("gone", now, now.Add(-time.Second))))
	got, ok, err := b.GetSession(ctx, "a")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, rec("a", now, now.Add(time.Hour)), got)
	require.NoError(t, b.TouchSession(ctx, "b", now.Add(time.Minute)))

	// Survives a restart.
	require.NoError(t, db.Close())
	db, err = OpenWithKey(ctx, path, "")
	require.NoError(t, err)
	defer db.Close()
	b = NewBrowserSessions(db)
	list, err := b.ListSessions(ctx)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, "b", list[0].IDHash, "most recently seen first")

	require.NoError(t, b.SweepSessions(ctx, now, 1))
	list, _ = b.ListSessions(ctx)
	require.Len(t, list, 1, "expired swept, then only the most recent kept")
	assert.Equal(t, "b", list[0].IDHash)

	require.NoError(t, b.PutSession(ctx, rec("c", now, now.Add(time.Hour))))
	n, err := b.DeleteSessionsExcept(ctx, "c")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	require.NoError(t, b.DeleteSession(ctx, "c"))
	_, ok, _ = b.GetSession(ctx, "c")
	assert.False(t, ok)
}
