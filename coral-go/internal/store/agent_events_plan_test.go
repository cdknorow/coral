package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3846 (#3833 item 3): the agent poll lists an agent's newest events. With no session given,
// the only index on agent_name was (agent_name, session_id), so SQLite read and sorted every one
// of the agent's events (10 ms for 6,000 on the Mac, against 0.15 ms by an index in time order).
// Each list is read from an index in time order: no sort.
func TestAgentEventListsNeedNoSort(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		_, err := db.ExecContext(ctx, `INSERT INTO agent_events (agent_name, session_id, event_type, summary, created_at) VALUES (?, ?, 'tool_use', 's', ?)`,
			fmt.Sprintf("agent-%d", i%5), fmt.Sprintf("sess-%d", i%10), fmt.Sprintf("2026-10-16T00:00:%02dZ", i%60))
		require.NoError(t, err)
	}
	_, err := db.ExecContext(ctx, "ANALYZE")
	require.NoError(t, err)
	const cols = `SELECT id, agent_name, session_id, event_type, tool_name, summary, detail_json, created_at, duration_ms, ref_id FROM agent_events`
	for _, c := range []struct {
		where string
		args  []any
	}{
		{" WHERE agent_name = ?", []any{"agent-1"}},
		{" WHERE agent_name = ? AND session_id = ?", []any{"agent-1", "sess-1"}},
		{" WHERE session_id = ?", []any{"sess-1"}},
	} {
		rows, err := db.Reader().QueryxContext(ctx, "EXPLAIN QUERY PLAN "+cols+c.where+" ORDER BY created_at DESC LIMIT ?", append(c.args, 50)...)
		require.NoError(t, err)
		var plan []string
		for rows.Next() {
			m := map[string]any{}
			require.NoError(t, rows.MapScan(m))
			plan = append(plan, fmt.Sprint(m["detail"]))
		}
		rows.Close()
		p := strings.Join(plan, "; ")
		assert.NotContains(t, p, "TEMP B-TREE", "%s: %s", c.where, p)
		assert.Contains(t, p, "USING INDEX", "%s: %s", c.where, p)
	}
}
