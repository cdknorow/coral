package tunnel

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #5841: the connector forwards the Idempotency-Key header, and the Coral Hub user behind it, ONLY on a POST to one of the five routes that start something (launch, launch-team, one
// agent's resume and wake, a task run); every other route and method, and a team's wake, drop both. The key is never the connector's to invent: it is not stamped, only carried.
func TestScrubForwardsTheIdempotencyKeyOnlyOnTheFiveStartingRoutes(t *testing.T) {
	in := http.Header{}
	in.Set(IdempotencyKeyHeader, "0123456789abcdef-key")
	in.Set(HubUserHeader, "hub-user-1234abcd")
	for _, tc := range []struct {
		method, path string
		kept         bool
	}{
		{http.MethodPost, "/api/sessions/launch", true},
		{http.MethodPost, "/api/sessions/launch-team", true},
		{http.MethodPost, "/api/sessions/live/claude-abc/resume", true},
		{http.MethodPost, "/api/sessions/live/claude-abc/wake", true},
		{http.MethodPost, "/api/tasks/run", true},
		{http.MethodPost, "/api/board/team/goals", true}, // a goal create and a task add (#7087): coral-hub's key comes from a five-minute preview token
		{http.MethodPost, "/api/board/team/tasks", true},
		{http.MethodGet, "/api/board/team/goals", false},
		{http.MethodPatch, "/api/board/team/goals", false},
		{http.MethodPost, "/api/board/team/goals/3/close", false},
		{http.MethodPost, "/api/board/team/tasks/3/complete", false},
		{http.MethodPost, "/api/board/a/b/tasks", false},
		{http.MethodPost, "/api/board/team/tasks/claim", false},
		{http.MethodGet, "/api/sessions/launch", false},
		{http.MethodPut, "/api/tasks/run", false},
		{http.MethodDelete, "/api/sessions/live/claude-abc/wake", false},
		{http.MethodPost, "/api/sessions/live/team/b-1/wake", false}, // a team's wake is not one of the five
		{http.MethodPost, "/api/sessions/live/wake-all", false},
		{http.MethodPost, "/api/sessions/live/x/restart", false},
		{http.MethodPost, "/api/sessions/live/x/send", false},
		{http.MethodPost, "/api/sessions/launch/x", false},
		{http.MethodPost, "/api/sessions/live/a/b/resume", false},
		{http.MethodPost, "/api/tasks/run/1", false},
		{http.MethodPost, "/api/board/team/messages", false}, // a board write keeps the hub user, not the key
	} {
		out := scrub(in, tc.method, tc.path)
		if tc.kept {
			assert.Equal(t, "0123456789abcdef-key", out.Get(IdempotencyKeyHeader), "%s %s", tc.method, tc.path)
			assert.Equal(t, "hub-user-1234abcd", out.Get(HubUserHeader), "%s %s: the hub user goes with the key", tc.method, tc.path)
		} else {
			assert.Empty(t, out.Get(IdempotencyKeyHeader), "%s %s", tc.method, tc.path)
		}
		assert.Equal(t, "1", out.Get(ViaHeader))
	}
	// Two spellings of the header stay two values, so the server's "exactly one" check sees them.
	two := http.Header{"idempotency-key": {"0123456789abcdef-a"}, "Idempotency-Key": {"0123456789abcdef-b"}}
	assert.Len(t, scrub(two, http.MethodPost, "/api/sessions/launch")[IdempotencyKeyHeader], 2)
}
