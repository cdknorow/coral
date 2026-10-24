package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// #6810: what #6745's two reads (SessionsWithAnyEvent, SessionsOfFinishedRuns) cost on every live list and feed build, next to the read that was already there (GetLatestEventTypes), at 20, 50 and 100
// live agents on a database with history: 300 events per live session, 200 historical sessions of 100 events, and 2000 scheduled runs (a tenth of them the live sessions').
func BenchmarkStartedReads(b *testing.B) {
	for _, live := range []int{20, 50, 100} {
		b.Run(fmt.Sprintf("agents=%d", live), func(b *testing.B) {
			ctx := context.Background()
			db, err := OpenWithKey(ctx, filepath.Join(b.TempDir(), "profile.db"), "")
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			tx, err := db.Begin()
			if err != nil {
				b.Fatal(err)
			}
			ev, err := tx.Prepare(`INSERT INTO agent_events(agent_name,session_id,event_type,tool_name,summary,detail_json,created_at) VALUES('fixture',?,?,'Edit','x','{}',?)`)
			if err != nil {
				b.Fatal(err)
			}
			var ids []string
			for s := 0; s < live+200; s++ {
				id := fmt.Sprintf("session-%d", s)
				if s < live {
					ids = append(ids, id)
				}
				n := 100
				if s < live {
					n = 300
				}
				for e := 0; e < n; e++ {
					typ := "tool_use"
					if e%7 == 0 {
						typ = "status"
					}
					if _, err := ev.Exec(id, typ, fmt.Sprintf("2026-10-%02dT10:%02d:%02dZ", 1+e%28, e%60, e%60)); err != nil {
						b.Fatal(err)
					}
				}
			}
			ev.Close()
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			sched := NewScheduleStore(db)
			job, err := sched.CreateScheduledJob(ctx, &ScheduledJob{Name: "j", CronExpr: "0 9 * * *", RepoPath: "/r", Prompt: "p", AgentType: "claude"})
			if err != nil {
				b.Fatal(err)
			}
			for r := 0; r < 2000; r++ {
				id, err := sched.CreateScheduledRun(ctx, job.ID, "2026-10-01T09:00:00Z", "completed")
				if err != nil {
					b.Fatal(err)
				}
				sid := fmt.Sprintf("old-run-session-%d", r)
				if r%10 == 0 && r/10 < live {
					sid = fmt.Sprintf("session-%d", r/10)
				}
				if err := sched.UpdateScheduledRun(ctx, id, map[string]interface{}{"session_id": sid}); err != nil {
					b.Fatal(err)
				}
			}
			tasks := NewTaskStore(db)
			b.Run("GetLatestEventTypes_existing", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if _, err := tasks.GetLatestEventTypes(ctx, ids); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("SessionsWithAnyEvent_new", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if _, err := tasks.SessionsWithAnyEvent(ctx, ids); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("SessionsOfFinishedRuns_new", func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if _, err := sched.SessionsOfFinishedRuns(ctx, ids); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
