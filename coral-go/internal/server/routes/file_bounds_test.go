package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/pkg/tunnel"
)

type boundsEnv struct {
	t       *testing.T
	srv     *httptest.Server
	base    string // temp dir holding the repo and the "outside" files
	repo    string
	outside string
	home    string
}

func newBoundsEnv(t *testing.T) *boundsEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("CORAL_MAX_FILE_BYTES", "4096")
	base := realPath(t.TempDir())
	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(home, "projects"), 0o755))
	t.Setenv("HOME", home)
	t.Setenv("CORAL_PROJECT_ROOTS", "")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "app"), 0o755))
	require.NoError(t, exec.Command("git", "init", "-q", repo).Run())
	write := func(p, s string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(s), 0o644))
	}
	write(filepath.Join(repo, "app", "main.go"), "package main")
	write(filepath.Join(repo, "README.md"), "hi")
	write(filepath.Join(repo, ".env"), "TOKEN=abc")
	write(filepath.Join(repo, "certs", "server.pem"), "-----BEGIN-----")
	write(filepath.Join(repo, "big.txt"), strings.Repeat("x", 5000))
	write(filepath.Join(base, "outside.txt"), "outside-secret")
	write(filepath.Join(home, ".ssh", "id_rsa"), "PRIVATE")
	write(filepath.Join(base, "outdir", "inner.txt"), "inner")
	require.NoError(t, os.Symlink(filepath.Join(base, "outside.txt"), filepath.Join(repo, "app", "linkfile")))
	require.NoError(t, os.Symlink(filepath.Join(base, "outdir"), filepath.Join(repo, "app", "linkdir")))
	require.NoError(t, os.Symlink(base, filepath.Join(home, "projects", "escape")))

	server, handler, _, ss := setupSessionsTestServer(t)
	ss.RegisterLiveSession(context.Background(), &store.LiveSession{AgentName: "claude-fb", AgentType: "claude", WorkingDir: filepath.Join(repo, "app"), SessionID: "fb-1"})

	r := chi.NewRouter()
	r.Get("/api/sessions/live/{name}/file-content", handler.GetFileContent)
	r.Put("/api/sessions/live/{name}/file-content", handler.SaveFileContent)
	r.Get("/api/sessions/live/{name}/resolve-path", handler.ResolvePath)
	r.Get("/api/sessions/live/{name}/search-files", handler.SearchFiles)
	sys := &SystemHandler{}
	r.Get("/api/filesystem/list", sys.ListFilesystem)
	r.Get("/api/filesystem/is-git", sys.IsGitDir)
	_ = server
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &boundsEnv{t: t, srv: srv, base: base, repo: repo, outside: filepath.Join(base, "outside.txt"), home: home}
}

// caller kinds: what the server sees. Only "dashboard" is the relaxed one.
const (
	dashboard = false // a same-origin browser fetch from the local dashboard
	tunneled  = true  // executed by the connector
)

func (e *boundsEnv) do(method, path string, q url.Values, body string, viaTunnel bool) (int, string) {
	e.t.Helper()
	if viaTunnel {
		return e.doAs(method, path, q, body, kindTunnel)
	}
	return e.doAs(method, path, q, body, kindDashboard)
}

type kind int

const (
	kindTunnel      kind = iota // connector: stamp
	kindDashboard               // browser, same-origin fetch, loopback
	kindCurl                    // loopback, no fetch metadata
	kindCrossSite               // browser on another site
	kindBearer                  // a proxy with a Bearer key (self-hosted hub)
	kindOriginOnly              // engine without Sec-Fetch-*: Origin is this origin
	kindForgedFetch             // stamp plus a forged Sec-Fetch-Site
)

func (e *boundsEnv) doAs(method, path string, q url.Values, body string, k kind) (int, string) {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path+"?"+q.Encode(), strings.NewReader(body))
	switch k {
	case kindTunnel:
		req.Header.Set(tunnel.ViaHeader, "1")
	case kindDashboard:
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	case kindCrossSite:
		req.Header.Set("Sec-Fetch-Site", "cross-site")
	case kindBearer:
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Authorization", "Bearer spoke-key")
	case kindOriginOnly:
		req.Header.Set("Origin", e.srv.URL)
	case kindForgedFetch:
		req.Header.Set(tunnel.ViaHeader, "1")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

func (e *boundsEnv) get(path string, viaTunnel bool, kv ...string) (int, string) {
	q := url.Values{"session_id": {"fb-1"}}
	for i := 0; i < len(kv); i += 2 {
		q.Set(kv[i], kv[i+1])
	}
	return e.do("GET", path, q, "", viaTunnel)
}

const fc = "/api/sessions/live/claude-fb/file-content"

func TestFileContentTunnelBounds(t *testing.T) {
	e := newBoundsEnv(t)

	// Plain reads inside the repository work for both callers.
	for _, via := range []bool{false, true} {
		code, body := e.get(fc, via, "filepath", "app/main.go")
		assert.Equal(t, 200, code, via)
		assert.Contains(t, body, "package main")
	}

	// Through the tunnel nothing outside the repo is readable, and every refusal looks
	// the same as a missing file (no existence oracle).
	_, missing := e.get(fc, true, "filepath", "no/such/file")
	blocked := []string{
		e.outside,                                // absolute, outside
		"/etc/passwd",                            // absolute system file
		filepath.Join(e.home, ".ssh", "id_rsa"),  // credential file
		"../outside.txt",                         // traversal
		"app/../../outside.txt",                  // traversal
		"app/linkfile",                           // symlink to an outside file
		"app/linkdir/inner.txt",                  // through a symlinked directory
		filepath.Join(e.repo, "app", "linkfile"), // absolute path to the symlink
		".env", "certs/server.pem",               // credential files inside the repo
		filepath.Join(e.repo, ".env"),
	}
	for _, fp := range blocked {
		code, body := e.get(fc, true, "filepath", fp)
		assert.Contains(t, []int{403, 404}, code, fp)
		assert.NotContains(t, body, "outside-secret", fp)
		assert.NotContains(t, body, "PRIVATE", fp)
		assert.NotContains(t, body, "TOKEN=abc", fp)
		if strings.HasPrefix(fp, "/") || strings.Contains(fp, "env") || strings.Contains(fp, "pem") {
			assert.Equal(t, 404, code, fp)
			assert.Equal(t, missing, body, "same body as a missing file: "+fp)
		}
	}

	// An absolute path inside the repository is fine through the tunnel.
	code, body := e.get(fc, true, "filepath", filepath.Join(e.repo, "app", "main.go"))
	assert.Equal(t, 200, code)
	assert.Contains(t, body, "package main")

	// The local dashboard keeps its convenience: absolute paths elsewhere still open...
	code, body = e.get(fc, false, "filepath", e.outside)
	assert.Equal(t, 200, code)
	assert.Contains(t, body, "outside-secret")
	// ...but symlinks out of the workdir stay blocked for everyone.
	code, _ = e.get(fc, false, "filepath", "app/linkfile")
	assert.Equal(t, 403, code)
	code, _ = e.get(fc, false, "filepath", "app/linkdir/inner.txt")
	assert.Equal(t, 403, code)

	// Size cap for both.
	for _, via := range []bool{false, true} {
		code, body = e.get(fc, via, "filepath", "big.txt")
		assert.Equal(t, 413, code)
		assert.NotContains(t, body, "xxxx")
	}
	// Errors never carry absolute paths.
	_, body = e.get(fc, true, "filepath", e.outside)
	assert.NotContains(t, body, e.base)
}

func TestResolvePathTunnelBounds(t *testing.T) {
	e := newBoundsEnv(t)
	rp := "/api/sessions/live/claude-fb/resolve-path"

	code, _ := e.get(rp, false, "filepath", e.outside)
	assert.Equal(t, 200, code, "local dashboard still resolves an absolute path an agent printed")

	_, none := e.get(rp, true, "filepath", "definitely/missing.txt")
	for _, fp := range []string{e.outside, "/etc/passwd", filepath.Join(e.home, ".ssh", "id_rsa"), "~/.ssh/id_rsa"} {
		code, body := e.get(rp, true, "filepath", fp)
		assert.Equal(t, 404, code, fp)
		assert.Equal(t, none, body, "no existence oracle: "+fp)
	}
	code, body := e.get(rp, true, "filepath", ".env")
	assert.Equal(t, 404, code)
	assert.Equal(t, none, body)
	code, body = e.get(rp, true, "filepath", "app/main.go")
	assert.Equal(t, 200, code)
	assert.Contains(t, body, "app/main.go")
}

func TestSearchFilesDirDoesNotFollowSymlinksOut(t *testing.T) {
	e := newBoundsEnv(t)
	sf := "/api/sessions/live/claude-fb/search-files"
	for _, via := range []bool{false, true} {
		for _, dir := range []string{"app/linkdir", "linkdir", "app/linkdir/"} {
			code, body := e.get(sf, via, "dir", dir)
			assert.Equal(t, 200, code)
			assert.NotContains(t, body, "inner.txt", dir)
		}
		code, body := e.get(sf, via, "dir", "app")
		assert.Equal(t, 200, code)
		assert.Contains(t, body, "main.go")
		_ = code
	}
}

func TestFilesystemBrowserTunnelBounds(t *testing.T) {
	e := newBoundsEnv(t)
	list := "/api/filesystem/list"
	for _, p := range []string{"/", "/etc", e.base, filepath.Join(e.home, ".ssh"), "~/.ssh", "~/projects/escape", filepath.Join(e.home, "..")} {
		code, body := e.do("GET", list, url.Values{"path": {p}}, "", true)
		assert.Equal(t, 403, code, p)
		assert.NotContains(t, body, "repo", p)
	}
	code, body := e.do("GET", list, url.Values{"path": {"~"}}, "", true)
	assert.Equal(t, 200, code)
	assert.Contains(t, body, "projects")
	assert.NotContains(t, body, ".ssh")

	// Local use is unchanged.
	code, _ = e.do("GET", list, url.Values{"path": {"/"}}, "", false)
	assert.Equal(t, 200, code)

	// is-git does not confirm repositories outside the allowed roots.
	var r struct {
		IsGit bool `json:"is_git"`
	}
	_, body = e.do("GET", "/api/filesystem/is-git", url.Values{"path": {e.repo}}, "", true)
	require.NoError(t, json.Unmarshal([]byte(body), &r))
	assert.False(t, r.IsGit)
	_, body = e.do("GET", "/api/filesystem/is-git", url.Values{"path": {e.repo}}, "", false)
	require.NoError(t, json.Unmarshal([]byte(body), &r))
	assert.True(t, r.IsGit)
}

func TestSaveFileContentTunnelRefusesGitAndCredentials(t *testing.T) {
	e := newBoundsEnv(t)
	put := func(fp string, via bool) int {
		code, _ := e.do("PUT", fc, url.Values{"filepath": {fp}, "session_id": {"fb-1"}}, `{"content":"x"}`, via)
		return code
	}
	assert.Equal(t, 200, put("notes.txt", true))
	assert.Equal(t, 403, put(".git/hooks/pre-commit", true))
	assert.Equal(t, 403, put("sub/.git/config", true))
	assert.Equal(t, 403, put(".env", true))
	assert.Equal(t, 403, put("../outside.txt", true))
	_, err := os.Stat(filepath.Join(e.repo, ".git", "hooks", "pre-commit"))
	assert.Error(t, err)
}

func TestSensitivePathAndWithin(t *testing.T) {
	for _, p := range []string{"/home/u/.ssh/id_rsa", "/home/u/.aws/credentials", "/home/u/.coral/api_key", "/r/.env", "/r/.env.local", "/r/x/server.pem",
		"/r/deploy.key", "/Users/u/Library/Keychains/login.keychain-db", "/r/id_ed25519", "/r/.netrc", "/r/terraform.tfstate", "/home/u/.config/gh/hosts.yml"} {
		assert.True(t, sensitivePath(p), p)
	}
	for _, p := range []string{"/r/app/main.go", "/r/README.md", "/r/environment.md", "/r/keyboard.txt", "/r/src/envelope.go"} {
		assert.False(t, sensitivePath(p), p)
	}
	assert.True(t, within("/a/b", "/a/b"))
	assert.True(t, within("/a/b", "/a/b/c"))
	assert.False(t, within("/a/b", "/a/bc"))
	assert.False(t, within("/a/b", "/a"))
	assert.False(t, within("", "/a"))
	assert.False(t, within("/a", ""))
}

// The relaxed mode (absolute paths outside the repository) needs every condition of a
// positively identified local dashboard; anything else, and any doubt, is strict.
func TestStrictIsTheDefaultAndOnlyTheLocalDashboardRelaxes(t *testing.T) {
	e := newBoundsEnv(t)
	q := url.Values{"filepath": {e.outside}, "session_id": {"fb-1"}}
	cases := []struct {
		name string
		k    kind
		open bool
	}{
		{"same-origin browser fetch (dashboard, <img raw=1>)", kindDashboard, true},
		{"engine without fetch metadata but own Origin", kindOriginOnly, true},
		{"tunnel stamp", kindTunnel, false},
		{"stamp plus a forged Sec-Fetch-Site", kindForgedFetch, false},
		{"loopback curl, no Sec-Fetch", kindCurl, false},
		{"cross-site browser request", kindCrossSite, false},
		{"Bearer caller (self-hosted hub proxy)", kindBearer, false},
	}
	for _, c := range cases {
		code, body := e.doAs("GET", fc, q, "", c.k)
		if c.open {
			assert.Equal(t, 200, code, c.name)
			assert.Contains(t, body, "outside-secret", c.name)
		} else {
			assert.Equal(t, 404, code, c.name)
			assert.NotContains(t, body, "outside-secret", c.name)
		}
	}
	// raw=1 image previews take the same decision.
	img := filepath.Join(e.base, "pic.png")
	require.NoError(t, os.WriteFile(img, []byte("\x89PNG"), 0o644))
	qi := url.Values{"filepath": {img}, "session_id": {"fb-1"}, "raw": {"1"}}
	code, _ := e.doAs("GET", fc, qi, "", kindDashboard)
	assert.Equal(t, 200, code)
	code, _ = e.doAs("GET", fc, qi, "", kindCurl)
	assert.Equal(t, 404, code)

	// The credential denylist holds even for the dashboard.
	for _, fp := range []string{filepath.Join(e.home, ".ssh", "id_rsa"), filepath.Join(e.repo, ".env")} {
		code, _ := e.doAs("GET", fc, url.Values{"filepath": {fp}, "session_id": {"fb-1"}}, "", kindDashboard)
		assert.Equal(t, 404, code, fp)
	}
}

func TestLocalDashboardNeedsLocalHostAndPeer(t *testing.T) {
	mk := func(remote, host string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "http://"+host+"/x", nil)
		r.RemoteAddr = remote
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	assert.True(t, localDashboard(mk("127.0.0.1:5555", "localhost:8420", same)))
	assert.True(t, localDashboard(mk("[::1]:5555", "[::1]:8420", same)))
	assert.False(t, localDashboard(mk("203.0.113.9:5555", "localhost:8420", same)), "remote peer")
	assert.False(t, localDashboard(mk("127.0.0.1:5555", "evil.example", same)), "DNS rebinding host")
	assert.False(t, localDashboard(mk("127.0.0.1:5555", "localhost:8420", map[string]string{"Sec-Fetch-Site": "none"})))
	assert.False(t, localDashboard(mk("127.0.0.1:5555", "localhost:8420", map[string]string{"Origin": "http://evil.example"})))
	assert.True(t, localDashboard(mk("127.0.0.1:5555", "localhost:8420", map[string]string{"Origin": "http://localhost:8420"})))
	assert.True(t, localDashboard(mk("127.0.0.1:5555", "localhost:8420", map[string]string{"Referer": "http://localhost:8420/dash"})))
	assert.False(t, localDashboard(mk("127.0.0.1:5555", "localhost:8420", nil)), "no browser evidence at all")
	assert.False(t, localDashboard(mk("bad-addr", "localhost:8420", same)), "unparseable peer fails closed")
}
