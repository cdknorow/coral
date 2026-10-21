package server

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/server/routes"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #3560's switch: on by default, changed only on this computer (the dashboard or the signed
// command line), never through Coral Hub at any mode, and read for the replies (cached a second).
func TestRedactTokensSwitch(t *testing.T) {
	require.Equal(t, tunnel.TierNever, tunnel.Classify("PUT", "/api/cloud/redact-tokens", ""))
	r := newE2ERemote(t, "redact-switch", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), "Content-Type": "application/json"}
	ss := store.NewSessionStore(r.db)
	read := func() bool { return routes.RedactTokensFromSettings(ss)() } // a fresh reader: no cached value
	status := func() map[string]any {
		rec := call(h, "GET", "/api/cloud/status", "", nil)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var m map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &m))
		return m
	}
	assert.True(t, read(), "on by default")
	assert.Equal(t, true, status()["redact_tokens"])

	for _, m := range []string{"view", "interact", "launch"} {
		require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"`+m+`"}`, js).Code)
		assert.Equal(t, 403, call(h, "PUT", "/api/cloud/redact-tokens", `{"redact":false}`, hub).Code, "never through the hub (%s)", m)
	}
	assert.True(t, read())

	require.Equal(t, 400, call(h, "PUT", "/api/cloud/redact-tokens", `{}`, js).Code, "redact is required")
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/redact-tokens", `{"redact":false}`, js).Code)
	assert.False(t, read(), "off")
	assert.Equal(t, false, status()["redact_tokens"])
	require.Equal(t, 200, call(h, "PUT", "/api/cloud/redact-tokens", `{"redact":true}`, js).Code)
	assert.True(t, read())
}
