package server

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reviewed list of how each identity-attributing route is bound (routes/testdata/
// identity_route_bindings.txt, #4244) is built from the source; this checks it against the real router,
// so a route registered some way the source scan did not read can't be missing from it, and a route
// that was removed doesn't linger in it.
func TestRouteBindingListCoversTheRealRouter(t *testing.T) {
	srv, _, _ := strictServer(t, true)
	raw, err := os.ReadFile("routes/testdata/identity_route_bindings.txt")
	require.NoError(t, err)
	listed := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, _, _ := strings.Cut(line, " | ")
		listed[key] = true
	}

	real := map[string]bool{}
	require.NoError(t, chi.Walk(srv.Router(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/")
		if route == "" {
			route = "/"
		}
		real[method+" "+route] = true
		return nil
	}))
	require.Greater(t, len(real), 300, "the walk must find the router's routes (found %d)", len(real))

	var missing, stale []string
	for key := range real {
		_, path, _ := strings.Cut(key, " ")
		if (strings.HasPrefix(path, "/api/board/") || strings.HasPrefix(path, "/api/agent/") || strings.HasPrefix(path, "/api/sessions/")) && !listed[key] {
			missing = append(missing, key)
		}
	}
	for key := range listed {
		if !real[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	assert.Empty(t, missing, "routes the binding list does not have: the source scan did not read how they are registered")
	assert.Empty(t, stale, "routes in the binding list that the router no longer has")
}
