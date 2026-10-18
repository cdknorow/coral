package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

type evReply struct {
	Events     []store.AgentEvent `json:"events"`
	Total      int                `json:"total"`
	Limit      int                `json:"limit"`
	NextCursor string             `json:"next_cursor"`
	PrevCursor string             `json:"prev_cursor"`
	Truncated  bool               `json:"truncated"`
	Error      string             `json:"error"`
}

// #4745: GET /api/sessions/live/{name}/events with type, q, since, until, sort, limit and cursor: the reply {events, total, next_cursor,
// prev_cursor}, the plain list unchanged without them, and the Coral Hub rules.
func TestAgentEventsSearchOverHTTP(t *testing.T) {
	server, handler, _, _ := setupSessionsTestServer(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	add := func(i int, typ, tool, summary string) {
		ev := &store.AgentEvent{AgentName: "alpha", EventType: typ, Summary: summary, CreatedAt: base.Add(time.Duration(i) * time.Minute).Format(store.ISOFormat)}
		if tool != "" {
			ev.ToolName = &tool
		}
		d := fmt.Sprintf(`{"i":%d}`, i)
		ev.DetailJSON = &d
		_, err := handler.ts.InsertAgentEvent(ctx, ev)
		require.NoError(t, err)
	}
	add(0, "prompt_submit", "", "Please fix the Parser")
	add(1, "tool_use", "Read", "Read parser.go")
	add(2, "tool_use", "Bash", "go test ./parser")
	add(3, "notification", "", "Waiting for input")
	add(4, "tool_use", "Edit", "Edit parser.go")
	add(5, "stop", "", "Done: 100% green")
	add(6, "tool_use", "Read", "Read other.go")
	get := func(query string, tunneled bool) (int, evReply, string) {
		req, err := http.NewRequest("GET", server.URL+"/api/sessions/live/alpha/events?"+query, nil)
		require.NoError(t, err)
		if tunneled {
			req.Header.Set(tunnel.ViaHeader, "1")
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		var out evReply
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out, string(b)
	}
	summaries := func(r evReply) []string {
		var out []string
		for _, e := range r.Events {
			out = append(out, e.Summary)
		}
		return out
	}
	// the plain list is still a bare array, newest first, and ignores what it does not know
	resp, err := http.Get(server.URL + "/api/sessions/live/alpha/events?limit=3&_=cachebuster")
	require.NoError(t, err)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var plain []store.AgentEvent
	require.NoError(t, json.Unmarshal(b, &plain), string(b))
	require.Len(t, plain, 3)
	assert.Equal(t, "Read other.go", plain[0].Summary)
	// a search
	code, r, _ := get("type=tool_use&sort=oldest", false)
	require.Equal(t, 200, code)
	assert.Equal(t, []string{"Read parser.go", "go test ./parser", "Edit parser.go", "Read other.go"}, summaries(r))
	assert.Equal(t, 4, r.Total)
	assert.Equal(t, 50, r.Limit)
	assert.NotNil(t, r.Events[0].DetailJSON, "the whole event, detail included")
	code, r, _ = get("q=PARSER", false)
	assert.Equal(t, []string{"Edit parser.go", "go test ./parser", "Read parser.go", "Please fix the Parser"}, summaries(r), "newest first, any case")
	code, r, _ = get("q=bash&type=tool_use", false)
	assert.Equal(t, []string{"go test ./parser"}, summaries(r), "the tool name is searched too")
	code, r, _ = get("q="+url.QueryEscape("100%"), false)
	assert.Equal(t, []string{"Done: 100% green"}, summaries(r))
	code, r, _ = get("since="+url.QueryEscape(base.Add(2*time.Minute).Format(time.RFC3339))+"&until="+url.QueryEscape(base.Add(5*time.Minute).Format(time.RFC3339))+"&sort=oldest", false)
	assert.Equal(t, []string{"go test ./parser", "Waiting for input", "Edit parser.go"}, summaries(r), "since inclusive, until exclusive")
	// paging both ways
	code, r, _ = get("type=tool_use&sort=oldest&limit=3", false)
	assert.Equal(t, 4, r.Total)
	require.NotEmpty(t, r.NextCursor)
	assert.Empty(t, r.PrevCursor)
	code, r2, _ := get("type=tool_use&sort=oldest&limit=3&cursor="+r.NextCursor, false)
	assert.Equal(t, []string{"Read other.go"}, summaries(r2))
	require.NotEmpty(t, r2.PrevCursor)
	assert.Empty(t, r2.NextCursor)
	code, back, _ := get("type=tool_use&sort=oldest&limit=3&cursor="+r2.PrevCursor, false)
	assert.Equal(t, summaries(r), summaries(back))
	// refusals name the problem
	for query, want := range map[string]string{
		"type=Tool_Use": "type must be a lower-case word", "type=" + url.QueryEscape("x' OR 1=1"): "type must be", "q=" + strings.Repeat("x", 201): "q is at most 200 characters",
		"since=yesterday": "since must be an RFC 3339 time", "sort=name": "unknown sort", "q=a&limit=500": "limit must be a whole number", "cursor=junk": "cursor is not one this server gave",
		"q=a&nope=1": "unknown parameter nope", "q=a;b": "the query string is not valid", "q=a&q=b": "q is given more than once",
	} {
		code, r, _ = get(query, false)
		assert.Equal(t, 400, code, query)
		assert.Contains(t, r.Error, want, query)
	}
	// through the hub: a q holding a secret is refused, text without one works as ever
	code, r, _ = get("q=ghp_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5", true)
	assert.Equal(t, 400, code)
	assert.Contains(t, r.Error, "q can't contain a secret")
	code, r, _ = get("q=parser&type=tool_use", true)
	assert.Equal(t, 200, code)
	assert.Equal(t, 3, r.Total)
}

// A token in an event is stored redacted; through the hub the marker that stands in for it is searchable, a token is not.
func TestAgentEventsSearchThroughTheHubMatchesTheMarker(t *testing.T) {
	const tok = "ghp_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	server, handler, _, _ := setupSessionsTestServer(t)
	ctx := context.Background()
	_, err := handler.ts.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "alpha", EventType: "tool_use", Summary: "export TOKEN=" + tok})
	require.NoError(t, err)
	count := func(query string, tunneled bool) (int, int) {
		req, err := http.NewRequest("GET", server.URL+"/api/sessions/live/alpha/events?"+query, nil)
		require.NoError(t, err)
		if tunneled {
			req.Header.Set(tunnel.ViaHeader, "1")
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out evReply
		b, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out.Total
	}
	code, n := count("q=redacted", true)
	assert.Equal(t, 200, code)
	assert.Equal(t, 1, n)
	code, n = count("q=redacted", false)
	assert.Equal(t, 1, n, "stored redacted at write, so a local search sees the marker too")
	code, _ = count("q="+tok, true)
	assert.Equal(t, 400, code)
}

// #4745 (Security INFO): events are redacted when they are stored, so a tunneled search is safe today only because no raw token is in the table.
// This row is put there by SQL, as an older version or a direct write could have left it: a tunneled q that is any slice of the token matches
// nothing (the match is on what the viewer sees, the redaction marker), and the handler sets Shown itself rather than relying on the storage.
func TestAgentEventsTunneledSearchNeverMatchesARawTokenInTheTable(t *testing.T) {
	const tok = "ghp_aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	server, handler, _, _ := setupSessionsTestServer(t)
	_, err := handler.db.ExecContext(context.Background(),
		`INSERT INTO agent_events (agent_name, event_type, tool_name, summary, created_at) VALUES ('alpha', 'tool_use', 'Bash', ?, '2026-10-18T10:00:00.000000+00:00')`, "export TOKEN="+tok)
	require.NoError(t, err)
	get := func(q string, tunneled bool) (int, int) {
		req, err := http.NewRequest("GET", server.URL+"/api/sessions/live/alpha/events?q="+url.QueryEscape(q), nil)
		require.NoError(t, err)
		if tunneled {
			req.Header.Set(tunnel.ViaHeader, "1")
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		var out evReply
		b, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out.Total
	}
	code, n := get("TOKEN=", false)
	require.Equal(t, 200, code)
	require.Equal(t, 1, n, "the row is there, and a local search finds it")
	shown := strings.ToLower(shownText("export TOKEN="+tok) + " Bash tool_use alpha")
	require.NotContains(t, shown, strings.ToLower(tok[:8]), "the shown text hides the token")
	checked := 0
	for i := 0; i < len(tok); i++ {
		for j := i + 1; j <= len(tok); j++ {
			if strings.Contains(shown, strings.ToLower(tok[i:j])) {
				continue // a slice that is also visible text ("x" in "export") may match: it reveals nothing
			}
			code, n := get(tok[i:j], true)
			checked++
			assert.Zero(t, n, "slice %q matched through the tunnel", tok[i:j])
			assert.Contains(t, []int{200, 400}, code)
		}
	}
	assert.Greater(t, checked, 500)
	code, n = get("TOKEN=", true)
	assert.Equal(t, 200, code)
	assert.Equal(t, 1, n, "the part of the summary the redaction leaves is still searchable")
}
