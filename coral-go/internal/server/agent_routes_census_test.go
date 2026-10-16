package server

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store/storetest"
)

const agentRoutesFile = "testdata/agent_routes.txt"

// readAgentRoutes reads the census: route -> a, b or c.
func readAgentRoutes(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(agentRoutesFile)
	require.NoError(t, err)
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		group, route, ok := strings.Cut(line, " ")
		require.True(t, ok && (group == "a" || group == "b" || group == "c"), "bad line %q", line)
		_, dup := out[route]
		require.False(t, dup, "listed twice: %s", route)
		out[route] = group
	}
	require.NoError(t, sc.Err())
	return out
}

// hubServer is the real router in hub mode (it serves every route, the server registry too).
func hubServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	cfg := newE2EConfig(t, t.TempDir())
	cfg.HubMode = true
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := newTestServer(t, cfg, db, stack.backend, stack.terminal)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return srv, ts
}

// #3929 (Security's #3965 test 2): every route the server serves has exactly one decision in
// testdata/agent_routes.txt, and "b" there is exactly the refusal table; a new route fails until
// someone decides.
func TestAgentRouteCensus(t *testing.T) {
	srv, _ := hubServer(t)
	served := map[string]bool{}
	require.NoError(t, chi.Walk(srv.Router(), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/") || strings.HasPrefix(route, "/ws/") {
			served[method+" "+route] = true
		}
		return nil
	}))
	census := readAgentRoutes(t)
	b := map[string]bool{}
	for _, k := range routes.OperatorOnlyRouteKeys() {
		b[k] = true
	}
	var missing, stale, wrong []string
	for k := range served {
		if _, ok := census[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k, g := range census {
		if !served[k] {
			stale = append(stale, k)
		}
		if (g == "b") != b[k] {
			wrong = append(wrong, g+" "+k)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	sort.Strings(wrong)
	assert.Empty(t, missing, "routes with no decision in %s: add each as a, b or c", agentRoutesFile)
	assert.Empty(t, stale, "routes in %s the server no longer serves", agentRoutesFile)
	assert.Empty(t, wrong, "'b' in %s must be exactly operatorOnlyRoutes", agentRoutesFile)
}

// #3929 (Security's #3965 test 3): an agent's token is never refused as an agent on the routes its
// tools and hooks need, so the change can't break an agent (the handler answers, whatever it says).
func TestAgentNeededRoutesStayOpen(t *testing.T) {
	srv, ts := hubServer(t)
	tok, err := srv.db.MintSessionToken(context.Background(), "sid-dev", "claude-dev")
	require.NoError(t, err)
	param := regexp.MustCompile(`\{[^}]*\}|\*`)
	n := 0
	for route, g := range readAgentRoutes(t) {
		if g != "a" {
			continue
		}
		n++
		method, pattern, _ := strings.Cut(route, " ")
		req, err := http.NewRequest(method, ts.URL+param.ReplaceAllString(pattern, "x1"), strings.NewReader("{}"))
		require.NoError(t, err)
		req.Header.Set("X-Coral-Session-Token", tok)
		req.Header.Set("User-Agent", "coral-board/dev")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		assert.NotEqual(t, "forbidden_for_agent", codeOfBody(body), route)
	}
	assert.Greater(t, n, 60)
}
