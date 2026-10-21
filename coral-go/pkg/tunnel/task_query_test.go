package tunnel

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/pkg/redact"
)

// GET /api/board/tasks (#4540) takes the task query's parameters at the View tier and only those; a parameter the handler does
// not read and every write is refused.
func TestTaskQueryRouteIsViewTier(t *testing.T) {
	for _, tc := range []struct {
		method, path, query string
		want                Tier
	}{
		{"GET", "/api/board/tasks", "", TierView},
		{"GET", "/api/board/tasks", "limit=5", TierView},
		{"GET", "/api/board/tasks", "fields=id,title&status=pending&status=blocked&assigned_to=Operator", TierView},
		{"GET", "/api/board/tasks", "fields=summary", TierView},
		{"GET", "/api/board/tasks", "board=p&priority=high&created_by=x&parked=true&created_since=2026-10-18T00:00:00Z&updated_since=2026-10-18T00:00:00Z", TierView},
		{"GET", "/api/board/tasks", "sort=updated_at&order=asc&limit=50&cursor=abc", TierView},
		{"GET", "/api/board/tasks", "verification=verified&fields=id,verification", TierView}, // #4563
		{"GET", "/api/board/tasks", "verified=true", TierNever},
		{"HEAD", "/api/board/tasks", "limit=5", TierView},
		{"GET", "/api/board/tasks", "body=1", TierNever},
		{"GET", "/api/board/tasks", "filter=status%3Dpending", TierNever},
		{"GET", "/api/board/tasks", "q=select&status=pending&fields=id,title", TierView}, // #4745: q is read now
		{"GET", "/api/board/tasks", "query=select", TierNever},
		{"GET", "/api/board/tasks", "search=select", TierNever},
		{"POST", "/api/board/tasks", "", TierNever},
		{"PUT", "/api/board/tasks", "", TierNever},
		{"GET", "/api/board/tasks/query", "", TierNever}, // there is no second route
	} {
		assert.Equal(t, tc.want, Classify(tc.method, tc.path, tc.query), tc.method+" "+tc.path+"?"+tc.query)
	}
	assert.Equal(t, "redact", RedactionClass("GET", "/api/board/tasks"), "its replies are redacted like any View reply")
}

// A task's body and completion message come back through the tunnel with tokens replaced (#3560), and only when named.
func TestTaskQueryRepliesAreRedactedThroughTheTunnel(t *testing.T) {
	tok := fake("ghp_", 36, alnum)
	reply, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"id": 1, "title": "t", "body": "use GITHUB_TOKEN=" + tok + " to push", "completion_message": "Authorization: Bearer " + fake("", 40, alnum)}}})
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(reply)
	})
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, func(l *LocalHandler) { l.RedactTokens = func() bool { return true } })
	resp := do(t, sess, "GET", "/api/board/tasks?fields=id,title,body,completion_message", "", nil, nil)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	assert.NotContains(t, string(b), tok)
	assert.Contains(t, string(b), redact.Marker)
	assert.True(t, json.Valid(b), string(b))
}

// GET /api/board/{project}/artifacts takes the Team Artifacts query's parameters at the View tier, and only those (#4572).
func TestTeamArtifactsQueryIsViewTier(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  Tier
	}{
		{"", TierView},
		{"limit=50&offset=10&session_id=abc", TierView},
		{"q=report&kind=pdf,image&origin=received&by=Lead&board=beta&kept=true&since=2026-10-01T00:00:00Z&until=2026-10-18T00:00:00Z&sort=size&cursor=abc", TierView},
		{"q=100%25&sort=name", TierView},
		{"search=report", TierNever},
		{"filter=pdf", TierNever},
		{"order=asc", TierNever},
		{"fields=name", TierNever},
	} {
		assert.Equal(t, tc.want, Classify("GET", "/api/board/alpha/artifacts", tc.query), tc.query)
	}
	assert.Equal(t, TierNever, Classify("POST", "/api/board/alpha/artifacts", "q=a"))
}

// An artifact's name is redacted in the query's reply as in the plain list's (the route is a View route).
func TestTeamArtifactsQueryRepliesAreRedactedThroughTheTunnel(t *testing.T) {
	tok := fake("ghp_", 36, alnum)
	reply, _ := json.Marshal(map[string]any{"total": 1, "artifacts": []map[string]any{{"name": "deploy GITHUB_TOKEN=" + tok + ".txt", "task_title": "Authorization: Bearer " + fake("", 40, alnum)}}})
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(reply)
	})
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, func(l *LocalHandler) { l.RedactTokens = func() bool { return true } })
	resp := do(t, sess, "GET", "/api/board/alpha/artifacts?q=deploy&sort=name", "", nil, nil)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	assert.NotContains(t, string(b), tok)
	assert.Contains(t, string(b), redact.Marker)
	assert.True(t, json.Valid(b), string(b))
	assert.Equal(t, "redact", RedactionClass("GET", "/api/board/{project}/artifacts"))
}

// GET /api/board/{project}/messages/search (#4745) takes the message search's parameters at the View tier, and only those.
func TestBoardMessageSearchIsViewTier(t *testing.T) {
	for _, tc := range []struct {
		method, query string
		want          Tier
	}{
		{"GET", "", TierView},
		{"GET", "q=alpha&from=Backend+Dev&mentions=QA&since=2026-10-18T00:00:00Z&until=2026-10-19T00:00:00Z&sort=oldest&limit=50&cursor=abc", TierView},
		{"HEAD", "q=x", TierView},
		{"GET", "search=x", TierNever},
		{"GET", "offset=5", TierNever},
		{"GET", "after_id=5", TierNever},
		{"POST", "q=x", TierNever},
		{"PUT", "", TierNever},
	} {
		assert.Equal(t, tc.want, Classify(tc.method, "/api/board/proj/messages/search", tc.query), tc.method+" ?"+tc.query)
	}
	assert.Equal(t, "redact", RedactionClass("GET", "/api/board/proj/messages/search"), "its replies are redacted like any View reply")
}

// GET /api/sessions/live/{name}/events takes the events search's parameters at the View tier, and only those (#4745).
func TestAgentEventsSearchIsViewTier(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  Tier
	}{
		{"", TierView},
		{"limit=50&session_id=abc", TierView},
		{"type=tool_use&q=read&since=2026-10-18T00:00:00Z&until=2026-10-19T00:00:00Z&sort=oldest&limit=50&cursor=abc&session_id=abc", TierView},
		{"search=x", TierNever},
		{"offset=5", TierNever},
		{"events_limit=5", TierNever},
	} {
		assert.Equal(t, tc.want, Classify("GET", "/api/sessions/live/agent/events", tc.query), "?"+tc.query)
	}
}
