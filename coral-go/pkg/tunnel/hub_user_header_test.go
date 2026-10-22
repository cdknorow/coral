package tunnel

import (
	"net/http"
	"strings"
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
		// The image upload (#6088) gets the hub account from the stream's own field (stampUploader), never from a header: scrub drops it here, as on every route that is not a board write.
		{http.MethodPost, "/api/upload", false},
		{http.MethodGet, "/api/upload/shot.png", false},
		{http.MethodDelete, "/api/upload/shot.png", false},
		{http.MethodPost, "/api/upload/shot.png", false},
		{http.MethodPut, "/api/upload", false},
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

// #3489: the connector forwards the Coral Hub server header (a relayed cross-team message's
// source Coral) ONLY on POST /api/board/{project}/external-messages, and drops it on every
// other route and method, in any spelling.
func TestScrubForwardsTheHubServerHeaderOnlyOnExternalMessages(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		kept         bool
	}{
		{http.MethodPost, "/api/board/team/external-messages", true},
		{http.MethodPost, "/api/board/a b/external-messages", true},
		// A relayed file (cross-team artifacts): the same rule on its own route, and no other.
		{http.MethodPost, "/api/board/team/external-artifacts", true},
		{http.MethodPost, "/api/board/a b/external-artifacts", true},
		{http.MethodGet, "/api/board/team/external-artifacts", false},
		{http.MethodPut, "/api/board/team/external-artifacts", false},
		{http.MethodPost, "/api/board/team/external-artifacts/x", false},
		{http.MethodPost, "/api/board/team/external-artifact", false},
		{http.MethodPost, "/api/board/team/artifacts", false},
		{http.MethodGet, "/api/board/team/external-messages", false},
		{http.MethodPut, "/api/board/team/external-messages", false},
		{http.MethodPost, "/api/board/team/messages", false},
		{http.MethodPost, "/api/board/team/external-messages/x", false},
		{http.MethodPost, "/api/board/team/tasks", false},
		{http.MethodPut, "/api/board/team/settings", false},
		{http.MethodPost, "/api/sessions/live/x/send", false},
	} {
		for _, key := range []string{HubServerHeader, "x-coral-hub-server", "X-CORAL-HUB-SERVER"} {
			in := http.Header{}
			in[key] = []string{"0b6c6a52-8a0e-4f4e-9d61-3f1e2a1b9c00"}
			out := scrub(in, tc.method, tc.path)
			if tc.kept {
				assert.Equal(t, "0b6c6a52-8a0e-4f4e-9d61-3f1e2a1b9c00", out.Get(HubServerHeader), "%s %s (%s)", tc.method, tc.path, key)
			} else {
				assert.Empty(t, out.Values(HubServerHeader), "%s %s (%s)", tc.method, tc.path, key)
				for k := range out {
					assert.NotEqual(t, "x-coral-hub-server", strings.ToLower(k), "%s %s: no spelling survives", tc.method, tc.path)
				}
			}
		}
	}
}

// #3489: Coral Hub's notice header is forwarded ONLY on POST /api/board/{project}/hub-notice.
func TestScrubForwardsTheHubNoticeHeaderOnlyOnHubNotice(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		kept         bool
	}{
		{http.MethodPost, "/api/board/team/hub-notice", true},
		{http.MethodGet, "/api/board/team/hub-notice", false},
		{http.MethodPost, "/api/board/team/messages", false},
		{http.MethodPost, "/api/board/team/external-messages", false},
		{http.MethodPost, "/api/board/team/hub-notice/x", false},
	} {
		for _, key := range []string{HubNoticeHeader, "x-coral-hub-notice"} {
			in := http.Header{}
			in[key] = []string{"1"}
			out := scrub(in, tc.method, tc.path)
			if tc.kept {
				assert.Equal(t, "1", out.Get(HubNoticeHeader), "%s %s", tc.method, tc.path)
			} else {
				assert.Empty(t, out.Values(HubNoticeHeader), "%s %s", tc.method, tc.path)
			}
		}
	}
}
