package hubproxy

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/subgentic/coral/pkg/tunnel"
)

// #5841: the hub forwards Idempotency-Key (and the hub user with it) only with a POST to the six routes that start something, as the connector does, in any spelling.
func TestAllowlistHeadersIdempotencyKey(t *testing.T) {
	for _, c := range []struct {
		method, path string
		kept         bool
	}{
		{http.MethodPost, "/api/sessions/launch", true},
		{http.MethodPost, "/api/sessions/launch-team", true},
		{http.MethodPost, "/api/sessions/live/claude-abc/resume", true},
		{http.MethodPost, "/api/sessions/live/claude-abc/wake", true},
		{http.MethodPost, "/api/tasks/run", true},
		{http.MethodPost, "/api/board/team/goals", true},
		{http.MethodPost, "/api/board/team/tasks", true},
		{http.MethodPost, "/api/board/team/goals/3/close", false},
		{http.MethodGet, "/api/sessions/launch", false},
		{http.MethodPost, "/api/sessions/live/team/b-1/wake", false},
		{http.MethodPost, "/api/sessions/live/x/send", false},
		{http.MethodPost, "/api/board/team/messages", false},
	} {
		h := http.Header{"idempotency-key": {"0123456789abcdef-a"}, "Idempotency-Key": {"0123456789abcdef-b"}, "x-coral-hub-user": {"hub-user-1234abcd"}, "Accept": {"application/json"}, "Cookie": {"x=y"}}
		allowlistHeaders(h, c.method, c.path)
		_, lower := h["idempotency-key"]
		_, canon := h[tunnel.IdempotencyKeyHeader]
		_, user := h["x-coral-hub-user"]
		assert.Equal(t, c.kept, lower, "%s %s", c.method, c.path)
		assert.Equal(t, c.kept, canon, "%s %s", c.method, c.path)
		if !strings.HasPrefix(c.path, "/api/board/") { // a board write keeps the hub user whether or not it takes the key
			assert.Equal(t, c.kept, user, "%s %s: the hub user goes with the key", c.method, c.path)
		}
		assert.Contains(t, h, "Accept")
		assert.NotContains(t, h, "Cookie")
	}
}
