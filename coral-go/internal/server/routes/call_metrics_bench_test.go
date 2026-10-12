package routes

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
)

// BenchmarkRequestMetrics measures what the metrics middleware adds to an /api
// request at the operator's target scale (50 live agents) on a database that
// already holds a long call_metrics history. The handler does one small read,
// as most /api handlers do, so the numbers include contention for SQLite's
// single connection.
func BenchmarkRequestMetrics(b *testing.B) {
	const agents, history = 50, 200_000
	db, err := store.Open(filepath.Join(b.TempDir(), "sessions.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx := db.MustBegin()
	for i := 0; i < agents; i++ {
		tx.MustExec(`INSERT INTO live_sessions(session_id, agent_type, agent_name, display_name, working_dir, created_at) VALUES(?,?,?,?,?,?)`,
			fmt.Sprintf("session-%d", i), "claude", fmt.Sprintf("agent-%d", i), fmt.Sprintf("Agent %d", i), "/tmp", now)
	}
	for i := 0; i < history; i++ {
		tx.MustExec(`INSERT INTO call_metrics(call_type,operation,agent_name,session_id,method,status_code,duration_ms,is_error,created_at) VALUES('api','/api/sessions/live/{name}/poll',?,?,'GET',200,1,0,?)`,
			fmt.Sprintf("agent-%d", i%agents), fmt.Sprintf("session-%d", i%agents), now)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	for _, mode := range []string{"handler-only", "with-metrics"} {
		b.Run(mode, func(b *testing.B) {
			r := chi.NewRouter()
			if mode == "with-metrics" {
				r.Use(RequestMetrics(db))
			}
			r.Get("/api/sessions/live/{name}/poll", func(w http.ResponseWriter, r *http.Request) {
				var n int
				_ = db.GetContext(r.Context(), &n, `SELECT COUNT(*) FROM live_sessions`)
				w.WriteHeader(http.StatusOK)
			})
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					id := i % agents
					req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/sessions/live/agent-%d/poll?session_id=session-%d", id, id), nil)
					r.ServeHTTP(httptest.NewRecorder(), req)
					i++
				}
			})
		})
	}
}
