package routes

import (
	"context"
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

// The commands, prompts and flags of workflows and scheduled jobs, and what their runs wrote, are the Operator's (#5075): they belong to no board, an agent
// cannot create or run one, and a step's command often holds a credential. An agent's token is refused on every read of them, with 403
// forbidden_for_agent, while a request with no token (the dashboard, the command line) reaches the handler.
func TestAgentTokenIsRefusedOnWorkflowAndJobReads(t *testing.T) {
	routes := []string{
		"GET /api/workflows", "GET /api/workflows/{workflowID}", "GET /api/workflows/by-name/{name}", "GET /api/workflows/runs/recent",
		"GET /api/workflows/runs/{runID}", "GET /api/workflows/{workflowID}/runs", "GET /api/workflows/runs/{runID}/steps/{stepIndex}/files/*",
		"GET /api/scheduled/jobs", "GET /api/scheduled/jobs/{jobID}", "GET /api/scheduled/jobs/{jobID}/runs", "GET /api/scheduled/runs/recent",
	}
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	token, err := db.MintSessionToken(context.Background(), "sid-dev", "claude-dev")
	require.NoError(t, err)
	o := NewIdentityObserver(db, nil, func(*http.Request) bool { return false })
	r := chi.NewRouter()
	r.Use(o.Middleware)
	reached := 0
	for _, k := range routes {
		method, pattern, _ := strings.Cut(k, " ")
		r.MethodFunc(method, pattern, func(w http.ResponseWriter, r *http.Request) { reached++ })
	}
	for _, k := range routes {
		method, pattern, _ := strings.Cut(k, " ")
		assert.NotEmpty(t, operatorOnlyRoutes[k], "%s is in operatorOnlyRoutes", k)
		req := httptest.NewRequest(method, guardConcretePath(pattern), nil)
		req.Header.Set(store.SessionTokenHeader, token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code, k)
		assert.Contains(t, rec.Body.String(), "forbidden_for_agent", k)
	}
	assert.Zero(t, reached, "no handler ran for an agent's token")
	for _, k := range routes {
		method, pattern, _ := strings.Cut(k, " ")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(method, guardConcretePath(pattern), nil))
		assert.NotEqual(t, http.StatusForbidden, rec.Code, "%s without a token reaches the handler", k)
	}
	assert.Equal(t, len(routes), reached)
}
