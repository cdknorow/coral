package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #3511 (Security's #3509 finding 1): a browser write from any page but Coral's own is
// refused, whatever the mode: another port of localhost (same site, so the sign-in
// cookie goes with it), any other site, a sandboxed page. Coral's own page, clients
// with no browser evidence, and the Coral Hub connector's stamped requests pass.
func TestRefuseForeignBrowserWrites(t *testing.T) {
	reached := 0
	h := RefuseForeignBrowserWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))
	do := func(method string, headers map[string]string) int {
		r := httptest.NewRequest(method, "http://localhost:8420/api/settings", strings.NewReader(`{"cli_path_claude":"/tmp/x"}`))
		r.Host = "localhost:8420"
		r.Header.Set("Content-Type", "text/plain") // a "simple" request: no preflight
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for name, c := range map[string]struct {
		method  string
		headers map[string]string
		want    int
	}{
		"another localhost port":            {"PUT", map[string]string{"Origin": "http://localhost:3000", "Sec-Fetch-Site": "same-site"}, 403},
		"127.0.0.1 on another port":         {"POST", map[string]string{"Origin": "http://127.0.0.1:5173"}, 403},
		"another site":                      {"POST", map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}, 403},
		"a sandboxed page (Origin null)":    {"DELETE", map[string]string{"Origin": "null"}, 403},
		"fetch metadata only, same-site":    {"PATCH", map[string]string{"Sec-Fetch-Site": "same-site"}, 403},
		"same origin but cross-site fetch":  {"PUT", map[string]string{"Origin": "http://localhost:8420", "Sec-Fetch-Site": "cross-site"}, 403},
		"a forged Coral Hub stamp":          {"PUT", map[string]string{"Origin": "http://localhost:3000", tunnel.ViaHeader: "1"}, 403},
		"Coral's own page":                  {"PUT", map[string]string{"Origin": "http://localhost:8420", "Sec-Fetch-Site": "same-origin"}, 200},
		"Coral's own page, no metadata":     {"POST", map[string]string{"Origin": "http://localhost:8420"}, 200},
		"no browser evidence (curl, agent)": {"PUT", nil, 200},
		"the user's own navigation":         {"POST", map[string]string{"Sec-Fetch-Site": "none"}, 200},
		"a suffix host":                     {"PUT", map[string]string{"Origin": "http://localhost:8420.evil.test"}, 403},
		"a prefix host":                     {"PUT", map[string]string{"Origin": "http://localhost.evil.test:8420"}, 403},
		"127.0.0.1 on Coral's own port":     {"PUT", map[string]string{"Origin": "http://127.0.0.1:8420", "Sec-Fetch-Site": "same-site"}, 403},
		"Coral's port, no port in Origin":   {"PUT", map[string]string{"Origin": "http://localhost"}, 403},
		"a custom scheme (app webview)":     {"PUT", map[string]string{"Origin": "coral://localhost:8420"}, 403},
		"Origin with userinfo":              {"PUT", map[string]string{"Origin": "http://x@localhost:8420"}, 403},
		"foreign Origin, no fetch metadata": {"PUT", map[string]string{"Origin": "https://evil.example"}, 403},
		"Coral's host in another case":      {"PUT", map[string]string{"Origin": "http://LOCALHOST:8420", "Sec-Fetch-Site": "same-origin"}, 200},
		"the Coral Hub connector":           {"PUT", map[string]string{"Origin": "https://app.coralai.ai", tunnel.ViaHeader: cloud.ViaSecret()}, 200},
		"a read from another port":          {"GET", map[string]string{"Origin": "http://localhost:3000", "Sec-Fetch-Site": "same-site"}, 200},
	} {
		reached = 0
		got := do(c.method, c.headers)
		assert.Equal(t, c.want, got, name)
		if c.want == 403 {
			assert.Zero(t, reached, "%s: the handler never runs", name)
		}
	}
}
