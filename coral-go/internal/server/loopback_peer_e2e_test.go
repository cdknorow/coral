package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/peercred"
)

// #95 (audit G4B, Security's conditions 2-4): the loopback pass is per OS user. The real router,
// served over real loopback connections with the main server's ConnContext, with the peer-user
// check stubbed (no second account to test with): Coral's own user keeps the pass, local powers
// included, even with strict local auth off; another user's connection, or one whose user can't be
// told, needs a credential like a request from another computer and gets no local-only power. Each
// connection is attributed once, and a forwarded request keeps the proxy rule.
func TestLoopbackPassIsOnlyForCoralsOwnUser(t *testing.T) {
	r := newE2ERemote(t, "loopback-peer", nil)
	require.False(t, r.srv.identity.Strict(), "the test server runs with strict local auth off: the pass must still be per user")

	var verdict atomic.Int32
	var calls atomic.Int32
	t.Cleanup(auth.SetPeerAttribution(func(netip.AddrPort, netip.AddrPort) (peercred.Verdict, error) {
		calls.Add(1)
		return peercred.Verdict(verdict.Load()), nil
	}))
	ts := httptest.NewUnstartedServer(r.srv.Router())
	ts.Config.ConnContext = auth.ConnContext
	ts.Start()
	t.Cleanup(ts.Close)

	// Each verdict gets its own client, so its own connections.
	get := func(v peercred.Verdict, path string, hdr map[string]string) (*http.Response, map[string]any) {
		t.Helper()
		verdict.Store(int32(v))
		client := &http.Client{Transport: &http.Transport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		defer client.CloseIdleConnections()
		req, err := http.NewRequest("GET", ts.URL+path, nil)
		require.NoError(t, err)
		for k, val := range hdr {
			req.Header.Set(k, val)
		}
		resp, err := client.Do(req)
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		return resp, body
	}

	// Coral's own user: the pass as before (strict local auth is off here).
	resp, _ := get(peercred.Self, "/api/system/status", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "Coral's own user keeps the loopback pass")
	resp, _ = get(peercred.Unsupported, "/api/system/status", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "where users can't be told apart, the rules from before")

	// Another user, or a connection whose user can't be told: refused, with the short reason.
	for _, tc := range []struct {
		v      peercred.Verdict
		detail string
	}{{peercred.OtherUser, auth.PeerOtherUser}, {peercred.Unattributed, auth.PeerUnattributed}} {
		resp, body := get(tc.v, "/api/system/status", nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, tc.detail)
		assert.Equal(t, "other_user_needs_sign_in", body["code"], tc.detail)
		assert.Equal(t, tc.detail, body["detail"], tc.detail)
		assert.NotContains(t, body["error"], "\\", "the account is never named")

		// A page load goes to the sign-in page.
		resp, _ = get(tc.v, "/", nil)
		assert.Equal(t, http.StatusTemporaryRedirect, resp.StatusCode, tc.detail)
		assert.True(t, strings.HasPrefix(resp.Header.Get("Location"), "/auth?locked=1"), tc.detail)

		// A credential still works: the API key, as from another computer.
		bearer := map[string]string{"Authorization": "Bearer " + r.srv.keyStore.Key()}
		resp, _ = get(tc.v, "/api/system/status", bearer)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "%s: the API key is a credential", tc.detail)

		// But never a local-only power: reading the API key is for Coral's own user at this computer.
		resp, _ = get(tc.v, "/api/system/api-key", bearer)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, "%s: no local-only power", tc.detail)
	}

	// A forwarded request from Coral's own user keeps the proxy rule: not direct, so key or session.
	resp, _ = get(peercred.Self, "/api/system/status", map[string]string{"X-Forwarded-For": "203.0.113.9"})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a proxied request is not local, whoever's the socket")

	// One attribution per connection: three requests on one kept-alive connection ask once.
	verdict.Store(int32(peercred.Self))
	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()
	before := calls.Load()
	for i := 0; i < 3; i++ {
		resp, err := client.Get(ts.URL + "/api/system/status")
		require.NoError(t, err)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	assert.Equal(t, int32(1), calls.Load()-before, "a connection is attributed once")
}
