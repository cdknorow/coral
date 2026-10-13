package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// #3211: when the agent's own terminal is gone, every write and terminal action answers
// 409 {code: terminal_gone, error: "The agent's terminal is gone"} and sends nothing.
func TestRoutesAnswerTerminalGone(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	const sid = "00000000-0000-0000-0000-000000003211"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "team", WorkingDir: "/tmp/team"}))
	terminal.addSession("team", "/tmp/team") // a sibling pane by the same name: never used
	terminal.gone = true

	ident := `"agent_type":"claude","session_id":"` + sid + `"`
	for _, c := range []struct{ path, body string }{
		{"send", `{"command":"echo hi",` + ident + `}`},
		{"keys", `{"keys":["Escape"],` + ident + `}`},
		{"resize", `{"columns":100,` + ident + `}`},
		{"answer-prompt", `{"n":1,"label":"Yes",` + ident + `}`},
		{"restart", `{` + ident + `}`},
	} {
		resp, err := http.Post(server.URL+"/api/sessions/live/team/"+c.path, "application/json", bytes.NewBufferString(c.body))
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		resp.Body.Close()
		assert.Equal(t, http.StatusConflict, resp.StatusCode, c.path)
		assert.Equal(t, "terminal_gone", got["code"], c.path)
		assert.Equal(t, "The agent's terminal is gone", got["error"], c.path)
	}
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	assert.Empty(t, terminal.sent, "nothing was typed anywhere")
	assert.Empty(t, terminal.raw, "no keys were sent anywhere")

	// prompt-options is a read: an empty prompt, not an error.
	terminal.mu.Unlock()
	resp, err := http.Get(server.URL + "/api/sessions/live/team/prompt-options?agent_type=claude&session_id=" + sid)
	terminal.mu.Lock()
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
