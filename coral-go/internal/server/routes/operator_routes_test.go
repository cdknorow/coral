package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
)

// #3424 (audit HIGH-01): workflows, scheduled jobs and task runs are the Operator's. An agent's
// token gets 403 forbidden_for_agent in every mode (#3929; before, only with strict local
// auth), and the call is recorded in the identity report, by session and route. A request with
// no token is left to strict mode: in default mode it goes through, as before.
func TestOperatorOnlyRoutes(t *testing.T) {
	for _, strict := range []bool{false, true} {
		db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
		require.NoError(t, err)
		token, err := db.MintSessionToken(context.Background(), "sid-dev", "claude-dev")
		require.NoError(t, err)
		cookie := false
		o := NewIdentityObserver(db, nil, func(*http.Request) bool { return cookie })
		if strict {
			o.SetVerifiers(func(*http.Request) bool { return false }, func(r *http.Request) (*http.Request, bool) { return r, false })
			o.SetStrict()
		}
		reached := 0
		ok := func(w http.ResponseWriter, r *http.Request) { reached++ }
		r := chi.NewRouter()
		r.Use(o.Middleware)
		for _, p := range []string{"/api/workflows", "/api/workflows/{workflowID}/trigger", "/api/workflows/by-name/{name}/trigger", "/api/scheduled/jobs", "/api/tasks/run", "/api/workflows/runs/{runID}/kill", "/api/tasks/runs/{runID}/kill", "/api/sessions/launch", "/api/sessions/launch-team", "/api/system/login-token", "/api/system/api-key/regenerate", "/api/cloud/connect", "/api/cloud/disconnect", "/api/scheduled/jobs/{jobID}/toggle"} {
			r.Post(p, ok)
		}
		r.Put("/api/workflows/{workflowID}", ok)
		r.Put("/api/settings", ok)
		r.Put("/api/system/hub-operator", ok)
		r.Put("/api/system/hub-mode", ok)
		r.Post("/api/system/restart", ok)
		r.Put("/api/settings/git-diff-mode", ok)
		r.Get("/api/system/api-key", ok)
		r.Get("/api/system/hooks-status", ok)
		r.Post("/api/sessions/live/{sessionID}/refresh-hooks", ok)
		r.Delete("/api/board/{project}", ok)
		r.Patch("/api/board/{project}", ok)
		r.Delete("/api/board/{project}/subscribe", ok)
		r.Delete("/api/board/{project}/messages/{messageID}", ok)
		r.Post("/api/board/{project}/pause", ok)
		r.Post("/api/board/{project}/resume", ok)
		r.Post("/api/board/{project}/messages", ok)
		r.Get("/api/workflows", ok)
		r.Get("/api/worktrees", ok)
		r.Post("/api/worktrees/remove", ok)
		do := func(method, path string, hdr map[string]string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, path, strings.NewReader("{}"))
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			return rec
		}
		agent := map[string]string{store.SessionTokenHeader: token, "User-Agent": "coral-agent/dev"}
		writes := []struct{ method, path string }{
			{"POST", "/api/workflows"}, {"PUT", "/api/workflows/3"}, {"POST", "/api/workflows/3/trigger"},
			{"POST", "/api/workflows/by-name/nightly/trigger"}, {"POST", "/api/scheduled/jobs"},
			{"POST", "/api/scheduled/jobs/7/toggle"}, {"POST", "/api/tasks/run"},
			// #3445: Coral's settings and the Operator's runs
			{"PUT", "/api/settings"}, {"POST", "/api/workflows/runs/5/kill"}, {"POST", "/api/tasks/runs/9/kill"},
			// routes that were guarded only by the forgeable dashboard-provenance headers, and Coral's configuration
			{"POST", "/api/system/login-token"}, {"POST", "/api/system/api-key/regenerate"}, {"POST", "/api/cloud/connect"},
			{"POST", "/api/cloud/disconnect"}, {"PUT", "/api/system/hub-operator"},
			// #3460: Run as a hub, and restarting Coral
			{"PUT", "/api/system/hub-mode"}, {"POST", "/api/system/restart"},
			// #3513: the API key (the Operator's credential), and deleting a board
			{"GET", "/api/system/api-key"}, {"DELETE", "/api/board/coral-go-team"},
			// #3536: renaming a board
			{"PATCH", "/api/board/coral-go-team"},
			// #3668: deleting a board message, pausing and resuming a board
			{"DELETE", "/api/board/coral-go-team/messages/42"}, {"POST", "/api/board/coral-go-team/pause"},
			{"POST", "/api/board/coral-go-team/resume"},
			// #3665: which agents run an older hook set, and resuming one on the current set
			{"GET", "/api/system/hooks-status"}, {"POST", "/api/sessions/live/abc/refresh-hooks"},
			// #3929 step 2: the dashboard's own preferences are the Operator's too
			{"PUT", "/api/settings/git-diff-mode"},
			// #6736: the kept worktrees, and removing one, are the Operator's
			{"GET", "/api/worktrees"}, {"POST", "/api/worktrees/remove"},
		}
		for _, wr := range writes {
			rec := do(wr.method, wr.path, agent)
			assert.Equal(t, http.StatusForbidden, rec.Code, "strict=%v: %s", strict, wr.path)
			var out map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			assert.Equal(t, "forbidden_for_agent", out["code"], wr.path)
		}
		forged := map[string]string{store.SessionTokenHeader: token, "User-Agent": "coral-agent/dev", "Origin": "http://example.com", "Sec-Fetch-Site": "same-origin"}
		assert.Equal(t, http.StatusForbidden, do("POST", "/api/system/login-token", forged).Code, "an agent token with forged dashboard headers still can't mint a sign-in link")
		// An agent launching agents (a team launch, a terminal agent) stays allowed in every mode and is
		// recorded.
		assert.Equal(t, http.StatusOK, do("POST", "/api/sessions/launch", agent).Code, "agent launches are allowed")
		assert.Equal(t, http.StatusOK, do("POST", "/api/sessions/launch-team", agent).Code, "team launches are allowed")
		assert.Equal(t, http.StatusOK, do("DELETE", "/api/board/coral-go-team/subscribe", agent).Code, "an agent can still leave its board")
		assert.Equal(t, http.StatusOK, do("POST", "/api/board/coral-go-team/messages", agent).Code, "and post on it")
		// Reads of the board and the agent's own session stay open to agents; the workflows' own commands and prompts do not (#5075), and the Operator (here the
		// dashboard's sign-in) can write.
		assert.Equal(t, http.StatusForbidden, do("GET", "/api/workflows", agent).Code, "an agent's token reads no workflow (#5075)")
		cookie = true
		assert.Equal(t, http.StatusOK, do("POST", "/api/tasks/run", nil).Code)
		cookie = false

		calls := o.Report()["agent_operator_calls"].(map[string]int64)
		assert.EqualValues(t, 1, calls["claude-dev POST /api/tasks/run"], calls)
		assert.EqualValues(t, 1, calls["claude-dev PUT /api/workflows/{workflowID}"], calls)
		assert.EqualValues(t, 1, calls["claude-dev PUT /api/settings"], calls)
		assert.EqualValues(t, 1, calls["claude-dev POST /api/workflows/runs/{runID}/kill"], calls)
		assert.EqualValues(t, 1, calls["claude-dev POST /api/sessions/launch"], calls)
		assert.EqualValues(t, 1, calls["claude-dev POST /api/sessions/launch-team"], calls)
		assert.EqualValues(t, 1, calls["claude-dev GET /api/workflows"], calls)
		assert.Len(t, calls, len(writes)+3, "every agent write, launch and refused read recorded once, by route pattern (no ids)")
		if !strict {
			// No token: left to strict mode, so in default mode it goes through (and is recorded).
			assert.Equal(t, http.StatusOK, do("POST", "/api/scheduled/jobs", map[string]string{"User-Agent": "coral-agent/dev"}).Code)
			assert.Equal(t, http.StatusOK, do("PUT", "/api/settings", nil).Code, "a tokenless local request in default mode")
			calls = o.Report()["agent_operator_calls"].(map[string]int64)
			assert.EqualValues(t, 1, calls["(no token) POST /api/scheduled/jobs"])
		}
		db.Close()
	}
}
