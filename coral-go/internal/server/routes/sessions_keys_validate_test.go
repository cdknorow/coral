package routes

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
)

// validatingTerminal is the mock terminal with the PTY terminal's key check.
type validatingTerminal struct{ *mockSessionTerminal }

func (validatingTerminal) ValidateKeys(keys []string) error {
	for _, k := range keys {
		if _, err := ptymanager.KeyBytes(k, false); err != nil {
			return err
		}
	}
	return nil
}

// #17: a terminal that can't send a key name (the PTY's) gets a 400 and nothing is typed; a name it knows goes through.
func TestKeysRouteRefusesAnUnknownKeyName(t *testing.T) {
	server, h, terminal, ss := setupSessionsTestServer(t)
	h.terminal = validatingTerminal{terminal}
	const sid = "00000000-0000-0000-0000-0000000000c1"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "repo", WorkingDir: "/tmp/test"}))
	terminal.addSession("claude-"+sid, "/tmp/test")
	post := func(keys string) int {
		resp, err := http.Post(server.URL+"/api/sessions/live/claude-"+sid+"/keys", "application/json",
			bytes.NewBufferString(`{"keys": `+keys+`, "agent_type": "claude", "session_id": "`+sid+`"}`))
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusBadRequest, post(`["Down", "Enterr"]`))
	assert.Equal(t, http.StatusBadRequest, post(`["DownEnter"]`))
	terminal.mu.Lock()
	assert.Empty(t, terminal.raw["claude-"+sid], "nothing was sent")
	terminal.mu.Unlock()
	assert.Equal(t, http.StatusOK, post(`["Down", "Enter"]`))
	terminal.mu.Lock()
	assert.Equal(t, []string{"Down", "Enter"}, terminal.raw["claude-"+sid])
	terminal.mu.Unlock()
}
