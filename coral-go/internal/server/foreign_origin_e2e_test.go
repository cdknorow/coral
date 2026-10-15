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
// can't change Coral's settings or post on the board as the Operator through the browser (a "simple" text/plain PUT with
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
		send := func(method, path, body, origin, site, contentType string) (int, string) {
			req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
			req.Header.Set("Content-Type", contentType)
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			if site != "" {
				req.Header.Set("Sec-Fetch-Site", site)
			}
			resp, err := browser.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}
		writes := []struct{ method, path, body string }{
			{http.MethodPut, "/api/settings", `{}`},
			{http.MethodPost, "/api/board/team/messages", `{"subscriber_id":"Operator","as":"Operator","content":"hello"}`},
		}
		for _, wr := range writes {
			for _, f := range []struct{ origin, site string }{{foreign, "same-site"}, {"https://evil.example", "cross-site"}, {"https://evil.example", ""}} {
				code, body := send(wr.method, wr.path, wr.body, f.origin, f.site, "text/plain")
				assert.Equal(t, http.StatusForbidden, code, "strict=%v: %s %s from %s: %s", strict, wr.method, wr.path, f.origin, body)
				assert.Contains(t, body, "foreign_origin")
			}
			code, body := send(wr.method, wr.path, wr.body, ts.URL, "same-origin", "application/json")
			assert.Equal(t, http.StatusOK, code, "strict=%v: %s %s from Coral's own page: %s", strict, wr.method, wr.path, body)
		}
		// No browser evidence (curl, the CLI, hooks) is left to the identity rules: in
		// default mode that's the localhost allowance; strict mode needs the CLI's proof.
		if !strict {
			for _, wr := range writes {
				code, body := send(wr.method, wr.path, wr.body, "", "", "application/json")
				assert.Equal(t, http.StatusOK, code, "default mode: %s %s with no Origin: %s", wr.method, wr.path, body)
			}
		} else {
			tok, err := cliauth.ReadToken(dir)
			require.NoError(t, err)
			resp, err := cliauth.CLIClient{Base: ts.URL, Token: tok}.Do(context.Background(), http.MethodPut, "/api/settings", []byte("{}"), nil, 0)
			require.NoError(t, err)
			resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode, "strict mode: the CLI's write with no Origin")
		}

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
