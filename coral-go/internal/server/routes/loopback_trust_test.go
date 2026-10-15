package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/stretchr/testify/assert"
)

// The four route-level "is this a person at this computer" checks go through auth.IsDirectLocal
// (#3585): a request a local proxy forwarded, and every request while require_auth_on_loopback is
// on, is not local, so a proxied client gets the strict file roots and no local-only powers.
func TestRouteLocalChecksDenyProxiedAndSettingOn(t *testing.T) {
	t.Cleanup(func() { auth.SetRequireAuthOnLoopback(false) })

	// localDashboard wants a same-origin browser fetch; localCLI wants none of those headers. Give
	// each its own happy-path request so the only variable under test is the loopback/proxy gate.
	dashboard := func(mod func(*http.Request)) *http.Request {
		r := httptest.NewRequest("GET", "/api/x", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Host = "127.0.0.1:8420"
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if mod != nil {
			mod(r)
		}
		return r
	}
	cli := func(mod func(*http.Request)) *http.Request {
		r := httptest.NewRequest("GET", "/api/x", nil)
		r.RemoteAddr = "127.0.0.1:5000"
		r.Host = "127.0.0.1:8420"
		if mod != nil {
			mod(r)
		}
		return r
	}

	auth.SetRequireAuthOnLoopback(false)
	// Plain direct loopback: all four say local.
	assert.True(t, localDashboard(dashboard(nil)), "localDashboard plain loopback")
	assert.True(t, localCLI(cli(nil)), "localCLI plain loopback")
	assert.True(t, isLocalClient(cli(nil)), "isLocalClient plain loopback")
	assert.True(t, fromLoopback(cli(nil)), "fromLoopback plain loopback")

	// A proxy header present: none say local (localCLI included: a proxy can't be the command line).
	proxy := func(r *http.Request) { r.Header.Set("X-Forwarded-For", "100.64.0.9") }
	assert.False(t, localDashboard(dashboard(proxy)), "localDashboard proxied")
	assert.False(t, localCLI(cli(proxy)), "localCLI proxied")
	assert.False(t, isLocalClient(cli(proxy)), "isLocalClient proxied")
	assert.False(t, fromLoopback(cli(proxy)), "fromLoopback proxied")

	// require_auth_on_loopback on: the setting-gated powers go non-local, but localCLI stays
	// local for a direct peer — the signed `coral cloud connect` handshake's signature is the
	// credential, so the setting is already satisfied (a proxied CLI request is still refused).
	auth.SetRequireAuthOnLoopback(true)
	assert.False(t, localDashboard(dashboard(nil)), "localDashboard with the setting on")
	assert.True(t, localCLI(cli(nil)), "localCLI stays local for a direct peer with the setting on")
	assert.False(t, localCLI(cli(proxy)), "localCLI still denies a proxied request with the setting on")
	assert.False(t, isLocalClient(cli(nil)), "isLocalClient with the setting on")
	assert.False(t, fromLoopback(cli(nil)), "fromLoopback with the setting on")
}
