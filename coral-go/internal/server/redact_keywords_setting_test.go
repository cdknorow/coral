package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/server/routes"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #5141's switch for the keyword rules: on by default, written only by the local operator through PUT /api/settings (never through Coral Hub at
// any mode), read for the replies.
func TestRedactKeywordsSwitch(t *testing.T) {
	require.Equal(t, tunnel.TierNever, tunnel.Classify("PUT", "/api/settings", ""))
	r := newE2ERemote(t, "redact-keywords", nil)
	h := r.srv.Router()
	js := map[string]string{"Content-Type": "application/json"}
	hub := map[string]string{tunnel.ViaHeader: cloud.ViaSecret(), "Content-Type": "application/json"}
	read := func() bool { return routes.RedactKeywordsFromSettings(store.NewSessionStore(r.db))() } // a fresh reader: no cached value
	assert.True(t, read(), "on by default")

	for _, m := range []string{"view", "interact", "launch"} {
		require.Equal(t, 200, call(h, "PUT", "/api/cloud/max-mode", `{"mode":"`+m+`"}`, js).Code)
		assert.Equal(t, 403, call(h, "PUT", "/api/settings", `{"hub_redact_keywords":false}`, hub).Code, "never through the hub (%s)", m)
	}
	assert.True(t, read())

	require.Equal(t, 200, call(h, "PUT", "/api/settings", `{"hub_redact_keywords":false}`, js).Code)
	assert.False(t, read(), "off")
	require.Equal(t, 200, call(h, "PUT", "/api/settings", `{"hub_redact_keywords":true}`, js).Code)
	assert.True(t, read(), "on again")
	require.Equal(t, 400, call(h, "PUT", "/api/settings", `{"hub_redact_keywords":"maybe"}`, js).Code, "a bool")
}
