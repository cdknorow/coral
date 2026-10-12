package store

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/sessionstate"
)

// seedAgentEvents writes perSession events for each of n sessions in one
// transaction. The mix resembles a real log: mostly tool_use with a
// detail_json payload, turns bracketed by prompt_submit/stop, notifications,
// status/goal/confidence rows, some transcript events stamped out of id order
// and some timestamp ties. It returns the session ids.
func seedAgentEvents(t testing.TB, db *DB, n, perSession int, seed int64) []string {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	detail := fmt.Sprintf(`{"file_path":"/repo/f.go","command":%q}`, string(make([]byte, 400)))
	notes := []string{
		"Notification: Claude needs your permission to use Bash",
		"Notification: Claude is waiting for your input",
		"Notification: Claude Code login successful",
	}
	tx, err := db.Beginx()
	require.NoError(t, err)
	stmt, err := tx.Preparex(`INSERT INTO agent_events
		(agent_name, session_id, event_type, tool_name, summary, detail_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	require.NoError(t, err)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ids := make([]string, n)
	for s := 0; s < n; s++ {
		sid := fmt.Sprintf("sess-%04d", s)
		ids[s] = sid
		agent := fmt.Sprintf("agent-%d", s%(n/2+1)) // agents share names across sessions
		at := base
		for i := 0; i < perSession; i++ {
			at = at.Add(time.Duration(rng.Intn(3)) * time.Second) // ties happen
			stamp := at
			if rng.Intn(20) == 0 { // transcript event that happened earlier
				stamp = at.Add(-time.Duration(rng.Intn(30)) * time.Second)
			}
			var typ, summary string
			var tool, det any
			switch r := rng.Intn(100); {
			case r < 60:
				typ, summary, det = "tool_use", "Ran: ls", detail
				tool = []string{"Bash", "Read", "Grep", "Edit", "Write"}[rng.Intn(5)]
				if tool == "Edit" || tool == "Write" {
					det = fmt.Sprintf(`{"file_path":"/repo/f%d.go"}`, rng.Intn(40))
				}
			case r < 66:
				typ, summary = "prompt_submit", "User submitted prompt"
			case r < 72:
				typ, summary = "stop", "Agent stopped"
			case r < 80:
				typ, summary = "notification", notes[rng.Intn(len(notes))]
			case r < 81:
				typ, summary = "session_reset", "Session reset: /clear"
			case r < 90:
				typ, summary = "status", fmt.Sprintf("status %d", i)
			case r < 95:
				typ, summary = "goal", fmt.Sprintf("goal %d", i)
			default:
				typ, summary = "confidence", "high"
			}
			_, err := stmt.Exec(agent, sid, typ, tool, summary, det, stamp.Format(ISOFormat))
			require.NoError(t, err)
		}
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
	return ids
}

// Reference implementations: the queries before #2917, kept to prove the
// rewritten ones return the same answers.

func legacyLatestEventTypes(t testing.TB, db *DB, ids []string) map[string][2]string {
	query, args, err := sqlx.In(`SELECT session_id, event_type, summary FROM agent_events
		 WHERE session_id IN (?) AND event_type NOT IN ('status', 'goal', 'confidence')
		 ORDER BY created_at DESC`, ids)
	require.NoError(t, err)
	var rows []struct {
		SessionID string `db:"session_id"`
		EventType string `db:"event_type"`
		Summary   string `db:"summary"`
	}
	require.NoError(t, db.Select(&rows, query, args...))
	out := map[string][2]string{}
	for _, r := range rows {
		if _, ok := out[r.SessionID]; !ok {
			out[r.SessionID] = [2]string{r.EventType, r.Summary}
		}
	}
	return out
}

func legacyLatestGoals(t testing.TB, db *DB, ids []string) map[string]string {
	query, args, err := sqlx.In(`SELECT session_id, summary FROM agent_events
		 WHERE session_id IN (?) AND event_type = 'goal'
		 ORDER BY created_at DESC, id DESC`, ids)
	require.NoError(t, err)
	var rows []struct {
		SessionID string `db:"session_id"`
		Summary   string `db:"summary"`
	}
	require.NoError(t, db.Select(&rows, query, args...))
	out := map[string]string{}
	for _, r := range rows {
		if _, ok := out[r.SessionID]; !ok {
			out[r.SessionID] = r.Summary
		}
	}
	return out
}

func legacyStateEvents(t testing.TB, db *DB, ids []string) map[string][]AgentEvent {
	query, args, err := sqlx.In(`SELECT id, agent_name, session_id, event_type, tool_name, summary, detail_json, created_at
		 FROM agent_events
		 WHERE session_id IN (?)
		   AND event_type IN ('notification', 'stop', 'prompt_submit', 'tool_use', 'session_reset')
		 ORDER BY session_id, created_at ASC, id ASC`, ids)
	require.NoError(t, err)
	var rows []AgentEvent
	require.NoError(t, db.Select(&rows, query, args...))
	out := map[string][]AgentEvent{}
	for _, id := range ids {
		out[id] = []AgentEvent{}
	}
	for _, ev := range rows {
		out[*ev.SessionID] = append(out[*ev.SessionID], ev)
	}
	return out
}

// derived is everything a consumer reads from the state events: the derived
// state and the newest timestamp (the transcript-merge cutoff).
type derived struct {
	state  sessionstate.State
	newest string
}

func deriveEvents(events []AgentEvent) derived {
	in := sessionstate.Input{StalenessSeconds: 1}
	var newest string
	for _, ev := range events {
		in.Events = append(in.Events, sessionstate.Event{Type: ev.EventType, Summary: ev.Summary})
		if ev.CreatedAt > newest {
			newest = ev.CreatedAt
		}
	}
	return derived{sessionstate.Derive(in), newest}
}

func requireSameLatest(t *testing.T, db *DB, ts *TaskStore, ids []string) {
	t.Helper()
	ctx := context.Background()
	gotTypes, err := ts.GetLatestEventTypes(ctx, ids)
	require.NoError(t, err)
	require.Equal(t, legacyLatestEventTypesStable(t, db, ids), gotTypes)

	gotGoals, err := ts.GetLatestGoals(ctx, ids)
	require.NoError(t, err)
	require.Equal(t, legacyLatestGoals(t, db, ids), gotGoals)

	gotState, err := ts.GetSessionStateEvents(ctx, ids)
	require.NoError(t, err)
	want := legacyStateEvents(t, db, ids)
	require.Len(t, gotState, len(want))
	for _, id := range ids {
		require.Contains(t, gotState, id)
		require.Equal(t, deriveEvents(want[id]), deriveEvents(gotState[id]), "session %s", id)
		// The result is a suffix of the full history, in the same order.
		got, full := gotState[id], want[id]
		require.LessOrEqual(t, len(got), len(full))
		tail := full[len(full)-len(got):]
		for i := range got {
			require.Equal(t, tail[i].ID, got[i].ID, "session %s", id)
		}
	}
}

// legacyLatestEventTypesStable is the legacy answer with timestamp ties broken
// by the newest id. The legacy ORDER BY created_at DESC left ties to the query
// plan; the rewrite breaks them deterministically, as GetLatestGoals did.
func legacyLatestEventTypesStable(t testing.TB, db *DB, ids []string) map[string][2]string {
	query, args, err := sqlx.In(`SELECT session_id, event_type, summary FROM agent_events
		 WHERE session_id IN (?) AND event_type NOT IN ('status', 'goal', 'confidence')
		 ORDER BY created_at DESC, id DESC`, ids)
	require.NoError(t, err)
	var rows []struct {
		SessionID string `db:"session_id"`
		EventType string `db:"event_type"`
		Summary   string `db:"summary"`
	}
	require.NoError(t, db.Select(&rows, query, args...))
	out := map[string][2]string{}
	for _, r := range rows {
		if _, ok := out[r.SessionID]; !ok {
			out[r.SessionID] = [2]string{r.EventType, r.Summary}
		}
	}
	return out
}

func TestLatestAgentEventQueriesMatchFullHistory(t *testing.T) {
	db := openTestDB(t)
	ts := NewTaskStore(db)
	ids := seedAgentEvents(t, db, 40, 300, 1)
	// Sessions with no events, with only notifications, and unknown ids.
	ctx := context.Background()
	only := "notes-only"
	for _, s := range []string{"Notification: Claude needs your permission to use Bash", "Notification: Claude Code login successful"} {
		_, err := ts.InsertAgentEvent(ctx, &AgentEvent{AgentName: "a", SessionID: &only, EventType: "notification", Summary: s})
		require.NoError(t, err)
	}
	ids = append(ids, only, "no-events")
	requireSameLatest(t, db, ts, ids)
	requireSameLatest(t, db, ts, append(ids, ids[:3]...)) // repeated ids
	requireSameLatest(t, db, ts, ids[:1])
	requireSameLatest(t, db, ts, []string{"no-events"})
}

func TestSessionStateEventsStartAtLastTurnBoundary(t *testing.T) {
	db := openTestDB(t)
	ts := NewTaskStore(db)
	ctx := context.Background()
	sid := "s1"
	add := func(typ, summary, at string) {
		_, err := ts.InsertAgentEvent(ctx, &AgentEvent{AgentName: "a", SessionID: &sid, EventType: typ, Summary: summary, CreatedAt: at})
		require.NoError(t, err)
	}
	add("prompt_submit", "p", "2026-10-01T00:00:01.000000Z")
	add("tool_use", "t", "2026-10-01T00:00:02.000000Z")
	add("stop", "s", "2026-10-01T00:00:03.000000Z")
	add("notification", "Notification: Claude is waiting for your input", "2026-10-01T00:00:04.000000Z")
	// A transcript event stamped earlier than the stop does not move the boundary.
	add("tool_use", "late", "2026-10-01T00:00:02.500000Z")

	got, err := ts.GetSessionStateEvents(ctx, []string{sid})
	require.NoError(t, err)
	var types []string
	for _, ev := range got[sid] {
		types = append(types, ev.EventType)
	}
	require.Equal(t, []string{"stop", "notification"}, types)
}

// TestAgentEventsLatestPerf times the per-tick agent_events queries at hub
// scale. Skipped unless CORAL_PERF_EVENTS is set.
func TestAgentEventsLatestPerf(t *testing.T) {
	if os.Getenv("CORAL_PERF_EVENTS") == "" {
		t.Skip("set CORAL_PERF_EVENTS=1 to run")
	}
	db := openTestDB(t)
	ts := NewTaskStore(db)
	ids := seedAgentEvents(t, db, 50, 500, 7)
	_, err := db.Exec("ANALYZE")
	require.NoError(t, err)
	ctx := context.Background()
	timeIt := func(name string, fn func() int) {
		rows := fn()
		var d []time.Duration
		for i := 0; i < 20; i++ {
			start := time.Now()
			fn()
			d = append(d, time.Since(start))
		}
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		t.Logf("%-24s p50=%-10s max=%-10s rows=%d", name, d[len(d)/2].Round(10*time.Microsecond), d[len(d)-1].Round(10*time.Microsecond), rows)
	}
	timeIt("GetLatestEventTypes", func() int { m, _ := ts.GetLatestEventTypes(ctx, ids); return len(m) })
	timeIt("GetLatestGoals", func() int { m, _ := ts.GetLatestGoals(ctx, ids); return len(m) })
	timeIt("GetSessionStateEvents", func() int {
		m, _ := ts.GetSessionStateEvents(ctx, ids)
		n := 0
		for _, evs := range m {
			n += len(evs)
		}
		return n
	})
	timeIt("GetEditedFileCounts", func() int { m, _ := ts.GetEditedFileCounts(ctx, ids); return len(m) })
	timeIt("GetAllEditedFileCounts", func() int { m, _ := ts.GetAllEditedFileCounts(ctx); return len(m) })
	agent, sid := "agent-1", ids[1]
	timeIt("InsertAgentEvent(+prune)", func() int {
		_, _ = ts.InsertAgentEvent(ctx, &AgentEvent{AgentName: agent, SessionID: &sid, EventType: "tool_use", Summary: "x"})
		return 1
	})
	timeIt("InsertAgentEvent(no sid)", func() int {
		_, _ = ts.InsertAgentEvent(ctx, &AgentEvent{AgentName: agent, EventType: "status", Summary: "x"})
		return 1
	})
}

// A Write/Edit row with malformed detail_json (possible in databases written
// by older or legacy writers) must neither stop the edited-file index from
// being built at startup nor make the event's INSERT fail.
func TestEditedFilesIndexToleratesMalformedDetailJSON(t *testing.T) {
	path := t.TempDir() + "/sessions.db"
	db, err := Open(path)
	require.NoError(t, err)
	// A database from before the index, holding a malformed row.
	_, err = db.Exec(`DROP INDEX idx_agent_events_edited_files`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO agent_events (agent_name, session_id, event_type, tool_name, summary, detail_json, created_at)
		VALUES ('a', 's1', 'tool_use', 'Write', 'w', '{not json', '2026-10-01T00:00:00Z')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	db, err = Open(path)
	require.NoError(t, err, "startup must survive a malformed detail_json row")
	t.Cleanup(func() { db.Close() })
	ts := NewTaskStore(db)
	ctx := context.Background()
	sid := "s1"
	_, err = ts.InsertAgentEvent(ctx, &AgentEvent{AgentName: "a", SessionID: &sid, EventType: "tool_use", ToolName: strPtr("Edit"), Summary: "e", DetailJSON: strPtr("{also not json")})
	require.NoError(t, err, "a malformed event is still recorded")
	_, err = ts.InsertAgentEvent(ctx, &AgentEvent{AgentName: "a", SessionID: &sid, EventType: "tool_use", ToolName: strPtr("Edit"), Summary: "e", DetailJSON: strPtr(`{"file_path":"/r/a.go"}`)})
	require.NoError(t, err)

	// Malformed rows are skipped; the well-formed edit is counted.
	counts, err := ts.GetEditedFileCounts(ctx, []string{sid})
	require.NoError(t, err)
	require.Equal(t, map[string]int{sid: 1}, counts)
	all, err := ts.GetAllEditedFileCounts(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]int{sid: 1}, all)
	files, err := ts.GetEditedFilesBySession(ctx, []string{sid})
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"/r/a.go": true}, files[sid])
}
