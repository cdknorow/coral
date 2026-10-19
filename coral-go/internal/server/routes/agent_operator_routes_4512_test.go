package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// The routes #4508 found open to an agent's token (class c), none of which any agent tool calls (a grep of
// coral-board, coral-agent, the hooks and the mock agent found none): the terminal stream, turning every agent's
// auto-accept off, the dashboard's upload and its removal, and opening a file in the Operator's editor (#4512).
var formerlyOpenOperatorRoutes = []string{
	"GET /ws/terminal/{name}",
	"POST /api/auto-accept/disable-all",
	"POST /api/upload",
	"DELETE /api/upload/{name}",
	"GET /api/upload/{name}", // #5028: an agent token reads no upload either
	"POST /api/sessions/live/{name}/open-in-editor",
}

// Each is refused to an agent's token, in default and strict mode, with 403 forbidden_for_agent and the route's
// reason, while a caller that presents no token (the dashboard, the command line) still reaches the handler.
func TestAgentTokenIsRefusedOnTheFormerlyOpenOperatorRoutes(t *testing.T) {
	for _, strict := range []bool{false, true} {
		db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
		require.NoError(t, err)
		token, err := db.MintSessionToken(context.Background(), "sid-dev", "claude-dev")
		require.NoError(t, err)
		o := NewIdentityObserver(db, nil, func(*http.Request) bool { return false })
		if strict {
			o.SetVerifiers(func(*http.Request) bool { return false }, func(r *http.Request) (*http.Request, bool) { return r, false })
			o.SetStrict()
		}
		r := chi.NewRouter()
		r.Use(o.Middleware)
		reached := 0
		for _, k := range formerlyOpenOperatorRoutes {
			method, pattern, _ := strings.Cut(k, " ")
			r.MethodFunc(method, pattern, func(w http.ResponseWriter, r *http.Request) { reached++ })
		}
		for _, k := range formerlyOpenOperatorRoutes {
			method, pattern, _ := strings.Cut(k, " ")
			assert.NotEmpty(t, operatorOnlyRoutes[k], "%s is in operatorOnlyRoutes", k)
			req := httptest.NewRequest(method, guardConcretePath(pattern), strings.NewReader("{}"))
			req.Header.Set(store.SessionTokenHeader, token)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s (strict=%v)", k, strict)
			assert.Contains(t, rec.Body.String(), "forbidden_for_agent", k)
		}
		assert.Zero(t, reached, "no handler ran for an agent's token")
		if !strict { // the Operator's scripts and the dashboard present no token and are not refused
			for _, k := range formerlyOpenOperatorRoutes {
				method, pattern, _ := strings.Cut(k, " ")
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(method, guardConcretePath(pattern), strings.NewReader("{}")))
				assert.Equal(t, http.StatusOK, rec.Code, k)
			}
			assert.Equal(t, len(formerlyOpenOperatorRoutes), reached, "a token-less caller reaches each handler")
		}
		db.Close()
	}
}

// The terminal stream itself refuses an agent's verified session before the upgrade, so typing and resizing
// through it are out of reach too, even if the identity middleware were not in front of it.
func TestWSTerminalRefusesAnAgentSessionBeforeTheUpgrade(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	h := NewSessionsHandler(db, &config.Config{WSPollIntervalS: 1, LogDir: t.TempDir()}, nil, nil, nil)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if sid := req.Header.Get(testAgentHeader); sid != "" {
				req = req.WithContext(context.WithValue(req.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: sid, SessionName: "agent-" + sid}))
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/ws/terminal/{name}", h.WSTerminal)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	url := "ws" + srv.URL[4:] + "/ws/terminal/claude-x?agent_type=claude&session_id=sid-x"
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{testAgentHeader: []string{"sid-agent"}}})
	if conn != nil {
		conn.CloseNow()
	}
	require.Error(t, err, "an agent's session does not get a terminal stream")
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	// No agent session: the handler goes on (here to its own refusal of an unknown target, not a 403 for the caller).
	conn, resp, err = websocket.Dial(ctx, url, nil)
	if conn != nil {
		conn.CloseNow()
	}
	if err != nil && resp != nil {
		assert.NotEqual(t, http.StatusForbidden, resp.StatusCode, "the Operator is not refused as an agent")
	}
}
