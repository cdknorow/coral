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
