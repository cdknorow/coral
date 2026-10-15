package server

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DNS rebinding, pinned (#3511 follow-up): a page on attacker.example whose name now points
// at 127.0.0.1 is "same origin" with itself, so the foreign-write check lets its writes by;
// the request's Host is the attacker's name, so the localhost allowance doesn't apply
// (isValidLocalhostHost) and, with no credential, the settings PUT and the board post are
// refused in default and strict mode alike, and nothing is written.
func TestDNSRebindingPageCantWrite(t *testing.T) {
	for _, strict := range []bool{false, true} {
		_, ts, _ := strictServer(t, strict)
		u, _ := url.Parse(ts.URL)
		host := "attacker.example:" + u.Port()
		send := func(method, path, body string) (int, string) {
			req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
			req.Host = host
			req.Header.Set("Content-Type", "text/plain")
			req.Header.Set("Origin", "http://"+host)
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b)
		}
		for _, wr := range []struct{ method, path, body string }{
			{http.MethodPut, "/api/settings", `{"cli_path_claude":"/tmp/x"}`},
			{http.MethodPost, "/api/board/team/messages", `{"subscriber_id":"Operator","as":"Operator","content":"rebound"}`},
		} {
			code, body := send(wr.method, wr.path, wr.body)
			assert.Equal(t, http.StatusForbidden, code, "strict=%v: %s %s from a rebound name: %s", strict, wr.method, wr.path, body)
			assert.Equal(t, "Forbidden", strings.TrimSpace(body), "auth.Middleware's DNS-rebinding refusal")
		}
		// Nothing was written: read back over Coral's own name (default mode: the localhost allowance).
		if !strict {
			resp, err := http.Get(ts.URL + "/api/settings")
			require.NoError(t, err)
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			assert.NotContains(t, string(b), "/tmp/x", "the rebound PUT changed nothing")
			resp, err = http.Get(ts.URL + "/api/board/team/messages/all")
			require.NoError(t, err)
			b, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			assert.NotContains(t, string(b), "rebound", "the rebound post isn't on the board")
		}
	}
}
