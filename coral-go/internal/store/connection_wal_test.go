package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWALReadPoolNonBlocking(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wal_test.db")

	db, err := OpenWithKey(context.Background(), dbPath, "")
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ss := NewSessionStore(db)

	// Seed a live session and setting
	err = ss.RegisterLiveSession(ctx, &LiveSession{
		SessionID: "sess-1",
		AgentType: "claude",
		AgentName: "TestAgent",
	})
	require.NoError(t, err)

	err = ss.SetSetting(ctx, "theme", "dark")
	require.NoError(t, err)

	// Begin a write transaction on the writer connection and write without committing
	tx, err := db.DB.BeginTxx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, "UPDATE user_settings SET value = 'light' WHERE key = 'theme'")
	require.NoError(t, err)

	// Concurrently read via Reader()
	readDone := make(chan struct{})
	start := time.Now()

	go func() {
		defer close(readDone)
		var val string
		err := db.Reader().GetContext(ctx, &val, "SELECT value FROM user_settings WHERE key = 'theme'")
		assert.NoError(t, err)
		assert.Equal(t, "dark", val) // Uncommitted update is not visible to reader, but reader does NOT block!

		ls, err := ss.GetLiveSession(ctx, "sess-1")
		assert.NoError(t, err)
		assert.NotNil(t, ls)
		assert.Equal(t, "TestAgent", ls.AgentName)

		all, err := ss.GetAllLiveSessions(ctx)
		assert.NoError(t, err)
		assert.Len(t, all, 1)
	}()

	select {
	case <-readDone:
		duration := time.Since(start)
		assert.Less(t, duration, 100*time.Millisecond, "Read queries should complete quickly without blocking on writer transaction")
	case <-time.After(2 * time.Second):
		t.Fatal("Read query blocked on active write transaction!")
	}

	require.NoError(t, tx.Commit())

	// After commit, reader sees the new value
	var updatedVal string
	err = db.Reader().GetContext(ctx, &updatedVal, "SELECT value FROM user_settings WHERE key = 'theme'")
	require.NoError(t, err)
	assert.Equal(t, "light", updatedVal)
}

func TestWALReadPoolQueryOnlyRejection(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "readonly_test.db")

	db, err := OpenWithKey(context.Background(), dbPath, "")
	require.NoError(t, err)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Direct write on db.Reader() must fail with readonly error
	_, err = db.Reader().ExecContext(ctx, "INSERT INTO user_settings (key, value) VALUES ('k', 'v')")
	require.Error(t, err)
	assert.True(t, strings.Contains(strings.ToLower(err.Error()), "readonly") || strings.Contains(strings.ToLower(err.Error()), "read-only"),
		"Expected readonly error, got: %v", err)
}

func TestWALReadPoolInMemoryFallback(t *testing.T) {
	db, err := OpenWithKey(context.Background(), ":memory:", "")
	require.NoError(t, err)
	defer db.Close()

	assert.Equal(t, db.DB, db.Reader(), "In-memory database must use same connection for reader")
}
