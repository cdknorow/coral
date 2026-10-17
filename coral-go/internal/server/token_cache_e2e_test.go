package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store"
)

// #4303, the real router: the token-to-owner cache never accepts a token the very next request
// after it was rotated or its session replaced or ended, a restarted session's new token works at
// once, and an agent's board request resolves its board ONCE (the identity layer and the board
// resolver share the answer). In strict mode and with it off.
func TestTokenCacheNeverAcceptsARevokedToken(t *testing.T) {
	for _, strict := range []bool{true, false} {
		t.Run(fmt.Sprintf("strict=%v", strict), func(t *testing.T) {
			srv, ts, _ := strictServer(t, strict)
			ctx := context.Background()
			ss := store.NewSessionStore(srv.db)
			require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: "sid-1", AgentType: "claude", AgentName: "dev", WorkingDir: t.TempDir()}))

			// The agent is a member of the board, so the request passes the identity check and reaches
			// the board resolver after it: both ask which board "team" is.
			_, err := srv.boardStore.Subscribe(ctx, "team", "Dev", "Developer", "claude-dev", nil, "")
			require.NoError(t, err)

			call := func(token string) (int, string) {
				req, _ := http.NewRequest("GET", ts.URL+"/api/board/team/messages/check?subscriber_id=Dev", nil)
				req.Header.Set(store.SessionTokenHeader, token)
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				return resp.StatusCode, string(b)
			}
			refused := func(code int, body string) bool {
				return code == http.StatusUnauthorized && codeOfBody([]byte(body)) == "session_token_required"
			}

			tok1, err := srv.db.MintSessionToken(ctx, "sid-1", "claude-dev")
			require.NoError(t, err)
			before := routes.BoardResolveCount()
			code, body := call(tok1) // warms the cache
			assert.False(t, refused(code, body), "a live token: %d %s", code, body)
			assert.EqualValues(t, 1, routes.BoardResolveCount()-before, "one board resolve for the request")
			lookups, hits := srv.db.SessionTokenCacheStats()
			call(tok1)
			l2, h2 := srv.db.SessionTokenCacheStats()
			assert.EqualValues(t, 1, l2-lookups)
			assert.EqualValues(t, 1, h2-hits, "the second request is answered from the cache")

			// Rotation: the old token is refused (strict) on the very next request; the new one works.
			tok2, err := srv.db.MintSessionToken(ctx, "sid-1", "claude-dev")
			require.NoError(t, err)
			code, body = call(tok1)
			assert.Equal(t, strict, refused(code, body), "the rotated token: %d %s", code, body)
			code, body = call(tok2)
			assert.False(t, refused(code, body), "the new token works at once: %d %s", code, body)
			call(tok2)

			// A restart replaces the session: the old token is refused at once, the new session's works at once.
			require.NoError(t, ss.ReplaceLiveSession(ctx, "sid-1", &store.LiveSession{SessionID: "sid-2", AgentType: "claude", AgentName: "dev", WorkingDir: t.TempDir()}))
			code, body = call(tok2)
			assert.Equal(t, strict, refused(code, body), "the replaced session's token: %d %s", code, body)
			tok3, err := srv.db.MintSessionToken(ctx, "sid-2", "claude-dev")
			require.NoError(t, err)
			code, body = call(tok3)
			assert.False(t, refused(code, body), "the restarted session's token: %d %s", code, body)
			call(tok3)

			// The session ends.
			require.NoError(t, ss.UnregisterLiveSession(ctx, "sid-2"))
			code, body = call(tok3)
			assert.Equal(t, strict, refused(code, body), "an ended session's token: %d %s", code, body)
		})
	}
}
