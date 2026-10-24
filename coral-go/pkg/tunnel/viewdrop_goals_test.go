package tunnel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #7087 (Orchestrator and coral-hub, 12:48): a blocked goal's blocked_on, decision, recommendation and if_silent are null for a viewer who can only look, in the goal list and in one goal, whatever
// the owner's secrets switch says; its title, outcome, done_when, state, driver and rollup stay (redacted like any View text); Interact and Launch get all of it. Other View replies that happen to
// hold a key of the same name are untouched: only the goal routes are read for them.

const goalOne = `{"id":3,"goal_uid":"0b9f1c2e-3d4a-4b5c-8d6e-7f8091a2b3c4","title":"Ship it","outcome":"People can use it","done_when":"Release is out","state":"blocked","driver":"Lead","blocked_on":"the operator","decision":"Ship Friday?","recommendation":"yes","if_silent":"ship Monday","rollup":{"done":1,"active":0,"blocked":0,"queued":2,"total":3}}`

func TestViewOnlyViewerGetsNoGoalDecisionFields(t *testing.T) {
	list := `{"goals":[` + goalOne + `,` + goalOne + `]}`
	other := `{"tasks":[{"id":1,"decision":"a task row that has a key of this name","blocked_on":"x"}]}`
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := ""
		switch r.URL.Path {
		case "/api/board/team/goals":
			body = list
		case "/api/board/team/goals/3":
			body = goalOne
		case "/api/board/team/tasks":
			body = other
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	})
	hub := newHub(t, fastCfg())
	mode := TierLaunch
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.MaxMode = func() Mode { return mode }
		l.AllowPath = nil
		l.AllowRequest = AllowRequest(func() Mode { return mode })
		l.RedactTokens = func() bool { return false }
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	get := func(path string, ctx context.Context) map[string]any {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://x"+path, nil)
		resp, err := sess.RoundTrip(req)
		require.NoError(t, err, path)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err, path)
		require.Equal(t, 200, resp.StatusCode, path)
		var out map[string]any
		require.NoError(t, json.Unmarshal(b, &out), path)
		return out
	}
	goalsIn := func(path string, doc map[string]any) []map[string]any {
		if path == "/api/board/team/goals/3" {
			return []map[string]any{doc}
		}
		var res []map[string]any
		for _, g := range doc["goals"].([]any) {
			res = append(res, g.(map[string]any))
		}
		return res
	}
	dropped := []string{"blocked_on", "decision", "recommendation", "if_silent"}
	for _, path := range []string{"/api/board/team/goals", "/api/board/team/goals/3"} {
		for _, g := range goalsIn(path, get(path, WithHubTier(ctx, "view"))) {
			for _, k := range dropped {
				v, present := g[k]
				assert.True(t, present, "%s %s is there, as null", path, k)
				assert.Nil(t, v, "%s %s is null for a view-only viewer", path, k)
			}
			assert.Equal(t, "Ship it", g["title"], path)
			assert.Equal(t, "People can use it", g["outcome"], path)
			assert.Equal(t, "Release is out", g["done_when"], path)
			assert.Equal(t, "blocked", g["state"], path)
			assert.Equal(t, "Lead", g["driver"], path)
			assert.Equal(t, map[string]any{"done": 1.0, "active": 0.0, "blocked": 0.0, "queued": 2.0, "total": 3.0}, g["rollup"], path)
		}
		for _, tier := range []string{"interact", "launch"} {
			for _, g := range goalsIn(path, get(path, WithHubTier(ctx, tier))) {
				assert.Equal(t, "Ship Friday?", g["decision"], "%s %s", path, tier)
				assert.Equal(t, "the operator", g["blocked_on"], "%s %s", path, tier)
				assert.Equal(t, "yes", g["recommendation"], "%s %s", path, tier)
				assert.Equal(t, "ship Monday", g["if_silent"], "%s %s", path, tier)
			}
		}
		// A viewer with no tier is View.
		for _, g := range goalsIn(path, get(path, ctx)) {
			assert.Nil(t, g["decision"], path)
		}
	}
	// Another View reply with keys of the same names is not a goal and is left as it is.
	rows := get("/api/board/team/tasks", WithHubTier(ctx, "view"))["tasks"].([]any)
	assert.Equal(t, "a task row that has a key of this name", rows[0].(map[string]any)["decision"])
	// The computer's mode caps the viewer: a Launch viewer on a View computer reads as View, at the next request.
	mode = TierView
	for _, g := range goalsIn("/api/board/team/goals", get("/api/board/team/goals", WithHubTier(ctx, "launch"))) {
		assert.Nil(t, g["decision"], "a Launch viewer on a View computer")
	}
}

func TestDropsForTheGoalRoutes(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/board/team/goals":         true,
		"/api/board/team/goals/3":       true,
		"/api/board/team/tasks":         false,
		"/api/board/team/goals/3/close": false, // a write: not a View route
	} {
		method := http.MethodGet
		if path == "/api/board/team/goals/3/close" {
			method = http.MethodPost
		}
		assert.Equal(t, want, dropsFor(method, path), path)
	}
}
