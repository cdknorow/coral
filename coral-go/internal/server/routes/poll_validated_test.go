package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/internal/terminalinput"
)

// captureCountingTerminal counts capture-pane runs.
type captureCountingTerminal struct {
	*ptymanager.TmuxSessionTerminal
	captures atomic.Int64
}

func (c *captureCountingTerminal) CaptureOutput(ctx context.Context, name string, lines int, agentType, sessionID string) (string, error) {
	c.captures.Add(1)
	return c.TmuxSessionTerminal.CaptureOutput(ctx, name, lines, agentType, sessionID)
}

// #3439: the terminal poll answers 304 while nothing changed (the dashboard sends the ETag back),
// 200 with a new ETag once the pane prints, and skips capture-pane while the pane's output log has
// not grown.
func TestPollAnswers304WhileNothingChanged(t *testing.T) {
	backend, client := newTerminalTestTmuxBackend(t)
	term := &captureCountingTerminal{TmuxSessionTerminal: ptymanager.NewTmuxSessionTerminal(client)}
	db, err := storetest.Open(t, t.TempDir()+"/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewSessionsHandler(db, &config.Config{WSPollIntervalS: 1, LogDir: t.TempDir()}, backend, term, nil)
	r := chi.NewRouter()
	r.Get("/api/sessions/live/{name}/poll", h.Poll)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	const sid = "d0e1f2a3-1111-4222-8333-444455556666"
	tmuxName := spawnTestSession(t, backend, "poll-agent", sid, "")
	// The poll answers only for a live session it knows (exact-target check).
	require.NoError(t, store.NewSessionStore(db).RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID: sid, AgentType: "claude", AgentName: "poll-agent", WorkingDir: t.TempDir()}))
	url := srv.URL + "/api/sessions/live/" + tmuxName + "/poll?agent_type=claude&session_id=" + sid
	get := func(etag string) (int, string, string) {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("ETag"), string(b)
	}
	capture := func(body string) string {
		var d struct {
			Capture struct {
				Capture string `json:"capture"`
			} `json:"capture"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &d), body)
		return d.Capture.Capture
	}

	// Settle: the shell's prompt is printed and the answer stops changing.
	var etag, body string
	require.Eventually(t, func() bool {
		code, e, b := get("")
		if code != http.StatusOK || capture(b) == "" {
			return false
		}
		same := e == etag
		etag, body = e, b
		return same
	}, 10*time.Second, 300*time.Millisecond, "the poll settles")
	assert.True(t, strings.HasPrefix(etag, `W/"`), etag)

	before := term.captures.Load()
	for i := 0; i < 3; i++ {
		code, e, b := get(etag)
		assert.Equal(t, http.StatusNotModified, code, "unchanged: 304")
		assert.Equal(t, etag, e)
		assert.Empty(t, b, "no body")
	}
	assert.Equal(t, before, term.captures.Load(), "no capture-pane while the pane printed nothing")

	// The pane prints: a 200 with the new screen and a new ETag, from a fresh capture.
	require.NoError(t, backend.SendInput(terminalinput.Unclassified, tmuxName, []byte("echo POLL_$((6*7))\r")))
	require.Eventually(t, func() bool {
		code, e, b := get(etag)
		if code == http.StatusOK && strings.Contains(capture(b), "POLL_42") {
			assert.NotEqual(t, etag, e)
			return true
		}
		return false
	}, 10*time.Second, 200*time.Millisecond, "the change arrives as a 200")
	assert.Greater(t, term.captures.Load(), before, "a new capture once the pane printed")
	_ = body
}

// #3439: the board's task list (polled every 10 s per open board) answers 304 while unchanged
// and 200 with a new ETag once a task changes.
func TestBoardTaskListAnswers304WhileUnchanged(t *testing.T) {
	server, _ := setupBoardTestServer(t)
	base := server.URL + "/api/board/etag/tasks"
	resp := postJSON(t, base, map[string]string{"title": "First", "subscriber_id": "Operator", "created_by": "Operator"})
	resp.Body.Close()
	require.Less(t, resp.StatusCode, 300, "the first task is created")

	get := func(etag string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, base+"?limit=50", nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, resp.Header.Get("ETag")
	}
	code, etag := get("")
	require.Equal(t, http.StatusOK, code)
	require.NotEmpty(t, etag)
	code, _ = get(etag)
	assert.Equal(t, http.StatusNotModified, code, "unchanged: 304")

	resp = postJSON(t, base, map[string]string{"title": "Second", "subscriber_id": "Operator", "created_by": "Operator"})
	resp.Body.Close()
	require.Less(t, resp.StatusCode, 300, "the second task is created")
	code, etag2 := get(etag)
	assert.Equal(t, http.StatusOK, code, "a new task: 200")
	assert.NotEqual(t, etag, etag2)
}
