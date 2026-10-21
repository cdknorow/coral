package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/server/routes"
)

// QA #5592: `coral-agent docs` against the REAL docs handler and the REAL docs (agent_docs/), not a stand-in with two short strings: every topic, and all, come out byte for byte as the
// server holds them (the output is run through the CLI's cleaning of terminal control, which must not change a doc), the list names every doc, a path-shaped topic reads nothing, and a request
// without the session token gets nothing.

// crlfDocs copies the real docs into a temp folder with CRLF line endings, as a Windows checkout with autocrlf holds them: the handler and the command must carry such a doc through, and the
// comparison below reads line endings as one (a rule of ours for tests of checked-in docs). Running it with CRLF on every OS means the Windows-only difference is tested everywhere.
func crlfDocs(t *testing.T) string {
	t.Helper()
	src := "../../agent_docs"
	dst := t.TempDir()
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(src + "/" + e.Name())
		require.NoError(t, err)
		crlf := strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n", "\r\n")
		require.NoError(t, os.WriteFile(dst+"/"+e.Name(), []byte(crlf), 0o600))
		n++
	}
	require.Greater(t, n, 10)
	return dst
}

// lf is s with CRLF read as LF: line endings are not what these tests are about.
func lf(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func realDocsServer(t *testing.T) *httptest.Server {
	t.Helper()
	h := routes.NewAgentDocsHandler(os.DirFS(crlfDocs(t)))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Header.Get("X-Coral-Session-Token") != "test-session-token" {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"code":"dashboard_auth_required","error":"Coral is locked"}`))
				return
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Get("/api/agent-docs", h.List)
	r.Get("/api/agent-docs/all", h.GetAll)
	r.Get("/api/agent-docs/{name}", h.Get)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func serverGet(t *testing.T, srv *httptest.Server, path string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("X-Coral-Session-Token", "test-session-token")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, path)
	return b
}

func TestDocsCommandPrintsEveryRealDocExactly(t *testing.T) {
	srv := realDocsServer(t)
	var list []struct{ Name, Title string }
	require.NoError(t, json.Unmarshal(serverGet(t, srv, "/api/agent-docs"), &list))
	require.Greater(t, len(list), 10, "the real docs")

	code, out, errOut := runAgentWithToken(t, srv.URL, "docs")
	require.Equal(t, 0, code, errOut)
	for _, d := range list {
		assert.Contains(t, out, d.Name, "the list names %s", d.Name)
	}

	for _, d := range list {
		var doc struct{ Content string }
		require.NoError(t, json.Unmarshal(serverGet(t, srv, "/api/agent-docs/"+d.Name), &doc), d.Name)
		code, out, errOut := runAgentWithToken(t, srv.URL, "docs", d.Name)
		require.Equal(t, 0, code, d.Name+": "+errOut)
		assert.Equal(t, lf(doc.Content), lf(out), "%s comes out as the server holds it (line endings read as one)", d.Name)
		code, out2, _ := runAgentWithToken(t, srv.URL, "docs", d.Name+".md")
		require.Equal(t, 0, code)
		assert.Equal(t, lf(out), lf(out2), "the .md name reads the same doc")
		assert.NotContains(t, out, "\x1b", "%s: no terminal control reaches the agent's screen", d.Name)
	}

	code, all, errOut := runAgentWithToken(t, srv.URL, "docs", "all")
	require.Equal(t, 0, code, errOut)
	assert.Equal(t, lf(string(serverGet(t, srv, "/api/agent-docs/all"))), lf(all), "all comes out as the server holds it (line endings read as one)")
	assert.Greater(t, len(all), 50_000, "every doc is in it")
}

func TestDocsCommandRefusesWhatIsNotATopic(t *testing.T) {
	srv := realDocsServer(t)
	for _, bad := range []string{"../board", "..%2fboard", "/etc/passwd", "board/../README", "a b", "board;ls", "%00", strings.Repeat("a", 500), "-h", "--raw"} {
		code, out, errOut := runAgentWithToken(t, srv.URL, "docs", bad)
		if bad == "-h" || bad == "--raw" {
			continue // flags: the usage or the raw switch, covered by the command's own tests
		}
		assert.NotEqual(t, 0, code, "%q is not a topic", bad)
		assert.NotContains(t, out, "# ", "%q printed a doc", bad)
		assert.True(t, strings.Contains(errOut, "no Coral doc named") || strings.Contains(errOut, "invalid doc name"), "%q: refused in words (%s)", bad, errOut)
	}
	code, out, errOut := runAgentEnv(t, srv.URL, nil, "docs")
	assert.NotEqual(t, 0, code, "no token: refused")
	assert.NotContains(t, out, "coral-board")
	assert.Contains(t, errOut, "401", "the refusal is said")
}
