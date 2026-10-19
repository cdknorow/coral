package store

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// servedOrder is the agent list as the server serves it: by the time each place was taken (list_at), then the id.
func servedOrder(t *testing.T, ss *SessionStore) []string {
	t.Helper()
	at, err := ss.LiveListAt(context.Background())
	require.NoError(t, err)
	ids := make([]string, 0, len(at))
	for id := range at {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if at[ids[i]] != at[ids[j]] {
			return at[ids[i]] < at[ids[j]]
		}
		return ids[i] < ids[j]
	})
	return ids
}

// registerN registers the sessions one after another, each a moment after the last (the clock's tick can be coarse).
func registerN(t *testing.T, ss *SessionStore, ids ...string) {
	t.Helper()
	for _, id := range ids {
		require.NoError(t, ss.RegisterLiveSession(context.Background(), &LiveSession{SessionID: id, AgentType: "claude", AgentName: id, WorkingDir: "/tmp"}))
		time.Sleep(2 * time.Millisecond) // sleep-ok: distinct creation times
	}
}

func replace(t *testing.T, ss *SessionStore, old, new string) {
	t.Helper()
	require.NoError(t, ss.ReplaceLiveSession(context.Background(), old, &LiveSession{SessionID: new, AgentType: "claude", AgentName: new, WorkingDir: "/tmp"}))
	time.Sleep(2 * time.Millisecond) // sleep-ok: a new row's creation time is later than the replaced one's
}

// #4943: a restart or resume replaces the session with a new id; the agent keeps its place in the list.
func TestReplaceLiveSessionKeepsTheAgentsPlace(t *testing.T) {
	t.Run("the middle, first and last agent", func(t *testing.T) {
		ss := NewSessionStore(openTestDB(t))
		registerN(t, ss, "a", "b", "c")
		assert.Equal(t, []string{"a", "b", "c"}, servedOrder(t, ss))
		replace(t, ss, "b", "b2")
		assert.Equal(t, []string{"a", "b2", "c"}, servedOrder(t, ss), "the middle agent stays in the middle")
		replace(t, ss, "a", "a2")
		replace(t, ss, "c", "c2")
		assert.Equal(t, []string{"a2", "b2", "c2"}, servedOrder(t, ss))
		registerN(t, ss, "d")
		assert.Equal(t, []string{"a2", "b2", "c2", "d"}, servedOrder(t, ss), "a new agent still goes last")
	})
	t.Run("a chain of restarts", func(t *testing.T) {
		ss := NewSessionStore(openTestDB(t))
		registerN(t, ss, "a", "b", "c")
		cur := "b"
		for i := 0; i < 6; i++ {
			next := fmt.Sprintf("b%d", i)
			replace(t, ss, cur, next)
			cur = next
		}
		assert.Equal(t, []string{"a", cur, "c"}, servedOrder(t, ss))
	})
	t.Run("the run's own start time is the new one", func(t *testing.T) {
		ss := NewSessionStore(openTestDB(t))
		registerN(t, ss, "a", "b")
		before, err := ss.GetLiveSession(context.Background(), "a")
		require.NoError(t, err)
		replace(t, ss, "a", "a2")
		after, err := ss.GetLiveSession(context.Background(), "a2")
		require.NoError(t, err)
		assert.Greater(t, after.CreatedAt, before.CreatedAt, "created_at is when this run started (the never-started check and the duration read it); only the list place is carried")
	})
	t.Run("an old id that is not live gets its own place", func(t *testing.T) {
		ss := NewSessionStore(openTestDB(t))
		registerN(t, ss, "a")
		replace(t, ss, "never-lived", "x")
		assert.Equal(t, []string{"a", "x"}, servedOrder(t, ss))
	})
}

// The saved order is the order of the agents within a group; the new id takes the old one's place in it (#4943).
func TestReplaceLiveSessionCarriesTheSavedOrder(t *testing.T) {
	ctx := context.Background()
	t.Run("listed", func(t *testing.T) {
		ss := NewSessionStore(openTestDB(t))
		registerN(t, ss, "a", "b", "c")
		require.NoError(t, ss.SetSetting(ctx, SessionOrderSetting, `["c","a","b"]`))
		replace(t, ss, "a", "a2")
		got, err := ss.GetSettings(ctx)
		require.NoError(t, err)
		assert.JSONEq(t, `["c","a2","b"]`, got[SessionOrderSetting])
	})
	t.Run("not listed", func(t *testing.T) {
		ss := NewSessionStore(openTestDB(t))
		registerN(t, ss, "a", "b")
		require.NoError(t, ss.SetSetting(ctx, SessionOrderSetting, `["b"]`))
		replace(t, ss, "a", "a2")
		got, err := ss.GetSettings(ctx)
		require.NoError(t, err)
		assert.JSONEq(t, `["b"]`, got[SessionOrderSetting], "nothing to carry: the saved order is not rewritten")
	})
	t.Run("no saved order", func(t *testing.T) {
		ss := NewSessionStore(openTestDB(t))
		registerN(t, ss, "a", "b")
		replace(t, ss, "a", "a2")
		got, err := ss.GetSettings(ctx)
		require.NoError(t, err)
		assert.NotContains(t, got, SessionOrderSetting)
	})
	t.Run("the user's data is not rewritten when it is not a list of ids", func(t *testing.T) {
		for _, raw := range []string{`not json`, `{"a":1}`, `[1,2]`} {
			ss := NewSessionStore(openTestDB(t))
			registerN(t, ss, "a", "b")
			require.NoError(t, ss.SetSetting(ctx, SessionOrderSetting, raw))
			replace(t, ss, "a", "a2")
			got, err := ss.GetSettings(ctx)
			require.NoError(t, err)
			assert.Equal(t, raw, got[SessionOrderSetting])
		}
	})
}
