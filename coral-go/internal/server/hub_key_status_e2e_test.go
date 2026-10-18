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
	// A long backoff so a clear can only come from the key edit, never from the worker's own next
	// dial: eventually (below) waits up to 10 s, far short of this, so a pass means the edit cleared
	// it at once (#4909 widened this from 30 s, which a loaded gate's 2 s wait could race).
	remoteAgentHubConfig.MinBackoff, remoteAgentHubConfig.MaxBackoff = 60*time.Second, 60*time.Second
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
	// The dashboard's view clears at once: eventually polls for up to 10 s, far below the 60 s backoff,
	// so a pass is the edit clearing it, not the worker's next dial.
	hub.eventually("the dashboard's view clears at once", func() bool {
		return hubSnapshotStatus(hub, "a") == "online"
	})
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
