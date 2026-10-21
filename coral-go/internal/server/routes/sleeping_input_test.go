package routes

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/subgentic/coral/internal/store"
)

// A sleeping agent takes no input: /send and /keys addressed to it (with the
// session_id and agent_type every client sends) answer 409 and nothing reaches
// the terminal, even when a pane with the agent's name is still around.
func TestSleepingAgentRefusesSendAndKeys(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	sid := "33333333-3333-4333-8444-555555555561"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		SessionID: sid, AgentType: "claude", AgentName: "napper", WorkingDir: "/tmp/napper", IsSleeping: 1,
	}))
	terminal.addSession("napper", "/tmp/napper")

	for _, c := range []struct{ path, body string }{
		{"/api/sessions/live/napper/send", `{"command":"hello","agent_type":"claude","session_id":"` + sid + `"}`},
		{"/api/sessions/live/napper/keys", `{"keys":["Enter"],"agent_type":"claude","session_id":"` + sid + `"}`},
		{"/api/sessions/live/napper/send", `{"command":"hello","session_id":"` + sid + `"}`},
		{"/api/sessions/live/napper/keys", `{"keys":["Escape"],"agent_type":"claude"}`},
	} {
		resp, err := http.Post(server.URL+c.path, "application/json", strings.NewReader(c.body))
		require.NoError(t, err)
		resp.Body.Close()
		assert.NotEqual(t, http.StatusOK, resp.StatusCode, "%s %s", c.path, c.body)
		if strings.Contains(c.body, `"agent_type":"claude","session_id"`) {
			assert.Equal(t, http.StatusConflict, resp.StatusCode, "%s %s", c.path, c.body)
		}
	}
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	assert.Empty(t, terminal.sent["napper"], "no input reached the sleeping agent's pane")
	assert.Empty(t, terminal.raw["napper"], "no keys reached the sleeping agent's pane")
}
