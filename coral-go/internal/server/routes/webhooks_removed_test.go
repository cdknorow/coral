package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #3646: Coral doesn't send webhooks. A board subscription or a one-off task run that still
// names a webhook_url is refused with 400, from this computer and through the hub alike, and
// nothing is stored; without one they work as before.
func TestWebhookURLsAreRefused(t *testing.T) {
	srv, handler := setupBoardTestServer(t)
	subscribe := func(via, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest("POST", srv.URL+"/api/board/team/subscribe", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if via != "" {
			req.Header.Set(tunnel.ViaHeader, via)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		msg, _ := out["error"].(string)
		return resp.StatusCode, msg
	}
	for _, via := range []string{"", cloud.ViaSecret(), "forged"} {
		code, msg := subscribe(via, `{"subscriber_id":"Hooked","job_title":"Dev","webhook_url":"https://hooks.example.com/x"}`)
		assert.Equal(t, http.StatusBadRequest, code, "via %q", via)
		assert.Equal(t, errWebhooksNotSupported, msg)
	}
	subs, err := handler.bs.ListSubscribers(context.Background(), "team")
	require.NoError(t, err)
	assert.Empty(t, subs, "nothing stored")
	for _, body := range []string{`{"subscriber_id":"Plain","job_title":"Dev"}`, `{"subscriber_id":"Empty","job_title":"Dev","webhook_url":""}`} {
		code, msg := subscribe("", body)
		assert.Equal(t, http.StatusOK, code, msg)
	}

	db, err := storetest.Open(t, t.TempDir()+"/tasks.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	tasks := NewTasksHandler(db, &config.Config{})
	r := chi.NewRouter()
	r.Post("/api/tasks/run", tasks.SubmitTask)
	w := send(t, r, "POST", "/api/tasks/run", map[string]any{"prompt": "p", "repo_path": t.TempDir(), "webhook_url": "https://hooks.example.com/x"})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), errWebhooksNotSupported)
}

// No board message makes an outbound request to a subscription's stored webhook_url (a value
// left from before the removal): nothing reads it any more.
func TestNoOutboundWebhookFromAStoredURL(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)

	path := filepath.Join(t.TempDir(), "board.db")
	bs, err := boardtest.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	handler := NewBoardHandler(bs)
	t.Cleanup(handler.WaitBackground)
	r := chi.NewRouter()
	r.Post("/api/board/{project}/messages", handler.PostMessage)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	ctx := context.Background()
	_, err = bs.Subscribe(ctx, "team", "Listener", "Dev", "s1", nil, "all")
	require.NoError(t, err)
	_, err = bs.Subscribe(ctx, "team", "Orchestrator", "Orchestrator", "s0", nil, "all")
	require.NoError(t, err)
	raw, err := sqlx.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.ExecContext(ctx, "UPDATE board_subscribers SET webhook_url = ? WHERE subscriber_id = 'Listener'", target.URL+"/hook")
	require.NoError(t, err)
	raw.Close()

	resp, err := http.Post(srv.URL+"/api/board/team/messages", "application/json",
		strings.NewReader(`{"subscriber_id":"Orchestrator","content":"hello @Listener"}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Less(t, resp.StatusCode, 300)
	handler.WaitBackground()
	assert.Zero(t, hits.Load(), "no outbound webhook request")
}

func send(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
