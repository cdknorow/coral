package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// #4835: the display name, icon and colour of a session are shown as fact (a tile, the Hub's list). An agent's token is refused on all three,
// for its own session and another's, and the name is unchanged; the dashboard still sets them.
func TestAgentTokenCannotRenameOrRecolourAnAgent(t *testing.T) {
	srv, ts, _ := strictServer(t, false)
	ctx := context.Background()
	ss := store.NewSessionStore(srv.db)
	sidA, sidV := "aaaaaaaa-4835-0000-0000-00000000000a", "bbbbbbbb-4835-0000-0000-00000000000b"
	for _, sid := range []string{sidA, sidV} {
		require.NoError(t, ss.RegisterLiveSession(ctx, &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "agent-" + sid[:4], WorkingDir: "/tmp/" + sid[:4]}))
	}
	tokA, err := srv.db.MintSessionToken(ctx, sidA, "claude-"+sidA)
	require.NoError(t, err)
	asAgent := http.Header{store.SessionTokenHeader: {tokA}}
	dashboard := http.Header{"Origin": {ts.URL}, "Sec-Fetch-Site": {"same-origin"}}
	put := func(path, body string, hdr http.Header) (int, string) {
		req, _ := http.NewRequest("PUT", ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for _, c := range []struct{ path, field, value string }{
		{"/api/sessions/live/x/display-name", "display_name", "Orchestrator"},
		{"/api/sessions/live/x/icon", "icon", "👑"},
		{"/api/sessions/live/x/name-color", "color", "#ff0000"},
	} {
		for _, sid := range []string{sidV, sidA} { // another agent's, and its own
			code, out := put(c.path, `{"session_id":"`+sid+`","`+c.field+`":"`+c.value+`"}`, asAgent)
			assert.Equal(t, http.StatusForbidden, code, "%s for %s: %s", c.path, sid[:4], out)
			assert.Contains(t, out, "forbidden_for_agent", c.path)
		}
		code, out := put(c.path, `{"session_id":"`+sidV+`","`+c.field+`":"`+c.value+`"}`, dashboard)
		assert.Equal(t, http.StatusOK, code, "the dashboard still sets it: %s %s", c.path, out)
	}
}
