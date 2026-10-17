package hubproxy

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/pkg/tunnel"
)

// #4297: a remote reached directly gets the connector's request-header allowlist and its response
// filter. What the browser sends to the hub is never relayed (X-API-Key, X-Coral-*, Sec-Fetch-*,
// the Hub user), only the remote's own key; and a reply under /api/remote/<id>/api/... can't run a
// page on the hub's origin.
func TestDirectTargetGetsTheHeaderAllowlist(t *testing.T) {
	var mu sync.Mutex
	var seen http.Header
	_, hub, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}), nil)

	req, _ := http.NewRequest("POST", hub.URL+"/api/remote/a/api/board/team/messages", strings.NewReader(`{"content":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-API-Key", "browser-hub-key")
	req.Header.Set("X-Coral-Session-Token", "agent-token")
	req.Header.Set("X-Coral-Anything", "1")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("X-Forwarded-Prefix", "/x")
	req.Header[tunnel.HubUserHeader] = []string{"hub-user-1234abcd"}
	req.Header["x-coral-hub-user"] = []string{"hub-user-1234abcd"} // not canonical, as written
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "Bearer "+remoteKey, seen.Get("Authorization"), "the remote's own key")
	assert.Equal(t, "application/json", seen.Get("Accept"))
	assert.Equal(t, "application/json", seen.Get("Content-Type"))
	assert.Equal(t, "gzip", seen.Get("Accept-Encoding"), "relayed gzip still works")
	for k := range seen {
		c := http.CanonicalHeaderKey(k)
		assert.False(t, c == "X-Api-Key" || strings.HasPrefix(c, "X-Coral-") || strings.HasPrefix(c, "Sec-Fetch-") || c == "X-Forwarded-Prefix",
			"%s reached the remote", k)
	}
}

func TestDirectTargetResponsesAreSandboxed(t *testing.T) {
	const evil = `<script>fetch('/api/system/api-key')</script>`
	_, hub, _ := setup(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/evil-page":
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Set-Cookie", "evil=1")
			w.Header().Set("Location", "https://evil.example/x")
			w.Header().Set("Cache-Control", "public, max-age=31536000")
			io.WriteString(w, evil)
		case "/api/evil-csp":
			// Its own policy is not the bare sandbox: it allows scripts on a same-origin document.
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-same-origin")
			io.WriteString(w, evil)
		case "/api/evil-first-wins":
			// The first sandbox directive is the one a browser reads: this one allows scripts on the
			// hub's own origin, and the bare one after it does not undo it.
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-same-origin; sandbox")
			io.WriteString(w, evil)
		case "/api/evil-two-lines":
			w.Header().Set("Content-Type", "text/html")
			w.Header().Add("Content-Security-Policy", "sandbox")
			w.Header().Add("Content-Security-Policy", "default-src 'none'")
			io.WriteString(w, evil)
		case "/api/evil-comma":
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Security-Policy", "sandbox, script-src 'unsafe-inline'")
			io.WriteString(w, evil)
		case "/api/preview":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Disposition", `inline; filename="page.html"`)
			w.Header().Set("Content-Security-Policy", PreviewCSP)
			io.WriteString(w, "<style>p{}</style><p>hi</p>")
		case "/api/file-preview":
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
			io.WriteString(w, "<p>hi</p>")
		case "/api/json":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"ok":true}`)
		}
	}), nil)
	get := func(path string) *http.Response {
		resp, err := http.Get(hub.URL + "/api/remote/a" + path)
		require.NoError(t, err)
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	for _, path := range []string{"/api/evil-page", "/api/evil-csp", "/api/evil-first-wins", "/api/evil-two-lines", "/api/evil-comma"} {
		resp := get(path)
		assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"), path)
		assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment"), path)
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"), path)
		assert.Equal(t, SandboxCSP, resp.Header.Get("Content-Security-Policy"), path)
		assert.Empty(t, resp.Header.Get("Set-Cookie"), path)
		assert.Empty(t, resp.Header.Get("Location"), path)
		assert.Equal(t, "private, no-store", resp.Header.Get("Cache-Control"), path)
	}

	// Coral's own sandboxed previews still work: type, policy and disposition kind survive.
	resp := get("/api/preview")
	assert.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Equal(t, PreviewCSP, resp.Header.Get("Content-Security-Policy"))
	assert.True(t, strings.HasPrefix(resp.Header.Get("Content-Disposition"), "inline"), resp.Header.Get("Content-Disposition"))
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "<p>hi</p>")
	resp = get("/api/file-preview")
	assert.Equal(t, "text/html", resp.Header.Get("Content-Type"))
	assert.Equal(t, "default-src 'none'; style-src 'unsafe-inline'; sandbox", resp.Header.Get("Content-Security-Policy"))

	resp = get("/api/json")
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
}

// plainSandbox accepts one header line with exactly one sandbox directive, and it bare.
func TestPlainSandbox(t *testing.T) {
	for _, tc := range []struct {
		values []string
		ok     bool
	}{
		{[]string{"sandbox"}, true},
		{[]string{"SANDBOX"}, true},
		{[]string{PreviewCSP}, true},
		{[]string{"default-src 'none'; style-src 'unsafe-inline'; sandbox"}, true},
		{[]string{"sandbox;"}, true},
		{[]string{"sandbox allow-scripts allow-same-origin; sandbox"}, false}, // the first wins
		{[]string{"sandbox; sandbox allow-scripts"}, false},
		{[]string{"sandbox; sandbox"}, false}, // two directives: the second is ignored, but it is not a shape Coral sends
		{[]string{"sandbox allow-scripts"}, false},
		{[]string{"sandbox, default-src 'none'"}, false},
		{[]string{"default-src 'none', sandbox"}, false},
		{[]string{"sandbox", "default-src 'none'"}, false}, // two header lines
		{[]string{"sandbox", "sandbox"}, false},
		{[]string{"default-src 'none'"}, false},
		{[]string{"sandboxed"}, false},
		{[]string{""}, false},
		{nil, false},
	} {
		assert.Equal(t, tc.ok, plainSandbox(tc.values), "%q", tc.values)
	}
}
