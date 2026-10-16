package store

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// referenceEditedFileCounts is the count as the live list computed it before #3846.
func referenceEditedFileCounts(t *testing.T, db *DB) map[string]int {
	t.Helper()
	var rows []struct {
		SessionID string `db:"session_id"`
		Count     int    `db:"cnt"`
	}
	require.NoError(t, db.SelectContext(context.Background(), &rows, `SELECT session_id, COUNT(DISTINCT json_extract(detail_json, '$.file_path')) AS cnt
		FROM agent_events WHERE event_type = 'tool_use' AND tool_name IN ('Write', 'Edit')
		AND detail_json IS NOT NULL AND json_valid(detail_json) AND session_id IS NOT NULL GROUP BY session_id`))
	out := map[string]int{}
	for _, r := range rows {
		if r.Count > 0 {
			out[r.SessionID] = r.Count
		}
	}
	return out
}

// #3846: the kept counts equal the aggregate after any mix of writes, by any writer: the store's
// inserts and prunes, clears, and raw INSERT, UPDATE and DELETE statements.
func TestEditedFileCountsMatchTheAggregate(t *testing.T) {
	db := openTestDB(t)
	ts := NewTaskStore(db)
	ts.eventKeep.Store(20) // prunes often
	ctx := context.Background()
	rng := rand.New(rand.NewSource(3846))
	sessions := []string{"s1", "s2", "s3"}
	tools := []string{"Edit", "Write", "Read", "Bash"}
	detail := func() string {
		switch rng.Intn(6) {
		case 0:
			return `not json`
		case 1:
			return `{"command":"ls"}`
		}
		return fmt.Sprintf(`{"file_path":"/f%d.go"}`, rng.Intn(8))
	}
	for i := 0; i < 600; i++ {
		sid := sessions[rng.Intn(len(sessions))]
		switch r := rng.Intn(10); {
		case r < 6:
			d, tool := detail(), tools[rng.Intn(len(tools))]
			_, err := ts.InsertAgentEvent(ctx, &AgentEvent{AgentName: "a" + sid, SessionID: &sid, EventType: "tool_use", ToolName: &tool, Summary: "x", DetailJSON: &d})
			require.NoError(t, err)
		case r < 7:
			_, err := db.ExecContext(ctx, `INSERT INTO agent_events (agent_name, session_id, event_type, tool_name, summary, detail_json, created_at) VALUES ('raw', ?, 'tool_use', 'Edit', 'x', ?, '2026-01-01')`, sid, detail())
			require.NoError(t, err)
		case r < 8:
			_, err := db.ExecContext(ctx, `UPDATE agent_events SET detail_json = ?, tool_name = ? WHERE id = (SELECT id FROM agent_events ORDER BY random() LIMIT 1)`, detail(), tools[rng.Intn(len(tools))])
			require.NoError(t, err)
		case r < 9:
			_, err := db.ExecContext(ctx, `DELETE FROM agent_events WHERE id = (SELECT id FROM agent_events ORDER BY random() LIMIT 1)`)
			require.NoError(t, err)
		default:
			if rng.Intn(10) == 0 {
				require.NoError(t, ts.ClearAgentEvents(ctx, "a"+sid, &sid))
			}
		}
		if i%25 == 0 {
			got, err := ts.GetEditedFileCounts(ctx, sessions)
			require.NoError(t, err)
			require.Equal(t, referenceEditedFileCounts(t, db), got, "after %d writes", i)
		}
	}
	all, err := ts.GetAllEditedFileCounts(ctx)
	require.NoError(t, err)
	require.Equal(t, referenceEditedFileCounts(t, db), all)
}

// A database from before #3846 gets the table filled from its events on open.
func TestEditedFileCountsAreFilledOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := OpenWithKey(context.Background(), path, "")
	require.NoError(t, err)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		_, err := db.ExecContext(ctx, `INSERT INTO agent_events (agent_name, session_id, event_type, tool_name, summary, detail_json, created_at) VALUES ('a', ?, 'tool_use', 'Edit', 'x', ?, '2026-01-01')`,
			fmt.Sprintf("s%d", i%3), fmt.Sprintf(`{"file_path":"/f%d.go"}`, i%7))
		require.NoError(t, err)
	}
	want := referenceEditedFileCounts(t, db)
	for _, s := range []string{"DROP TRIGGER agent_event_files_insert", "DROP TRIGGER agent_event_files_delete", "DROP TRIGGER agent_event_files_update_old", "DROP TRIGGER agent_event_files_update_new", "DROP TABLE agent_event_files"} {
		_, err := db.ExecContext(ctx, s)
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())
	db, err = OpenWithKey(context.Background(), path, "")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	got, err := NewTaskStore(db).GetEditedFileCounts(ctx, []string{"s0", "s1", "s2"})
	require.NoError(t, err)
	require.Equal(t, want, got)
}
