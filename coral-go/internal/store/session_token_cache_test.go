package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4303: an owner lookup is answered from the cache, and every write that changes a token makes the
// very next lookup see it: a revoked, rotated, replaced or ended session's token is never
// accepted from the cache.
func TestSessionTokenOwnerCache(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ss := NewSessionStore(db)
	require.NoError(t, ss.RegisterLiveSession(ctx, &LiveSession{SessionID: "s1", AgentType: "claude", AgentName: "a", WorkingDir: "/tmp"}))
	tok, err := db.MintSessionToken(ctx, "s1", "claude-s1")
	require.NoError(t, err)

	warm := func(token string) {
		t.Helper()
		_, err := db.SessionTokenOwnerOf(ctx, token)
		require.NoError(t, err)
	}
	warm(tok)
	lookups, hits := db.SessionTokenCacheStats()
	warm(tok)
	warm(tok)
	l2, h2 := db.SessionTokenCacheStats()
	assert.EqualValues(t, 2, l2-lookups)
	assert.EqualValues(t, 2, h2-hits, "the repeats are answered from the cache")

	// Rotation (a restart mints again): the old token is refused at once, the new one works at once.
	tok2, err := db.MintSessionToken(ctx, "s1", "claude-s1")
	require.NoError(t, err)
	_, err = db.SessionTokenOwnerOf(ctx, tok)
	assert.ErrorIs(t, err, ErrUnknownSessionToken, "a rotated token")
	o, err := db.SessionTokenOwnerOf(ctx, tok2)
	require.NoError(t, err)
	assert.Equal(t, "s1", o.SessionID)
	warm(tok2)

	// A session replaced by a restart or resume: its token is refused at once, though it was cached.
	require.NoError(t, ss.ReplaceLiveSession(ctx, "s1", &LiveSession{SessionID: "s2", AgentType: "claude", AgentName: "a", WorkingDir: "/tmp"}))
	_, err = db.SessionTokenOwnerOf(ctx, tok2)
	assert.ErrorIs(t, err, ErrUnknownSessionToken, "a replaced session's token")
	tok3, err := db.MintSessionToken(ctx, "s2", "claude-s2")
	require.NoError(t, err)
	o, err = db.SessionTokenOwnerOf(ctx, tok3)
	require.NoError(t, err, "the restarted session's new token works at once")
	assert.Equal(t, "s2", o.SessionID)

	// A session that ends.
	warm(tok3)
	require.NoError(t, ss.UnregisterLiveSession(ctx, "s2"))
	_, err = db.SessionTokenOwnerOf(ctx, tok3)
	assert.ErrorIs(t, err, ErrUnknownSessionToken, "an ended session's token")
}

// Only a positive answer is kept, an error is never kept, and an entry expires.
func TestSessionTokenOwnerCacheKeepsOnlyLiveAnswers(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tok, err := db.MintSessionToken(ctx, "s1", "claude-s1")
	require.NoError(t, err)

	_, err = db.SessionTokenOwnerOf(ctx, "made-up")
	assert.ErrorIs(t, err, ErrUnknownSessionToken)
	_, err = db.SessionTokenOwnerOf(ctx, "made-up")
	assert.ErrorIs(t, err, ErrUnknownSessionToken)
	_, hits := db.SessionTokenCacheStats()
	assert.Zero(t, hits, "an unknown token is looked up every time")

	// A lookup that fails (a cancelled context) refuses the token and caches nothing.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = db.SessionTokenOwnerOf(cancelled, tok)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrUnknownSessionToken)
	_, err = db.SessionTokenOwnerOf(ctx, tok)
	require.NoError(t, err)
	_, hits = db.SessionTokenCacheStats()
	assert.Zero(t, hits, "the failed lookup left nothing behind")

	// Expiry: past the TTL the entry is asked for again.
	db.tokenOwners.mu.Lock()
	for k, e := range db.tokenOwners.entries {
		e.expires = time.Now().Add(-time.Second)
		db.tokenOwners.entries[k] = e
	}
	db.tokenOwners.mu.Unlock()
	_, err = db.SessionTokenOwnerOf(ctx, tok)
	require.NoError(t, err)
	_, hits = db.SessionTokenCacheStats()
	assert.Zero(t, hits, "an expired entry is not a hit")
}

// A lookup that began before an invalidation must not store its answer: it may have read the token
// before the revoking write committed.
func TestSessionTokenOwnerCacheDropsAnAnswerFromBeforeAnInvalidation(t *testing.T) {
	db := openTestDB(t)
	c := &db.tokenOwners
	gen := c.gen.Load()
	db.InvalidateSessionTokens() // the revoke commits while the lookup is in flight
	c.put("hash", SessionTokenOwner{SessionID: "s1"}, gen)
	_, ok := c.get("hash")
	assert.False(t, ok, "a stale answer was stored")
	c.put("hash", SessionTokenOwner{SessionID: "s1"}, c.gen.Load())
	_, ok = c.get("hash")
	assert.True(t, ok)
}

// GetSettingsForCLI (#4350) reads with a context the caller's cancellation cannot end: a client that
// hangs up in the middle of a team launch must not make the next agent read no settings.
func TestGetSettingsForCLISurvivesACancelledContext(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	require.NoError(t, ss.SetSetting(context.Background(), "cli_path_claude", "/mock/claude"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ss.GetSettings(ctx)
	require.Error(t, err, "a plain read with that context fails, which is the bug")
	got, err := ss.GetSettingsForCLI(ctx)
	require.NoError(t, err)
	assert.Equal(t, "/mock/claude", got["cli_path_claude"])
	assert.Greater(t, settingsForCLITimeout, time.Duration(0))
	assert.LessOrEqual(t, settingsForCLITimeout, 30*time.Second, "bounded")
}
