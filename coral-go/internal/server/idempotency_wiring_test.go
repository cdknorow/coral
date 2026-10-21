package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5841: the five routes that start something are behind the Idempotency-Key wrapper, and nothing else is. A Hub-stamped request with a hub user and a malformed key is refused by the
// wrapper itself (invalid_idempotency_key) before any handler runs, so nothing is started to prove it; a route without the wrapper answers something else.
func TestIdempotencyKeyIsWiredToTheFiveStartingRoutesOnly(t *testing.T) {
	r := newE2ERemote(t, "idem-wiring", nil)
	h := r.srv.Router()
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"launch"}`, map[string]string{"Content-Type": "application/json"}).Code, "the owner allows Launch")
	hdr := map[string]string{
		tunnel.ViaHeader:            cloud.ViaSecret(),
		tunnel.HubUserHeader:        "hub-user-1234abcd",
		tunnel.IdempotencyKeyHeader: "short",
		"Content-Type":              "application/json",
	}
	for _, path := range []string{
		"/api/sessions/launch", "/api/sessions/launch-team", "/api/sessions/live/claude-x/resume", "/api/sessions/live/claude-x/wake", "/api/tasks/run",
	} {
		rec := call(h, "POST", path, `{}`, hdr)
		assert.Equal(t, 400, rec.Code, "%s: %s", path, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "invalid_idempotency_key", path)
		// Without a key the wrapper is out of the way (the handler answers its own thing, not this).
		noKey := map[string]string{tunnel.ViaHeader: hdr[tunnel.ViaHeader], tunnel.HubUserHeader: hdr[tunnel.HubUserHeader], "Content-Type": "application/json"}
		assert.NotContains(t, call(h, "POST", path, `{}`, noKey).Body.String(), "invalid_idempotency_key", path)
	}
	for _, path := range []string{
		"/api/sessions/live/team/b-1/wake", "/api/sessions/live/wake-all", "/api/sessions/live/claude-x/restart", "/api/board/team/resume",
	} {
		rec := call(h, "POST", path, `{}`, hdr)
		assert.False(t, strings.Contains(rec.Body.String(), "invalid_idempotency_key"), "%s is not one of the five: %s", path, rec.Body.String())
	}
}
