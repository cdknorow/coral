package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLiveRefreshScopedCountsAndGitFallback(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	tasks, git := NewTaskStore(db), NewGitStore(db)
	for _, id := range []string{"awake", "sleeping", "historical"} {
		for _, file := range []string{"a.go", "a.go", "b.go"} {
			_, err := db.Exec(`INSERT INTO agent_events(agent_name,session_id,event_type,tool_name,summary,detail_json,created_at) VALUES('repo',?,'tool_use','Edit','',?,'2026-10-02')`, id, fmt.Sprintf(`{"file_path":%q}`, file))
			require.NoError(t, err)
		}
	}
	counts, err := tasks.GetEditedFileCounts(ctx, []string{"awake"})
	require.NoError(t, err)
	require.Equal(t, map[string]int{"awake": 2}, counts)
	empty, err := tasks.GetEditedFileCounts(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
	for i, id := range []string{"awake", "historical", "awake"} {
		_, err := db.Exec(`INSERT INTO git_snapshots(agent_name,agent_type,working_directory,branch,commit_hash,session_id,recorded_at) VALUES('repo','codex','/repo',?,?,?,?)`, fmt.Sprintf("branch-%d", i), fmt.Sprint(i), id, fmt.Sprintf("2026-10-02T00:00:0%dZ", i))
		require.NoError(t, err)
	}
	rows, err := git.GetLiveGitState(ctx, map[string]string{"awake": "repo", "new": "repo", "unknown": "other"})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, "branch-2", rows["awake"].Branch)
	require.Equal(t, "branch-2", rows["new"].Branch, "preserve agent-name fallback")
	require.NotContains(t, rows, "historical")
	// No cached count may conceal a newly written event.
	_, err = db.Exec(`INSERT INTO agent_events(agent_name,session_id,event_type,tool_name,summary,detail_json,created_at) VALUES('repo','awake','tool_use','Write','', '{"file_path":"c.go"}', '2026-10-02')`)
	require.NoError(t, err)
	counts, err = tasks.GetEditedFileCounts(ctx, []string{"awake"})
	require.NoError(t, err)
	require.Equal(t, 3, counts["awake"])
}

func BenchmarkLiveRefreshFileCounts(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "profile.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO agent_events(agent_name,session_id,event_type,tool_name,summary,detail_json,created_at) VALUES('repo',?,'tool_use','Edit','',?,'2026-10-02')`)
	if err != nil {
		b.Fatal(err)
	}
	var live []string
	for session := 0; session < 226; session++ {
		id := fmt.Sprintf("session-%d", session)
		if session < 26 {
			live = append(live, id)
		}
		for event := 0; event < 100; event++ {
			if _, err := statement.Exec(id, fmt.Sprintf(`{"file_path":"file-%d.go"}`, event%20)); err != nil {
				b.Fatal(err)
			}
		}
	}
	statement.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	tasks := NewTaskStore(db)
	for _, mode := range []string{"all_history", "live_only"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var err error
				if mode == "all_history" {
					_, err = tasks.GetAllEditedFileCounts(context.Background())
				} else {
					_, err = tasks.GetEditedFileCounts(context.Background(), live)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
