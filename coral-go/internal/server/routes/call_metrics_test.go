package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/cdknorow/coral/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

func TestRequestMetricsRecordsAPIIdentityAndExcludesMetricsRoutes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "metrics.db"))
	require.NoError(t, err)
	defer db.Close()

	r := chi.NewRouter()
	r.Use(RequestMetrics(db))
	r.Post("/api/board/{project}/tasks", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "Frontend Dev", body["subscriber_id"])
		w.WriteHeader(http.StatusCreated)
	})
	r.Post("/api/call-metrics", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	r.Post("/proxy/{sessionID}/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/board/death-or-trade/tasks", bytes.NewBufferString(`{"subscriber_id":"Frontend Dev"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(httptest.NewRecorder(), req)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/call-metrics", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/proxy/claude-session/v1/responses", nil))

	rows, err := db.CallMetricSummary(context.Background(), time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	var apiRow, proxyRow store.CallMetricSummary
	for _, row := range rows {
		if row.CallType == "api" {
			apiRow = row
		} else {
			proxyRow = row
		}
	}
	require.Equal(t, "/api/board/{project}/tasks", apiRow.Operation)
	require.Equal(t, "Frontend Dev", apiRow.AgentName)
	require.Equal(t, "death-or-trade", apiRow.BoardName)
	require.Equal(t, 1, apiRow.Calls)
	require.Equal(t, "proxy", proxyRow.CallType)
	require.Equal(t, "/proxy/{sessionID}/v1/responses", proxyRow.Operation)
	require.Equal(t, "claude-session", proxyRow.AgentName)
}

func TestRequestMetricsResolvesSessionToAgentDisplayName(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "metrics.db"))
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`INSERT INTO live_sessions(session_id, agent_type, agent_name, display_name, working_dir, created_at) VALUES(?,?,?,?,?,?)`,
		"session-1", "codex", "death_or_trade", "Lead Developer", "/tmp", time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)

	r := chi.NewRouter()
	r.Use(RequestMetrics(db))
	r.Get("/api/sessions/live/{name}/poll", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/live/death_or_trade/poll?session_id=session-1", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	rows, err := db.CallMetricSummary(context.Background(), time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "Lead Developer", rows[0].AgentName)
}

func TestCallMetricSummaryResolvesHistoricalSessionTeam(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "metrics.db"))
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`INSERT INTO live_sessions(session_id, agent_type, agent_name, display_name, board_name, working_dir, created_at) VALUES(?,?,?,?,?,?,?)`,
		"session-1", "codex", "death_or_trade", "Frontend Dev", "death-or-trade-ai-auto", "/tmp", time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, db.RecordCallMetric(context.Background(), &store.CallMetric{CallType: "api", Operation: "/api/example", AgentName: "death_or_trade", SessionID: "session-1", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}))

	rows, err := db.CallMetricSummary(context.Background(), time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "Frontend Dev", rows[0].AgentName)
	require.Equal(t, "death-or-trade-ai-auto", rows[0].BoardName)
}

// The metrics wrapper must not hide http.Hijacker/Flusher: WebSockets and SSE
// on /api/ routes (the hub's /api/remote/{server}/ws/... bridge) depend on it.
func TestRequestMetricsKeepsWebSocketAndStreamingWorking(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "metrics.db"))
	require.NoError(t, err)
	defer db.Close()

	r := chi.NewRouter()
	r.Use(RequestMetrics(db))
	r.Get("/api/ws-echo", func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		typ, data, err := c.Read(r.Context())
		if err == nil {
			_ = c.Write(r.Context(), typ, data)
		}
		c.Close(websocket.StatusNormalClosure, "")
	})
	flushed := make(chan struct{})
	r.Get("/api/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("first\n"))
		require.NoError(t, http.NewResponseController(w).Flush(), "Flush must reach the real writer")
		close(flushed)
		<-r.Context().Done()
	})
	ts := httptest.NewServer(r)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+ts.URL[4:]+"/api/ws-echo", nil)
	require.NoError(t, err, "websocket upgrade through RequestMetrics")
	require.NoError(t, c.Write(ctx, websocket.MessageText, []byte("hi")))
	_, got, err := c.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "hi", string(got))
	c.CloseNow()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err) // headers only arrive once the handler flushed
	<-flushed
	resp.Body.Close()
}
