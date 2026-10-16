package server

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store"
)

// #3670, the real router with --strict-local-auth: every Operator-only route, walked from the
// table itself, refuses an agent's token with 403 forbidden_for_agent, before its handler.
func TestEveryOperatorOnlyRouteRefusesAnAgentInStrictMode(t *testing.T) {
	srv, ts, _ := strictServer(t, true)
	tok, err := srv.db.MintSessionToken(context.Background(), "sid-dev", "claude-dev")
	require.NoError(t, err)
	param := regexp.MustCompile(`\{[^}]*\}|\*`)
	keys := routes.OperatorOnlyRouteKeys()
	require.NotEmpty(t, keys)
	for _, k := range keys {
		method, pattern, _ := strings.Cut(k, " ")
		req, err := http.NewRequest(method, ts.URL+param.ReplaceAllString(pattern, "x1"), strings.NewReader("{}"))
		require.NoError(t, err)
		req.Header.Set(store.SessionTokenHeader, tok)
		req.Header.Set("User-Agent", "coral-agent/dev")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, "%s: %s", k, body)
		assert.Equal(t, "forbidden_for_agent", codeOfBody(body), k)
	}
	refused := srv.identity.Report()["refused"].(map[string]int64)
	assert.EqualValues(t, len(keys), refused["forbidden_for_agent"], "each refused by the guard, none by a handler")
}
