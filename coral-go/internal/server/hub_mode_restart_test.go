package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// Run as a hub off at a restart keeps the hub's data (#3460): the server list is only
// unrouted while hub mode is off, and is all there when it comes back on.
func TestHubModeOffKeepsTheServers(t *testing.T) {
	r := newE2ERemote(t, "hubdata", nil)
	rs := store.NewRemoteServerStore(r.db, r.cfg.CoralDir())
	_, err := rs.Add(context.Background(), "box", "Box", "https://box.example.com:8420", "k", false)
	require.NoError(t, err)

	off := *r.cfg
	off.HubMode = false
	require.Equal(t, 404, call(newTestServer(t, &off, r.db, r.backend, nil).Router(), "GET", "/api/servers", "", nil).Code, "not a hub: no /api/servers")

	on := *r.cfg
	on.HubMode = true
	rec := call(newTestServer(t, &on, r.db, r.backend, nil).Router(), "GET", "/api/servers", "", nil)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"id":"box"`, "the server added before is still there")
}
