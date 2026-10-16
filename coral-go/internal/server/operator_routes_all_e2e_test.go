package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #3670, the real router: every Operator-only route, walked from the table itself, refuses an
// agent's token with 403 forbidden_for_agent, before its handler, in strict mode AND with strict
// local auth off (#3929: before, only in strict mode). The API key, the Operator's machine
// credential, is never refused for that reason. Hub mode too, which adds the server registry
// (#3685); a standalone Coral doesn't serve those.
func TestEveryOperatorOnlyRouteRefusesAnAgent(t *testing.T) {
	param := regexp.MustCompile(`\{[^}]*\}|\*`)
	keys := routes.OperatorOnlyRouteKeys()
	require.NotEmpty(t, keys)
	for _, c := range []struct{ hub, strict bool }{{false, true}, {true, true}, {false, false}, {true, false}} {
		hub, strict := c.hub, c.strict
		t.Run(fmt.Sprintf("hub=%v,strict=%v", hub, strict), func(t *testing.T) {
			cfg := newE2EConfig(t, t.TempDir())
			cfg.StrictLocalAuth = strict
			cfg.HubMode = hub
			db, err := storetest.Open(t, cfg.DBPath)
			require.NoError(t, err)
			t.Cleanup(func() { db.Close() })
			stack := newTermStack(t)
			srv := newTestServer(t, cfg, db, stack.backend, stack.terminal)
			ts := httptest.NewServer(srv.Router())
			t.Cleanup(ts.Close)
			tok, err := srv.db.MintSessionToken(context.Background(), "sid-dev", "claude-dev")
			require.NoError(t, err)
			n := 0
			for _, k := range keys {
				method, pattern, _ := strings.Cut(k, " ")
				if !hub && strings.HasPrefix(pattern, "/api/servers") {
					continue
				}
				n++
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
			assert.EqualValues(t, n, refused["forbidden_for_agent"], "each refused by the guard, none by a handler")

			// The API key is never refused as an agent (the handler answers, whatever it says).
			// The key is read first: one of the routes rotates it.
			key := srv.keyStore.Key()
			for _, k := range keys {
				method, pattern, _ := strings.Cut(k, " ")
				if (!hub && strings.HasPrefix(pattern, "/api/servers")) || k == "POST /api/system/restart" || k == "POST /api/system/api-key/regenerate" {
					continue
				}
				req, err := http.NewRequest(method, ts.URL+param.ReplaceAllString(pattern, "x1"), strings.NewReader("{}"))
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+key)
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				assert.NotEqual(t, "forbidden_for_agent", codeOfBody(body), "%s with the API key", k)
			}
		})
	}
}
