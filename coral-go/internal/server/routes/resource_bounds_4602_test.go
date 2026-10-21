package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #4602: a list size a caller asks for is bounded. A huge ?limit used to be the whole list, or the whole transcript and a copy of it
// in memory, for the Operator, an agent token and a viewer through Coral Hub alike.

func TestCrossBoardTaskListIsCappedAt500(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	e := newTQEnv(t)
	for i := 0; i < 520; i++ {
		e.add("alpha", fmt.Sprintf("task %03d", i), "body", "low", "Orchestrator", "", "")
	}
	for _, q := range []string{"limit=100000", "limit=501", "limit=-5", "limit=0"} {
		resp, err := http.Get(e.srv.URL + "/api/board/tasks?" + q)
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var out struct {
			Tasks []map[string]any `json:"tasks"`
		}
		require.NoError(t, json.Unmarshal(b, &out), q)
		want := 500
		if q == "limit=-5" || q == "limit=0" {
			want = 100 // a junk or non-positive value is the default
		}
		assert.Len(t, out.Tasks, want, q)
	}
}

func TestBoardTaskListFullIsCappedAndSaysSo(t *testing.T) {
	t.Parallel() // no shared state: these ran one after another (#5622)
	e := newTQEnv(t)
	r := chi.NewRouter()
	r.Get("/api/board/{project}/tasks", NewBoardHandler(e.bs).ListTasks)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	get := func(project string) (int, bool) {
		resp, err := http.Get(srv.URL + "/api/board/" + project + "/tasks?full=1")
		require.NoError(t, err)
		defer resp.Body.Close()
		var out struct {
			Tasks     []map[string]any `json:"tasks"`
			Truncated bool             `json:"truncated"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
		return len(out.Tasks), out.Truncated
	}
	for i := 0; i < 500; i++ {
		e.add("exact", fmt.Sprintf("t%d", i), "b", "low", "Orchestrator", "", "")
	}
	n, cut := get("exact")
	assert.Equal(t, 500, n)
	assert.False(t, cut, "exactly the cap is not a cut")
	e.add("exact", "one more", "b", "low", "Orchestrator", "", "")
	n, cut = get("exact")
	assert.Equal(t, 500, n)
	assert.True(t, cut, "past the cap the reply says it was cut")
	e.add("small", "only", "b", "low", "Orchestrator", "", "")
	n, cut = get("small")
	assert.Equal(t, 1, n)
	assert.False(t, cut)
}

func TestChatPageLimitIsCappedAt500(t *testing.T) {
	server, _, _, _ := setupSessionsTestServer(t)
	dir := t.TempDir()
	projectDir := filepath.Join(dir, "project")
	require.NoError(t, os.MkdirAll(projectDir, 0755))
	t.Setenv("CLAUDE_PROJECTS_DIR", dir)
	sid := "00000000-0000-0000-0000-000000004602"
	var b strings.Builder
	for i := 0; i < 650; i++ {
		line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": fmt.Sprintf("message %d", i)}})
		b.Write(line)
		b.WriteString("\n")
	}
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, sid+".jsonl"), []byte(b.String()), 0644))
	count := func(q string) int {
		resp, err := http.Get(fmt.Sprintf("%s/api/sessions/live/claude-%s/chat?session_id=%s&after=0&%s", server.URL, sid, sid, q))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Messages []map[string]any `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return len(body.Messages)
	}
	assert.Equal(t, 100, count(""), "the default page")
	assert.Equal(t, 500, count("limit=100000"), "a huge limit is the cap, not the whole transcript")
	assert.Equal(t, 500, count("limit=501"))
	assert.Equal(t, 30, count("limit=30"))
}

func TestHistoryPageLimitIsCappedAt500(t *testing.T) {
	_, h, ss, codexHome, _, _ := setupHistoryVerificationServer(t)
	ctx := context.Background()
	dayDir := filepath.Join(codexHome, "sessions", "2026", "10", "16")
	require.NoError(t, os.MkdirAll(dayDir, 0755))
	sid := "f6a7b8c9-d0e1-2345-f012-3456789a4602"
	var b strings.Builder
	b.WriteString(`{"timestamp":"2026-10-16T10:00:00.000Z","type":"session_meta","payload":{"id":"rollout-cap","cwd":"/repo"}}` + "\n")
	fmt.Fprintf(&b, `{"timestamp":"2026-10-16T10:00:00.100Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Coral session metadata:\n%s %s\n"}]}}`+"\n", at.CoralSessionMarkerPrefix, sid)
	for i := 0; i < 650; i++ {
		fmt.Fprintf(&b, `{"timestamp":"2026-10-16T10:00:%02d.000Z","type":"event_msg","payload":{"type":"user_message","message":"message %d","images":[]}}`+"\n", i%60, i)
	}
	rollout := filepath.Join(dayDir, "rollout-cap.jsonl")
	require.NoError(t, os.WriteFile(rollout, []byte(b.String()), 0644))
	require.NoError(t, ss.UpsertSessionIndex(ctx, &store.SessionIndex{SessionID: sid, SourceType: "codex", SourceFile: rollout}))
	r := chi.NewRouter()
	r.Get("/api/sessions/history/{sessionID}", h.GetSessionDetail)
	count := func(q string) (int, int) {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions/history/"+sid+q, nil))
		require.Equal(t, http.StatusOK, rec.Code, q)
		var p struct {
			Messages []map[string]any `json:"messages"`
			Total    int              `json:"total"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
		return len(p.Messages), p.Total
	}
	n, total := count("?limit=100000")
	assert.Equal(t, 500, n, "a huge limit is the cap")
	assert.Equal(t, 650, total, "the total still says how many there are")
	n, _ = count("?limit=40")
	assert.Equal(t, 40, n)
}

func TestClampTerminalSize(t *testing.T) {
	for _, c := range []struct{ cols, rows, wantCols, wantRows int }{
		{120, 40, 120, 40},
		{120, 0, 120, 50},        // no rows: the default
		{120, -1, 120, 50},       // negative rows: the default, never a wrapped uint16
		{5000, 99999, 1000, 300}, // both over: the caps
		{65535, 65535, 1000, 300},
		{70000, 70000, 1000, 300}, // past uint16: no wrap to a small number
		{1000, 300, 1000, 300},
		{1001, 301, 1000, 300},
		{9, 24, 10, 24}, // under the minimum (the callers refuse this first)
	} {
		cols, rows := clampTerminalSize(c.cols, c.rows)
		assert.EqualValues(t, c.wantCols, cols, "cols %d", c.cols)
		assert.EqualValues(t, c.wantRows, rows, "rows %d", c.rows)
	}
}

func TestResizeRouteRefusesAnAbsurdWidth(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	const sid = "00000000-0000-0000-0000-000000004603"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "repo", WorkingDir: "/tmp/test"}))
	terminal.addSession("claude-"+sid, "/tmp/test")
	post := func(cols int) int {
		body := strings.NewReader(fmt.Sprintf(`{"columns": %d, "agent_type": "claude", "session_id": "%s"}`, cols, sid))
		resp, err := http.Post(server.URL+"/api/sessions/live/claude-"+sid+"/resize", "application/json", body)
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusOK, post(120))
	assert.Equal(t, http.StatusOK, post(1000))
	assert.Equal(t, http.StatusBadRequest, post(1001))
	assert.Equal(t, http.StatusBadRequest, post(65535))
	assert.Equal(t, http.StatusBadRequest, post(9))
}

// #4602: one launch-team request starts at most 32 agents.
func TestLaunchTeamCapsAgentsPerRequest(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	cfg := &config.Config{LogDir: t.TempDir()}
	cfg.UseCoralDir(t.TempDir())
	handler := NewSessionsHandler(db, cfg, nil, newMockTerminal(), bs)
	launch := func(n int) (int, string) {
		agents := make([]map[string]any, n)
		for i := range agents {
			agents[i] = map[string]any{"name": fmt.Sprintf("Agent %d", i), "prompt": "work"}
		}
		b, _ := json.Marshal(map[string]any{"board_name": "captest", "working_dir": t.TempDir(), "agents": agents})
		rec := httptest.NewRecorder()
		handler.LaunchTeam(rec, httptest.NewRequest(http.MethodPost, "/x", bytes.NewReader(b)))
		return rec.Code, rec.Body.String()
	}
	code, body := launch(maxLaunchTeamAgents + 1)
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Contains(t, body, "too_many_agents")
	assert.Contains(t, body, `"max":32`)
	_, body = launch(maxLaunchTeamAgents)
	assert.NotContains(t, body, "too_many_agents", "the cap itself is allowed")
}

// #4602: at most two team generations run at once, and the finished jobs kept are bounded.
func TestTeamGenerationIsCappedAtTwoRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary")
	}
	h := newSystemHandlerForTest(t)
	fake := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 3\n"), 0o755))
	require.NoError(t, store.NewSessionStore(h.db).SetSetting(context.Background(), agent.CLIPathSettingKey(at.Claude), fake)) // only the fake ever runs
	generate := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.GenerateTeam(rec, httptest.NewRequest(http.MethodPost, "/api/teams/generate", strings.NewReader(`{"directive":"a small team"}`)))
		return rec
	}
	assert.Equal(t, http.StatusAccepted, generate().Code)
	assert.Equal(t, http.StatusAccepted, generate().Code)
	third := generate()
	assert.Equal(t, http.StatusTooManyRequests, third.Code, third.Body.String())
	assert.Contains(t, third.Body.String(), `"code":"busy"`)
	assert.NotEmpty(t, third.Header().Get("Retry-After"))
	assert.Equal(t, "action", third.Header().Get(httputil.ActionLimitHeader), "marked as a limit on this action, not an overloaded Coral")
	h.generateJobsMu.Lock()
	assert.Len(t, h.generateJobs, 2, "the refused request left no job record")
	// The kept records are bounded: a hundred finished ones, and a new job evicts the oldest finished.
	for i := 0; i < 100; i++ {
		h.generateJobs[fmt.Sprintf("old-%d", i)] = &generateJob{Status: "complete", CreatedAt: time.Now().Add(-time.Duration(i) * time.Second)}
	}
	for _, j := range h.generateJobs {
		if j.Status == "pending" {
			j.Status = "complete" // free the running slots
		}
	}
	h.generateJobsMu.Unlock()
	assert.Equal(t, http.StatusAccepted, generate().Code)
	h.generateJobsMu.Lock()
	assert.LessOrEqual(t, len(h.generateJobs), maxGenerateJobs)
	h.generateJobsMu.Unlock()
}

// #4602: a notification's text is bounded where a client or an agent chooses it, and the store keeps at most 200.
func TestNotificationCreateBoundsTheText(t *testing.T) {
	store := NewNotificationStore()
	h := NewNotificationHandler(store)
	post := func(title, message string) int {
		b, _ := json.Marshal(map[string]any{"title": title, "message": message})
		req := httptest.NewRequest(http.MethodPost, "/api/notifications", bytes.NewReader(b))
		req = req.WithContext(context.WithValue(req.Context(), callerKindKey{}, callerDashCookie))
		rec := httptest.NewRecorder()
		h.Create(rec, req)
		return rec.Code
	}
	assert.Equal(t, http.StatusOK, post(strings.Repeat("t", maxNotificationTitle), strings.Repeat("m", maxNotificationMessage)), "the limits themselves pass")
	assert.Equal(t, http.StatusBadRequest, post(strings.Repeat("t", maxNotificationTitle+1), "x"))
	assert.Equal(t, http.StatusBadRequest, post("x", strings.Repeat("m", maxNotificationMessage+1)))
	assert.Equal(t, http.StatusBadRequest, post("x", strings.Repeat("m", 4<<20)), "a 4 MiB message")
}

func TestNotificationStoreKeepsAtMost200(t *testing.T) {
	store := NewNotificationStore()
	var last Notification
	for i := 0; i < 3*maxNotificationItems; i++ {
		last = store.Push(Notification{Title: "n", Message: fmt.Sprintf("message %d", i)})
	}
	backlog, _ := store.Connect()
	assert.Len(t, backlog, maxNotificationItems)
	assert.Equal(t, last.ID, backlog[len(backlog)-1].ID, "the newest is kept")
	assert.Equal(t, last.ID-int64(maxNotificationItems)+1, backlog[0].ID, "the oldest go first")
}
