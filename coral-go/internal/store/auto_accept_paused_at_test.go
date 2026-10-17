package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// auto_accept_paused_at (#4521) is set with the paused code and cleared with it, on every path that sets or clears the code,
// so a client can tell a pause it has shown from a new one.
func TestAutoAcceptPausedAtGoesWithTheCode(t *testing.T) {
	db := openTestDB(t)
	ss := NewSessionStore(db)
	ctx := context.Background()
	const sid = "paused-at-1"
	require.NoError(t, ss.RegisterLiveSession(ctx, &LiveSession{SessionID: sid, AgentType: "claude", AgentName: "dev", WorkingDir: "/tmp/dev"}))
	state := func() AutoAcceptState {
		t.Helper()
		st, err := ss.GetAutoAcceptState(ctx, sid)
		require.NoError(t, err)
		return st
	}
	within := func(st AutoAcceptState, from, to int64, what string) {
		t.Helper()
		require.True(t, st.PausedCode.Valid, "%s: a paused code", what)
		require.True(t, st.PausedAt.Valid, "%s: paused_at is set with it", what)
		assert.GreaterOrEqual(t, st.PausedAt.Int64, from, what)
		assert.LessOrEqual(t, st.PausedAt.Int64, to, what)
	}
	none := func(st AutoAcceptState, what string) {
		t.Helper()
		assert.False(t, st.PausedCode.Valid, "%s: no paused code", what)
		assert.False(t, st.PausedAt.Valid, "%s: and no paused_at", what)
	}
	enable := func() {
		t.Helper()
		ok, err := ss.EnableAutoAccept(ctx, sid, AutoAcceptSourceLocal, 0)
		require.NoError(t, err)
		require.True(t, ok)
	}

	none(state(), "never used")

	// The loop guard.
	enable()
	before := time.Now().Unix()
	require.NoError(t, ss.PauseAutoAccept(ctx, sid, AutoAcceptRepeatLoop))
	within(state(), before, time.Now().Unix(), "paused")

	// Enabling again clears both.
	enable()
	none(state(), "enabled again")

	// The user turning it off clears both.
	require.NoError(t, ss.PauseAutoAccept(ctx, sid, AutoAcceptRepeatLoop))
	ok, err := ss.DisableAutoAccept(ctx, sid)
	require.NoError(t, err)
	require.True(t, ok)
	none(state(), "turned off by the user")

	// Expiry and Stop all set a code, so paused_at with it.
	ok, err = ss.EnableAutoAccept(ctx, sid, AutoAcceptSourceLocal, time.Now().Add(-time.Minute).Unix())
	require.NoError(t, err)
	require.True(t, ok)
	before = time.Now().Unix()
	n, err := ss.ExpireAutoAccept(ctx, time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	within(state(), before, time.Now().Unix(), "expired")
	assert.Equal(t, AutoAcceptExpired, state().PausedCode.String)

	enable()
	before = time.Now().Unix()
	n, err = ss.ClearAllAutoAccept(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	within(state(), before, time.Now().Unix(), "Stop all")

	// A run lifecycle clear sets no code: both go (sleep here).
	enable()
	require.NoError(t, ss.SetSessionSleeping(ctx, sid, true))
	none(state(), "the agent slept")
}
