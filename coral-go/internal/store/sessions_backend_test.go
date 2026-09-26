package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveSession_BackendField(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()

	// Register a session with backend='pty'
	err := ss.RegisterLiveSession(ctx, &LiveSession{
		SessionID:  "sess-pty-1",
		AgentType:  "claude",
		AgentName:  "myproject",
		WorkingDir: "/tmp/test",
		Backend:    backendPtr("pty"),
	})
	require.NoError(t, err)

	// Register a session with default backend (tmux)
	err = ss.RegisterLiveSession(ctx, &LiveSession{
		SessionID:  "sess-tmux-1",
		AgentType:  "claude",
		AgentName:  "myproject2",
		WorkingDir: "/tmp/test2",
		Backend:    backendPtr("tmux"),
	})
	require.NoError(t, err)

	// Register a session with nil backend (should use DB default 'tmux')
	err = ss.RegisterLiveSession(ctx, &LiveSession{
		SessionID:  "sess-nil-1",
		AgentType:  "agy",
		AgentName:  "myproject3",
		WorkingDir: "/tmp/test3",
	})
	require.NoError(t, err)

	// Verify backend values via raw query
	var backend1, backend2 string
	var backend3 *string

	err = db.GetContext(ctx, &backend1, "SELECT backend FROM live_sessions WHERE session_id = ?", "sess-pty-1")
	require.NoError(t, err)
	assert.Equal(t, "pty", backend1)

	err = db.GetContext(ctx, &backend2, "SELECT backend FROM live_sessions WHERE session_id = ?", "sess-tmux-1")
	require.NoError(t, err)
	assert.Equal(t, "tmux", backend2)

	// nil backend should be stored as null or default to 'tmux'
	row := db.QueryRowContext(ctx, "SELECT backend FROM live_sessions WHERE session_id = ?", "sess-nil-1")
	err = row.Scan(&backend3)
	require.NoError(t, err)
	// Either nil (null) or 'tmux' from the DB default is acceptable
	if backend3 != nil {
		assert.Equal(t, "tmux", *backend3)
	}
}

func backendPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
