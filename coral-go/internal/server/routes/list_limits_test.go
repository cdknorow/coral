package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #3620 (hub audit H3): every list route a viewer can call bounds the size it was asked for. A
// missing, junk, zero or negative limit is the route's default (a negative SQLite LIMIT means no
// limit), anything above the route's maximum is the maximum, and a negative offset is zero.

func TestClampLimit(t *testing.T) {
	for _, c := range []struct{ n, def, max, want int }{
		{-1, 20, 200, 20}, {0, 20, 200, 20}, {1, 20, 200, 1}, {150, 20, 200, 150},
		{200, 20, 200, 200}, {201, 20, 200, 200}, {10000000, 20, 200, 200}, {-10000000, 50, 500, 50},
	} {
		assert.Equal(t, c.want, clampLimit(c.n, c.def, c.max), "clampLimit(%d, %d, %d)", c.n, c.def, c.max)
	}
	for _, c := range []struct{ n, want int }{{-5, 0}, {0, 0}, {7, 7}} {
		assert.Equal(t, c.want, clampOffset(c.n), "clampOffset(%d)", c.n)
	}
}

// A new list route can't skip the bound: every limit, events_limit or offset read in this package
// goes through clampLimit or clampOffset.
func TestListLimitsAlwaysClamped(t *testing.T) {
	read := regexp.MustCompile(`queryInt\(r, "(limit|offset|events_limit|[a-z_]+_limit)"`)
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var bad []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for i, line := range strings.Split(string(src), "\n") {
			if read.MatchString(line) && !strings.Contains(line, "clampLimit(") && !strings.Contains(line, "clampOffset(") {
				bad = append(bad, fmt.Sprintf("%s:%d: %s", f, i+1, strings.TrimSpace(line)))
			}
		}
	}
	assert.Empty(t, bad, "a list size or offset read without clampLimit/clampOffset (#3620)")
}

func TestListRoutesAreBounded(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	ctx := context.Background()
	dir := t.TempDir()
	db, err := storetest.Open(t, filepath.Join(dir, "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	bs, err := boardtest.Open(filepath.Join(dir, "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	cfg := &config.Config{LogDir: t.TempDir()}

	// Seed past every cap.
	const sid = "3620aaaa-0000-0000-0000-000000000001"
	gs := store.NewGitStore(db)
	ts := store.NewTaskStore(db)
	for i := 0; i < 205; i++ {
		s, when := sid, fmt.Sprintf("2026-10-15T10:%02d:%02dZ", i/60, i%60)
		require.NoError(t, gs.UpsertGitSnapshot(ctx, &store.GitSnapshot{AgentName: "a", AgentType: "claude", WorkingDirectory: dir,
			Branch: "main", CommitHash: fmt.Sprintf("%040x", i+1), CommitSubject: "c", CommitTimestamp: &when, SessionID: &s}))
		_, err := ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "agent-3620", SessionID: &s, EventType: "tool_use", Summary: "e"})
		require.NoError(t, err)
	}
	sched := store.NewScheduleStore(db)
	job, err := sched.CreateScheduledJob(ctx, &store.ScheduledJob{Name: "j", CronExpr: "0 * * * *", Timezone: "UTC", AgentType: "claude",
		RepoPath: dir, BaseBranch: "main", Prompt: "p", Enabled: 0, MaxDurationS: 60, Flags: "", JobType: "prompt"})
	require.NoError(t, err)
	ws := store.NewWorkflowStore(db)
	wf, err := ws.CreateWorkflow(ctx, &store.Workflow{Name: "wf", StepsJSON: `[{"name":"one","type":"shell","command":"true"}]`,
		DefaultAgentJSON: "{}", BaseBranch: "main", MaxDurationS: 60, Enabled: 1})
	require.NoError(t, err)
	for i := 0; i < 205; i++ {
		_, err := sched.CreateScheduledRun(ctx, job.ID, fmt.Sprintf("2026-10-15T10:%02d:%02dZ", i/60, i%60), "completed")
		require.NoError(t, err)
		_, err = ws.CreateWorkflowRun(ctx, wf.ID, "manual", nil)
		require.NoError(t, err)
	}
	// The local read follows a subscriber's cursor (each read moves it on), so the reader joins
	// first and there are enough messages for every case: 4 x 50 + 500 = 700 of 1200.
	_, err = bs.Subscribe(ctx, "team-3620", "reader", "QA Engineer", "", nil, "all")
	require.NoError(t, err)
	for i := 0; i < 1200; i++ {
		_, err := bs.PostMessage(ctx, "team-3620", "poster", fmt.Sprintf("m%d", i), nil)
		require.NoError(t, err)
	}

	r := chi.NewRouter()
	hh := NewHistoryHandler(db, cfg, bs)
	sh := NewScheduleHandler(db, cfg)
	wh := NewWorkflowHandler(db, cfg)
	bh := NewBoardHandler(bs)
	r.Get("/api/sessions/history/{sessionID}/git", hh.GetSessionGit)
	r.Get("/api/sessions/history/{sessionID}/events", hh.GetSessionEvents)
	r.Get("/api/scheduled/jobs/{jobID}/runs", sh.GetJobRuns)
	r.Get("/api/scheduled/runs/recent", sh.GetRecentRuns)
	r.Get("/api/workflows/{workflowID}/runs", wh.ListWorkflowRuns)
	r.Get("/api/workflows/runs/recent", wh.ListRecentRuns)
	r.Get("/api/board/{project}/messages", bh.ReadMessages)
	r.Get("/api/board/{project}/messages/all", bh.ListAllMessages)

	count := func(t *testing.T, body []byte) int {
		t.Helper()
		var arr []json.RawMessage
		if json.Unmarshal(body, &arr) == nil {
			return len(arr)
		}
		var obj map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(body, &obj), string(body))
		for _, k := range []string{"commits", "runs", "messages", "events"} {
			if v, ok := obj[k]; ok {
				require.NoError(t, json.Unmarshal(v, &arr), k)
				return len(arr)
			}
		}
		t.Fatalf("no list in %.200s", body)
		return 0
	}
	routes := []struct {
		name, path   string
		def, max     int
		tunneled     bool
		offsetParams bool
	}{
		{"history git", "/api/sessions/history/" + sid + "/git", 20, 200, false, false},
		{"history events", "/api/sessions/history/" + sid + "/events", 200, 200, false, false},
		{"job runs", fmt.Sprintf("/api/scheduled/jobs/%d/runs", job.ID), 20, 200, false, false},
		{"recent job runs", "/api/scheduled/runs/recent", 50, 200, false, false},
		{"workflow runs", fmt.Sprintf("/api/workflows/%d/runs", wf.ID), 20, 200, false, true},
		{"recent workflow runs", "/api/workflows/runs/recent", 20, 200, false, true},
		{"board messages (local)", "/api/board/team-3620/messages?subscriber_id=reader&all=1", 50, 500, false, false},
		{"board messages (through the hub)", "/api/board/team-3620/messages", 50, 500, true, false},
		{"board messages/all", "/api/board/team-3620/messages/all", 200, 500, false, true},
	}
	for _, rt := range routes {
		for _, c := range []struct {
			limit string
			want  func(def, max int) int
		}{
			{"", func(d, _ int) int { return d }},
			{"-1", func(d, _ int) int { return d }},
			{"0", func(d, _ int) int { return d }},
			{"junk", func(d, _ int) int { return d }},
			{"10000000", func(_, m int) int { return m }},
		} {
			url := rt.path
			sep := "?"
			if strings.Contains(url, "?") {
				sep = "&"
			}
			if c.limit != "" {
				url += sep + "limit=" + c.limit
				sep = "&"
			}
			if rt.offsetParams {
				url += sep + "offset=-5"
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if rt.tunneled {
				req.Header.Set(tunnel.ViaHeader, "1")
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, "%s limit=%q: %s", rt.name, c.limit, rec.Body.String())
			assert.Equal(t, c.want(rt.def, rt.max), count(t, rec.Body.Bytes()), "%s limit=%q", rt.name, c.limit)
		}
	}
}
