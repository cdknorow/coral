package board

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// #3387: only the server's own posts are is_system; the API always says which.
func TestPostSystemMessage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sys, err := s.PostSystemMessage(ctx, "p", "Coral Task Queue", "[Task #1 claimed by Dev] x", nil)
	require.NoError(t, err)
	assert.True(t, sys.IsSystem)
	own, err := s.PostMessage(ctx, "p", "Dev", "hello", nil)
	require.NoError(t, err)
	assert.False(t, own.IsSystem)
	msgs, err := s.ListMessages(ctx, "p", 10, 0, 0)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	got := map[string]bool{}
	for _, m := range msgs {
		got[m.SubscriberID] = m.IsSystem
	}
	assert.Equal(t, map[string]bool{"Coral Task Queue": true, "Dev": false}, got)
}

// An older database gets the column once; the earlier posts of Coral's own senders are
// marked then.
func TestIsSystemBackfillOnUpgrade(t *testing.T) {
	path2 := filepath.Join(t.TempDir(), "old2.db")
	raw, err := sqlx.Open("sqlite", path2)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TABLE board_messages (id INTEGER PRIMARY KEY AUTOINCREMENT, project TEXT NOT NULL, session_id TEXT NOT NULL, subscriber_id TEXT, content TEXT NOT NULL, created_at TEXT NOT NULL);
		INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at) VALUES ('p','x','Coral Task Queue','a','2026-01-01T00:00:00Z'), ('p','y','Dev','b','2026-01-01T00:00:01Z')`)
	require.NoError(t, err)
	raw.Close()
	s2, err := NewStoreWithKey(path2, "")
	require.NoError(t, err)
	defer s2.Close()
	var flags []int
	require.NoError(t, s2.db.Select(&flags, `SELECT is_system FROM board_messages ORDER BY id`))
	assert.Equal(t, []int{1, 0}, flags)
}
