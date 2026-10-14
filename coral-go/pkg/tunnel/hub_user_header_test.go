package tunnel

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3377, #3404: the connector forwards the Coral Hub user header ONLY on a board write,
// /api/board/{project}/... other than GET or HEAD (chat posts, and the Operator's task
// actions and cross-team posts through Coral Hub), and drops it on every other route
// and method; other hub headers are dropped as before.
func TestScrubForwardsTheHubUserHeaderOnlyOnBoardWrites(t *testing.T) {
	in := http.Header{}
	in.Set(HubUserHeader, "hub-user-1234abcd")
	in.Set("X-Coral-Hub-Admin", "1")
	for _, tc := range []struct {
		method, path string
		kept         bool
	}{
		{http.MethodPost, "/api/board/team/messages", true},
		{http.MethodPost, "/api/board/a b/messages", true},
		{http.MethodPost, "/api/board/team/tasks", true},
		{http.MethodPatch, "/api/board/team/tasks/7", true},
		{http.MethodPatch, "/api/board/team/tasks/7/amend", true},
		{http.MethodPost, "/api/board/team/tasks/7/reassign", true},
		{http.MethodPost, "/api/board/team/tasks/7/complete", true},
		{http.MethodPost, "/api/board/team/external-messages", true},
		{http.MethodDelete, "/api/board/team/waits", true},
		{http.MethodGet, "/api/board/team/messages", false},
		{http.MethodHead, "/api/board/team/tasks", false},
		{http.MethodPost, "/api/board/messages", false},
		{http.MethodDelete, "/api/board/team", false},
		{http.MethodPost, "/api/boards/team/tasks", false},
		{http.MethodPut, "/api/settings", false},
		{http.MethodPost, "/api/sessions/live/x/send", false},
		{http.MethodGet, "/ws/terminal/x", false},
	} {
		out := scrub(in, tc.method, tc.path)
		if tc.kept {
			assert.Equal(t, "hub-user-1234abcd", out.Get(HubUserHeader), "%s %s", tc.method, tc.path)
		} else {
			assert.Empty(t, out.Get(HubUserHeader), "%s %s", tc.method, tc.path)
		}
		assert.Empty(t, out.Get("X-Coral-Hub-Admin"))
		assert.Equal(t, "1", out.Get(ViaHeader))
	}
}
