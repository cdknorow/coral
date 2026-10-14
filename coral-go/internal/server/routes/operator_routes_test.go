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

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #3424 (audit HIGH-01): workflows, scheduled jobs and task runs are the Operator's. With
// strict local auth an agent's token gets 403 forbidden_for_agent; otherwise the call goes
// through and is recorded in the identity report, by session and route.
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
		for _, p := range []string{"/api/workflows", "/api/workflows/{workflowID}/trigger", "/api/workflows/by-name/{name}/trigger", "/api/scheduled/jobs", "/api/tasks/run", "/api/scheduled/jobs/{jobID}/toggle"} {
			r.Post(p, ok)
		}
		r.Put("/api/workflows/{workflowID}", ok)
		r.Get("/api/workflows", ok)
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
		}
		for _, wr := range writes {
			rec := do(wr.method, wr.path, agent)
			if strict {
				assert.Equal(t, http.StatusForbidden, rec.Code, wr.path)
				var out map[string]any
				_ = json.Unmarshal(rec.Body.Bytes(), &out)
				assert.Equal(t, "forbidden_for_agent", out["code"], wr.path)
			} else {
				assert.Equal(t, http.StatusOK, rec.Code, "default mode: recorded, not refused: %s", wr.path)
			}
		}
		// Reads stay open to agents; the Operator (here the dashboard's sign-in) can write.
		assert.Equal(t, http.StatusOK, do("GET", "/api/workflows", agent).Code)
		cookie = true
		assert.Equal(t, http.StatusOK, do("POST", "/api/tasks/run", nil).Code)
		cookie = false

		calls := o.Report()["agent_operator_calls"].(map[string]int64)
		assert.EqualValues(t, 1, calls["claude-dev POST /api/tasks/run"], calls)
		assert.EqualValues(t, 1, calls["claude-dev PUT /api/workflows/{workflowID}"], calls)
		assert.Len(t, calls, len(writes), "every agent write recorded once, by route pattern (no ids)")
		if !strict {
			do("POST", "/api/scheduled/jobs", map[string]string{"User-Agent": "coral-agent/dev"})
			calls = o.Report()["agent_operator_calls"].(map[string]int64)
			assert.EqualValues(t, 1, calls["(no token) POST /api/scheduled/jobs"])
		}
		db.Close()
	}
}
