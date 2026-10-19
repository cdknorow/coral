package background

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The hub keeps at most maxRemoteSessions sessions for one remote machine, from its list or its feed (#5099).
func TestRemoteSessionListIsCapped(t *testing.T) {
	rows := make([]map[string]any, 0, maxRemoteSessions+50)
	for i := 0; i < maxRemoteSessions+50; i++ {
		rows = append(rows, map[string]any{"session_id": fmt.Sprintf("s%d", i), "name": "n"})
	}
	body, err := json.Marshal(rows)
	require.NoError(t, err)
	list, err := parseLiveList(body)
	require.NoError(t, err)
	assert.Len(t, list, maxRemoteSessions)
	assert.Equal(t, "s0", list[0]["session_id"], "the first ones are kept, in order")
	// A row naming another server is dropped before the cap counts it.
	mixed := []map[string]any{{"session_id": "x", "server": "other"}, {"session_id": "y"}}
	assert.Len(t, filterLocalOnly(mixed), 1)
}

func TestRemoteFeedStopsGrowingAtTheCap(t *testing.T) {
	feed := map[string]map[string]any{}
	var order []string
	for i := 0; i < maxRemoteSessions+100; i++ {
		kept := addFeedSession(feed, &order, fmt.Sprintf("s%d", i), map[string]any{"i": i})
		assert.Equal(t, i < maxRemoteSessions, kept, "session %d", i)
	}
	assert.Len(t, feed, maxRemoteSessions)
	assert.Len(t, order, maxRemoteSessions)
	// A session already known still updates at the cap; a removed one frees a place.
	assert.True(t, addFeedSession(feed, &order, "s0", map[string]any{"i": -1}))
	assert.Equal(t, -1, feed["s0"]["i"])
	assert.False(t, addFeedSession(feed, &order, "new", map[string]any{}))
	delete(feed, "s1")
	assert.True(t, addFeedSession(feed, &order, "new", map[string]any{}))
	assert.Len(t, feed, maxRemoteSessions)
}
