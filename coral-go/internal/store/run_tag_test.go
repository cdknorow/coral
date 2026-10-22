package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #6141: a session's run tag is set once and stays: a second write never replaces it, a session with no row is left alone, and a session that never had one reads "".
func TestARunTagIsSetOnceAndKept(t *testing.T) {
	ss := NewSessionStore(openTestDB(t))
	ctx := context.Background()
	require.NoError(t, ss.RegisterLiveSession(ctx, &LiveSession{SessionID: "s1", AgentType: "claude", AgentName: "a", WorkingDir: "/tmp"}))
	assert.Equal(t, "", ss.RunTagOf(ctx, "s1"), "an agent started before the tag existed has none")
	require.NoError(t, ss.SetRunTagIfEmpty(ctx, "s1", "aaaa"))
	assert.Equal(t, "aaaa", ss.RunTagOf(ctx, "s1"))
	require.NoError(t, ss.SetRunTagIfEmpty(ctx, "s1", "bbbb"))
	assert.Equal(t, "aaaa", ss.RunTagOf(ctx, "s1"), "never replaced while the row lives")
	require.NoError(t, ss.SetRunTagIfEmpty(ctx, "no-such-session", "cccc"))
	assert.Equal(t, "", ss.RunTagOf(ctx, "no-such-session"))
	require.NoError(t, ss.SetRunTagIfEmpty(ctx, "s1", ""))
	assert.Equal(t, "aaaa", ss.RunTagOf(ctx, "s1"), "an empty tag changes nothing")
}
