package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/ptymanager"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/terminalinput"
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

// Security's N1 on #39: more than 64 keys in one request is a 400 on every backend, and nothing is sent.
func TestKeysRouteBoundsTheList(t *testing.T) {
	server, _, terminal, ss := setupSessionsTestServer(t)
	const sid = "00000000-0000-0000-0000-0000000000c2"
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "repo", WorkingDir: "/tmp/test"}))
	terminal.addSession("claude-"+sid, "/tmp/test")
	post := func(n int) int {
		keys := make([]string, n)
		for i := range keys {
			keys[i] = "Down"
		}
		body, _ := json.Marshal(map[string]any{"keys": keys, "agent_type": "claude", "session_id": sid})
		resp, err := http.Post(server.URL+"/api/sessions/live/claude-"+sid+"/keys", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, http.StatusBadRequest, post(ptymanager.MaxKeysPerRequest+1))
	terminal.mu.Lock()
	assert.Empty(t, terminal.raw["claude-"+sid])
	terminal.mu.Unlock()
	assert.Equal(t, http.StatusOK, post(ptymanager.MaxKeysPerRequest))
}

// #5715: /keys from a person (the dashboard) reaches the terminal as a person's input, so a character typed through it is logged by its
// length; from anything that is not a person it goes through the gate as Coral's.
func TestKeysRouteNamesWhoseInputItIs(t *testing.T) {
	_, h, terminal, ss := setupSessionsTestServer(t)
	const sid = "00000000-0000-0000-0000-0000000000c5"
	name := "claude-" + sid
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{SessionID: sid, AgentType: "claude", AgentName: "repo", WorkingDir: "/tmp/test"}))
	terminal.addSession(name, "/tmp/test")
	send := func(ctx context.Context) {
		req := httptest.NewRequest(http.MethodPost, "/api/sessions/live/"+name+"/keys",
			bytes.NewBufferString(`{"keys": ["x"], "agent_type": "claude", "session_id": "`+sid+`"}`))
		rc := chi.NewRouteContext()
		rc.URLParams.Add("name", name)
		req = req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, rc))
		w := httptest.NewRecorder()
		h.Keys(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
	send(context.WithValue(context.Background(), callerKindKey{}, callerDashCookie))
	terminal.mu.Lock()
	got := append([]terminalinput.Origin(nil), terminal.origins[name]...)
	terminal.mu.Unlock()
	assert.Equal(t, []terminalinput.Origin{terminalinput.Person}, got, "the dashboard's /keys is a person's input")
	send(context.Background())
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	assert.Equal(t, terminalinput.Coral, terminal.origins[name][1], "a caller that is not a person goes through the gate as Coral's")
}
