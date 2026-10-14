package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/httputil"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// Addresses every save path must refuse (#3363). A hostname that resolves to a blocked
// address is covered in httputil (it needs a fake resolver).
var blockedWebhookURLs = []string{
	"http://127.0.0.1:8420/api/sessions",
	"http://169.254.169.254/latest/meta-data/",
	"http://10.0.0.5/hook",
	"http://[::1]:8420/",
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

// #3363: notification webhooks (create and update), board subscriber webhooks and one-shot
// job-run callbacks all refuse a private target when saved, with a 400 that says why.
func TestWebhookTargetsAreValidatedWhenSaved(t *testing.T) {
	db, err := storetest.Open(t, t.TempDir()+"/ssrf.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	cfg := &config.Config{}
	wh := NewWebhooksHandler(db, cfg)
	tasks := NewTasksHandler(db, cfg)
	r := chi.NewRouter()
	r.Post("/api/webhooks", wh.CreateWebhook)
	r.Patch("/api/webhooks/{webhookID}", wh.UpdateWebhook)
	r.Post("/api/tasks/run", tasks.SubmitTask)
	boardSrv, _ := setupBoardTestServer(t)

	for _, u := range blockedWebhookURLs {
		w := send(t, r, "POST", "/api/webhooks", map[string]any{"name": "n", "url": u})
		assert.Equal(t, http.StatusBadRequest, w.Code, "create "+u)
		assert.Contains(t, w.Body.String(), "private network", u)

		w = send(t, r, "POST", "/api/tasks/run", map[string]any{"prompt": "p", "repo_path": t.TempDir(), "webhook_url": u})
		assert.Equal(t, http.StatusBadRequest, w.Code, "task run "+u)

		resp, err := http.Post(boardSrv.URL+"/api/board/team/subscribe", "application/json",
			strings.NewReader(`{"subscriber_id":"a","job_title":"Dev","webhook_url":"`+u+`"}`))
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "subscribe "+u)
	}

	// A public URL is accepted; updating it to a private one is refused.
	w := send(t, r, "POST", "/api/webhooks", map[string]any{"name": "ok", "url": "https://93.184.216.34/hook"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var created struct{ ID int64 }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	w = send(t, r, "PATCH", "/api/webhooks/"+strconv.FormatInt(created.ID, 10), map[string]any{"url": "http://169.254.169.254/"})
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w = send(t, r, "PATCH", "/api/webhooks/"+strconv.FormatInt(created.ID, 10), map[string]any{"name": "renamed"})
	assert.Equal(t, http.StatusOK, w.Code, "an update without url is not affected")

	// Subscribing without a webhook is unaffected.
	resp, err := http.Post(boardSrv.URL+"/api/board/team/subscribe", "application/json", strings.NewReader(`{"subscriber_id":"b","job_title":"Dev"}`))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// #3363: board subscriber webhooks are posted with the guarded client, so a subscriber
// saved before this check (or a host that now resolves to a private address) is not reached.
func TestBoardWebhooksAreNotDeliveredToPrivateTargets(t *testing.T) {
	hits := make(chan struct{}, 4)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits <- struct{}{} }))
	defer target.Close()
	srv, handler := setupBoardTestServer(t)
	ctx := t.Context()
	url := target.URL + "/hook"
	_, err := handler.bs.Subscribe(ctx, "team", "listener", "Dev", "", &url, nil, "")
	require.NoError(t, err)
	_, err = handler.bs.Subscribe(ctx, "team", "poster", "Lead", "", nil, nil, "")
	require.NoError(t, err)
	postMsg := func() {
		resp, err := http.Post(srv.URL+"/api/board/team/messages", "application/json", strings.NewReader(`{"subscriber_id":"poster","content":"hi"}`))
		require.NoError(t, err)
		resp.Body.Close()
	}

	postMsg()
	select {
	case <-hits:
		t.Fatal("a webhook on 127.0.0.1 was delivered")
	case <-time.After(500 * time.Millisecond): // proving a negative: nothing arrives
	}

	httputil.SetWebhookAllowPrivate(func() bool { return true })
	t.Cleanup(func() { httputil.SetWebhookAllowPrivate(nil) })
	postMsg()
	select {
	case <-hits:
	case <-time.After(5 * time.Second):
		t.Fatal("with private targets allowed, the webhook should be delivered")
	}
}
