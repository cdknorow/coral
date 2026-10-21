package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board/boardtest"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

type histEventsServer struct {
	url string
	db  *store.DB
	ts  *store.TaskStore
}

func setupHistoryEventsServer(t *testing.T) *histEventsServer {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	bs, err := boardtest.Open(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	t.Cleanup(func() { bs.Close() })
	h := NewHistoryHandler(db, &config.Config{}, bs)
	r := chi.NewRouter()
	r.Get("/api/sessions/history/{sessionID}/events", h.GetSessionEvents)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	return &histEventsServer{url: server.URL, db: db, ts: h.ts}
}

func (s *histEventsServer) get(t *testing.T, sid, query string, tunneled bool) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest("GET", s.url+"/api/sessions/history/"+sid+"/events?"+query, nil)
	require.NoError(t, err)
	if tunneled {
		req.Header.Set(tunnel.ViaHeader, "1")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// #4779: GET /api/sessions/history/{id}/events takes the live route's search (type, q, since, until, sort, cursor; the reply {events, total,
// next_cursor}) on one session's events, and answers as before without those parameters.
func TestHistoryEventsSearchOverHTTP(t *testing.T) {
	s := setupHistoryEventsServer(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	add := func(sid, agent string, i int, typ, summary string) {
		sidCopy := sid
		_, err := s.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: agent, SessionID: &sidCopy, EventType: typ, Summary: summary,
			CreatedAt: base.Add(time.Duration(i) * time.Minute).Format(store.ISOFormat)})
		require.NoError(t, err)
	}
	for i := 0; i < 7; i++ {
		typ := "tool_use"
		if i%3 == 0 {
			typ = "notification"
		}
		add("s1", fmt.Sprintf("agent%d", i%2), i, typ, fmt.Sprintf("Parser step %d", i)) // two agent names in one session
	}
	add("s1", "agent0", 8, "tool_use", "something else")
	add("s2", "agent0", 9, "tool_use", "Parser of another session")

	code, body := s.get(t, "s1", "q=parser", false)
	require.Equal(t, 200, code, string(body))
	var out evReply
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, 7, out.Total, "the session's parser events, not the other session's")
	assert.Len(t, out.Events, 7)
	for _, e := range out.Events {
		require.NotNil(t, e.SessionID)
		assert.Equal(t, "s1", *e.SessionID)
	}

	code, body = s.get(t, "s1", "type=notification&q=parser", false)
	require.Equal(t, 200, code)
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, 3, out.Total)

	// paged, oldest first (sort=oldest): every event once, in order
	var seen []string
	cursor := ""
	for page := 0; page < 10; page++ {
		q := "sort=oldest&limit=3&q=parser"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		code, body = s.get(t, "s1", q, false)
		require.Equal(t, 200, code, string(body))
		out = evReply{}
		require.NoError(t, json.Unmarshal(body, &out))
		assert.Equal(t, 7, out.Total)
		for _, e := range out.Events {
			seen = append(seen, e.Summary)
		}
		if out.NextCursor == "" {
			break
		}
		cursor = out.NextCursor
	}
	assert.Equal(t, []string{"Parser step 0", "Parser step 1", "Parser step 2", "Parser step 3", "Parser step 4", "Parser step 5", "Parser step 6"}, seen)

	code, body = s.get(t, "s1", "since="+url.QueryEscape(base.Add(5*time.Minute).Format(time.RFC3339))+"&until="+url.QueryEscape(base.Add(7*time.Minute).Format(time.RFC3339)), false)
	require.Equal(t, 200, code)
	out = evReply{}
	require.NoError(t, json.Unmarshal(body, &out))
	assert.Equal(t, 2, out.Total, "since is inclusive and until exclusive: minutes 5 and 6")

	// the plain list is unchanged: a bare array, newest first, limit and no search parameters
	code, body = s.get(t, "s1", "limit=3", false)
	require.Equal(t, 200, code)
	var plain []store.AgentEvent
	require.NoError(t, json.Unmarshal(body, &plain), string(body))
	require.Len(t, plain, 3)
	assert.Equal(t, "something else", plain[0].Summary)

	for _, bad := range []string{"session_id=s2&q=x", "bogus=1&q=x", "q=a&q=b", "since=notatime", "sort=sideways&q=x", "cursor=garbage&q=x"} {
		code, _ = s.get(t, "s1", bad, false)
		assert.Equal(t, 400, code, bad)
	}
}

// #4779: through Coral Hub a viewer matches on what it sees. A raw token left in the table (older versions, a direct write) is matched by no
// slice of itself, and a q holding a token is refused.
func TestHistoryEventsTunneledSearchNeverMatchesARawToken(t *testing.T) {
	const tok = "ghp_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	s := setupHistoryEventsServer(t)
	_, err := s.db.ExecContext(context.Background(),
		`INSERT INTO agent_events (agent_name, session_id, event_type, tool_name, summary, created_at) VALUES ('alpha', 's1', 'tool_use', 'Bash', ?, '2026-10-18T10:00:00.000000+00:00')`, "export TOKEN="+tok)
	require.NoError(t, err)
	total := func(q string, tunneled bool) (int, int) {
		code, b := s.get(t, "s1", "q="+url.QueryEscape(q), tunneled)
		var out evReply
		_ = json.Unmarshal(b, &out)
		return code, out.Total
	}
	code, n := total("TOKEN=", false)
	require.Equal(t, 200, code)
	require.Equal(t, 1, n)
	shown := strings.ToLower(shownText("export TOKEN="+tok) + " Bash tool_use alpha")
	checked := 0
	for i := 0; i < len(tok); i++ {
		for j := i + 1; j <= len(tok); j++ {
			if strings.Contains(shown, strings.ToLower(tok[i:j])) {
				continue
			}
			code, n := total(tok[i:j], true)
			checked++
			assert.Zero(t, n, "slice %q matched through the tunnel", tok[i:j])
			assert.Contains(t, []int{200, 400}, code)
		}
	}
	assert.Greater(t, checked, 500)
	code, _ = total(tok, true)
	assert.Equal(t, 400, code)
	code, n = total("TOKEN=", true)
	assert.Equal(t, 200, code)
	assert.Equal(t, 1, n)
}
