package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hubSnapshotStatus is a server's status as the dashboard shows it (the hub worker's own view).
func hubSnapshotStatus(h *e2eHub, id string) string {
	for _, s := range h.srv.remoteAgents.Snapshot().Servers {
		if s.ID == id {
			return s.Status
		}
	}
	return "(absent)"
}

// #3649: editing a server's key from a rejected one to the right one clears "Key rejected" at
// once, in the registry and in the dashboard's view, without waiting out the worker's backoff.
func TestHubE2E_KeyEditClearsKeyRejectedAtOnce(t *testing.T) {
	env := newE2EEnv(t)
	prev := remoteAgentHubConfig
	remoteAgentHubConfig.MinBackoff, remoteAgentHubConfig.MaxBackoff = 30*time.Second, 30*time.Second
	t.Cleanup(func() { remoteAgentHubConfig = prev })
	hub := newE2EHub(t, env, "", true)
	a := newE2ERemote(t, "a", nil)
	require.Equal(t, 201, hub.register("a", a.url(), a.key(), true).Code)

	_, err := a.srv.keyStore.RegenerateKey() // the hub's stored key is now the stale one
	require.NoError(t, err)
	hub.do("GET", "/api/remote/a/api/sessions/live", nil)
	hub.eventually("Key rejected after the remote's key changed", func() bool {
		return hubSnapshotStatus(hub, "a") == "unauthorized"
	})

	code, body := hub.do("PATCH", "/api/servers/a", map[string]any{"api_key": a.key()})
	require.Equal(t, http.StatusOK, code, string(body))
	assert.Equal(t, "online", hub.serverStatus("a"), "the registry row")
	deadline := time.Now().Add(2 * time.Second) // far less than the 30 s backoff
	for hubSnapshotStatus(hub, "a") != "online" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	assert.Equal(t, "online", hubSnapshotStatus(hub, "a"), "the dashboard's view clears at once")
}

// #3649: a remote's application-level 401 (a coral-board call through the hub without the
// remote's session token, answered with a JSON code) is that request's answer, not a rejected
// API key: the server never flashes "Key rejected" while its traffic works.
func TestHubE2E_ApplicationLevel401IsNotAKeyFailure(t *testing.T) {
	env := newE2EEnv(t)
	hub := newE2EHub(t, env, "", true)
	a := newE2ERemote(t, "a", nil)
	require.Equal(t, 201, hub.register("a", a.url(), a.key(), true).Code)

	// A hook's turn-end call without the remote's session token: the remote answers 401
	// session_token_required (JSON), an answer about that call, not about the hub's key.
	req, err := http.NewRequest("POST", hub.ts.URL+"/api/remote/a/api/sessions/live/claude-x/turn-end", strings.NewReader(`{}`))
	require.NoError(t, err)
	req.Header.Set("User-Agent", "coral-hook-agentic-state/1.4.48")
	req.Header.Set("Content-Type", "application/json")
	code, body := hub.send(req)
	require.Equal(t, http.StatusUnauthorized, code, "the remote's own answer is passed on: %s", body)
	assert.Contains(t, string(body), "session_token_required")
	for i := 0; i < 5; i++ {
		assert.NotEqual(t, "unauthorized", hubSnapshotStatus(hub, "a"), "probe %d", i)
		assert.NotEqual(t, "unauthorized", hub.serverStatus("a"), "probe %d", i)
		time.Sleep(60 * time.Millisecond)
	}
	assert.NotContains(t, string(body), "rejected API key")
}
