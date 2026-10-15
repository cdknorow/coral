package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cliauth"
)

// #3511 end to end, the real router in both modes: a page on another localhost port
// can't change Coral's settings through the browser (a "simple" text/plain PUT with
// the dashboard's sign-in cookie in strict mode, or with nothing in default mode), and
// can't open a WebSocket to it; Coral's own page can.
func TestForeignPageCannotWriteOrOpenSockets(t *testing.T) {
	for _, strict := range []bool{false, true} {
		_, ts, dir := strictServer(t, strict)
		jar, _ := cookiejar.New(nil)
		browser := &http.Client{Jar: jar}
		if strict { // sign the browser in, as 'coral open' does
			tok, err := cliauth.ReadToken(dir)
			require.NoError(t, err)
			resp, err := cliauth.CLIClient{Base: ts.URL, Token: tok}.Do(context.Background(), http.MethodPost, "/api/system/login-token", []byte("{}"), nil, 0)
			require.NoError(t, err)
			var link struct {
				Path string `json:"path"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&link))
			resp.Body.Close()
			r, err := browser.Get(ts.URL + link.Path)
			require.NoError(t, err)
			r.Body.Close()
		}
		own, _ := url.Parse(ts.URL)
		_, port := own.Hostname(), own.Port()
		foreign := "http://localhost:3000"
		if port == "3000" {
			foreign = "http://localhost:3001"
		}
		put := func(origin, site, contentType string) (int, string) {
			req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/settings", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", contentType)
			req.Header.Set("Origin", origin)
			req.Header.Set("Sec-Fetch-Site", site)
			resp, err := browser.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}
		code, body := put(foreign, "same-site", "text/plain")
		assert.Equal(t, http.StatusForbidden, code, "strict=%v: a foreign port's write: %s", strict, body)
		assert.Contains(t, body, "foreign_origin")
		code, body = put(ts.URL, "same-origin", "application/json")
		assert.Equal(t, http.StatusOK, code, "strict=%v: Coral's own page: %s", strict, body)

		// A WebSocket from the foreign port is refused (the upgrade isn't completed).
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ws/coral", nil)
		req.Header = http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}, "Sec-Websocket-Version": {"13"},
			"Sec-Websocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="}, "Origin": {foreign}}
		resp, err := browser.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.NotEqual(t, http.StatusSwitchingProtocols, resp.StatusCode, "strict=%v: a foreign port's WebSocket", strict)
	}
}
