package hubproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3990: a hub names its own static build in X-Coral-Build on every /api/ answer (the server's buildHeader middleware, which runs
// before the proxy), and a dashboard tab compares it with the build it loaded from. A proxied answer used to carry the REMOTE's
// header as well (the reverse proxy appends it), so the browser read "hub, remote" and showed "Coral was updated" on every load,
// even when both were the same build. The remote's build is never the hub page's build: only the hub's own header may reach the browser.
func TestRemoteBuildHeaderNeverReachesTheBrowser(t *testing.T) {
	for _, same := range []bool{false, true} {
		remoteBuild := "remotebuild00"
		if same {
			remoteBuild = "hubbuild00000"
		}
		rem := httptest.NewServer(keyed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Coral-Build", remoteBuild)
			io.WriteString(w, `{"ok":true}`)
		})))
		t.Cleanup(rem.Close)
		res := &fakeResolver{targets: map[string]HTTPTarget{"a": {URL: rem.URL, APIKey: remoteKey, AllowPrivate: true}}, errs: map[string]error{}}
		withBuild := func(next http.Handler) http.Handler { // what internal/server's buildHeader does for the hub's own answers
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Coral-Build", "hubbuild00000")
				next.ServeHTTP(w, r)
			})
		}
		hub := httptest.NewServer(withBuild(newHub(res)))
		t.Cleanup(hub.Close)

		resp, err := http.Get(hub.URL + "/api/remote/a/api/system/status")
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, []string{"hubbuild00000"}, resp.Header.Values("X-Coral-Build"), "the hub's own build is the only one the browser sees (remote same build: %v)", same)
	}
}
