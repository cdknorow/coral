package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #4964: chi routes on the RAW path when a request has one, and urlParam then decodes the parameter, so "/api/sessions/live/a%2Fb/display-name"
// reaches the handler as the name "a/b". The route lookups the gates make before routing (operator-only, body limits, the identity actor's route)
// must find the same route chi will: looked up by the decoded path, "a/b" is two segments, no route matches, and the gate is skipped.
func TestRouteGatesFindTheRouteChiWillRun(t *testing.T) {
	var route string
	var only, ok, reached bool
	var limit int64
	var actorRoute, name string
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
			route, only, ok = guardedRoute(q)
			limit = bodyLimitFor(q)
			actorRoute = routeOf(q)
			next.ServeHTTP(w, q)
		})
	})
	r.Put("/api/sessions/live/{name}/display-name", func(w http.ResponseWriter, q *http.Request) {
		reached = true
		name = urlParam(q, "name") // the handler sees the decoded name
	})
	for path, want := range map[string]string{"/api/sessions/live/plain/display-name": "plain", "/api/sessions/live/a%2Fb/display-name": "a/b", "/api/sessions/live/a%2fb/display-name": "a/b"} {
		route, only, ok, reached, limit, actorRoute, name = "", false, false, false, 0, "", ""
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "http://x"+path, nil))
		require.True(t, reached, path)
		assert.Equal(t, want, name, path)
		assert.True(t, ok && only, "%s is found as an operator-only route (got %q)", path, route)
		assert.Equal(t, "/api/sessions/live/{name}/display-name", route, path)
		assert.Equal(t, "/api/sessions/live/{name}/display-name", actorRoute, "routeOf finds it too: %s", path)
		assert.NotZero(t, limit, path)
	}
}

// #4977: the sites that read a board's name from the path read it as chi and urlParam do: "/api/board/a%2Fb/messages" is the project "a/b" with the
// tail "messages", not the project "a" with the tail "b/messages" (which is what the decoded path says).
func TestBoardRouteReadsTheProjectAsChiDoes(t *testing.T) {
	for _, c := range []struct{ path, project, tail string }{
		{"/api/board/team/messages", "team", "messages"},
		{"/api/board/team%20x/messages", "team x", "messages"},
		{"/api/board/a%2Fb/messages", "a/b", "messages"},
		{"/api/board/a%2fb/reminder", "a/b", "reminder"},
		{"/api/board/team/tasks/7", "team", "tasks/7"},
	} {
		req := httptest.NewRequest(http.MethodPost, "http://x"+c.path, nil)
		project, tail, ok := boardRoute(req)
		require.True(t, ok, c.path)
		assert.Equal(t, c.project, project, c.path)
		assert.Equal(t, c.tail, tail, c.path)
	}
	_, _, ok := boardRoute(httptest.NewRequest(http.MethodPost, "http://x/api/sessions/live/x/kill", nil))
	assert.False(t, ok)

	// the callers
	assert.True(t, isBoardChatPost(httptest.NewRequest(http.MethodPost, "http://x/api/board/a%2Fb/messages", nil)), "an encoded slash in the name is still a board post")
	assert.False(t, isBoardChatPost(httptest.NewRequest(http.MethodPost, "http://x/api/board/a/b/messages", nil)), "a real extra segment is not")
	assert.True(t, isBoardReminderWrite(httptest.NewRequest(http.MethodDelete, "http://x/api/board/a%2Fb/reminder", nil)))
	assert.True(t, isRelayedFileRoute(httptest.NewRequest(http.MethodPost, "http://x/api/board/a%2Fb/external-artifacts", nil)), "chi routes it to the file handler: its body is not read as JSON")
	assert.False(t, isRelayedFileRoute(httptest.NewRequest(http.MethodPost, "http://x/api/board/a/b/external-artifacts", nil)))
	assert.True(t, strictOpenPaths["GET "+routedPath(httptest.NewRequest(http.MethodGet, "http://x/api/health", nil))])
}
