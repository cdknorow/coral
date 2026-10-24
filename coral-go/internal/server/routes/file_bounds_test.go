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
	"runtime"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/gitutil"
	"github.com/subgentic/coral/internal/linktest"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/pkg/tunnel"
)

type boundsEnv struct {
	t       *testing.T
	srv     *httptest.Server
	base    string // temp dir holding the repo and the "outside" files
	repo    string
	outside string
	home    string
	ss      *store.SessionStore
}

func newBoundsEnv(t *testing.T) *boundsEnv {
	t.Helper()
	return newBoundsEnvLinks(t, true)
}

// newBoundsEnvLinks is newBoundsEnv with or without its three symbolic links: a test that never uses
// them (the Windows junction test) still runs where this account can't make symlinks (#28).
func newBoundsEnvLinks(t *testing.T, links bool) *boundsEnv {
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
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
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
	if links {
		linktest.Symlink(t, filepath.Join(base, "outside.txt"), filepath.Join(repo, "app", "linkfile"))
		linktest.Symlink(t, filepath.Join(base, "outdir"), filepath.Join(repo, "app", "linkdir"))
		linktest.Symlink(t, base, filepath.Join(home, "projects", "escape"))
	}

	server, handler, _, ss := setupSessionsTestServer(t)
	ss.RegisterLiveSession(context.Background(), &store.LiveSession{AgentName: "claude-fb", AgentType: "claude", WorkingDir: filepath.Join(repo, "app"), SessionID: "fb-1"})

	r := chi.NewRouter()
	r.Get("/api/sessions/live/{name}/file-content", handler.GetFileContent)
	r.Put("/api/sessions/live/{name}/file-content", handler.SaveFileContent)
	r.Get("/api/sessions/live/{name}/resolve-path", handler.ResolvePath)
	r.Get("/api/sessions/live/{name}/search-files", handler.SearchFiles)
	r.Get("/api/sessions/live/{name}/diff", handler.Diff)
	r.Get("/api/sessions/live/{name}/file-original", handler.GetFileOriginal)
	r.Get("/api/sessions/live/{name}/changes.diff", handler.ChangesDiff)
	r.Get("/api/sessions/{sessionID}/changes", handler.SessionChanges)
	r.Get("/api/sessions/{sessionID}/changes.diff", handler.SessionChangesArtifact)
	sys := &SystemHandler{}
	r.Get("/api/filesystem/list", sys.ListFilesystem)
	r.Get("/api/filesystem/is-git", sys.IsGitDir)
	_ = server
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &boundsEnv{t: t, srv: srv, base: base, repo: repo, outside: filepath.Join(base, "outside.txt"), home: home, ss: ss}
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

func TestHomeRootedSessionsAndHistoriesAreRefusedWhenStrict(t *testing.T) {
	e := newBoundsEnv(t)
	// A session whose "repository" is the home directory (a dotfiles repo) exposes nothing
	// but the denylist's complement to a strict caller.
	require.NoError(t, os.WriteFile(filepath.Join(e.home, ".zsh_history"), []byte("export TOKEN=1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(e.home, "notes.txt"), []byte("my notes"), 0o644))
	require.NoError(t, exec.Command("git", "init", "-q", e.home).Run())
	_, handler, _, ss := func() (*httptest.Server, *SessionsHandler, *mockSessionTerminal, *store.SessionStore) {
		return setupSessionsTestServer(t)
	}()
	ss.RegisterLiveSession(context.Background(), &store.LiveSession{AgentName: "claude-home", AgentType: "claude", WorkingDir: e.home, SessionID: "home-1"})
	r := chi.NewRouter()
	r.Get("/api/sessions/live/{name}/file-content", handler.GetFileContent)
	r.Get("/api/sessions/live/{name}/search-files", handler.SearchFiles)
	r.Get("/api/sessions/live/{name}/resolve-path", handler.ResolvePath)
	srv := httptest.NewServer(r)
	defer srv.Close()
	e.srv = srv
	for _, fp := range []string{"notes.txt", ".zsh_history", filepath.Join(e.home, "notes.txt")} {
		for _, k := range []kind{kindTunnel, kindCurl, kindBearer} {
			code, body := e.doAs("GET", "/api/sessions/live/claude-home/file-content", url.Values{"filepath": {fp}, "session_id": {"home-1"}}, "", k)
			assert.Equal(t, 404, code, "%s %d", fp, k)
			assert.NotContains(t, body, "my notes")
			assert.NotContains(t, body, "TOKEN=1")
			code, _ = e.doAs("GET", "/api/sessions/live/claude-home/resolve-path", url.Values{"filepath": {fp}, "session_id": {"home-1"}}, "", k)
			assert.Equal(t, 404, code, fp)
		}
	}
	for _, q := range []url.Values{{"dir": {"."}}, {"q": {"notes"}}, {"view": {"explorer"}, "dir": {"."}}} {
		q.Set("session_id", "home-1")
		code, body := e.doAs("GET", "/api/sessions/live/claude-home/search-files", q, "", kindTunnel)
		assert.NotContains(t, body, "notes.txt", "%d %v", code, q)
	}
	// The local dashboard still works in a home-rooted repo, apart from the denylist.
	code, body := e.doAs("GET", "/api/sessions/live/claude-home/file-content", url.Values{"filepath": {"notes.txt"}, "session_id": {"home-1"}}, "", kindDashboard)
	assert.Equal(t, 200, code)
	assert.Contains(t, body, "my notes")
	code, _ = e.doAs("GET", "/api/sessions/live/claude-home/file-content", url.Values{"filepath": {".zsh_history"}, "session_id": {"home-1"}}, "", kindDashboard)
	assert.Equal(t, 404, code)
}

func TestListingsHideCredentialFilesFromStrictCallers(t *testing.T) {
	e := newBoundsEnv(t)
	sf := "/api/sessions/live/claude-fb/search-files"
	for _, view := range []string{"", "explorer"} {
		q := url.Values{"dir": {"."}, "session_id": {"fb-1"}}
		if view != "" {
			q.Set("view", view)
		}
		_, strictBody := e.doAs("GET", sf, q, "", kindTunnel)
		assert.NotContains(t, strictBody, ".env", view)
		assert.NotContains(t, strictBody, "server.pem", view)
	}
	_, body := e.doAs("GET", sf, url.Values{"dir": {"certs"}, "session_id": {"fb-1"}}, "", kindTunnel)
	assert.NotContains(t, body, "server.pem")
	// Fuzzy search never offers credential files either.
	_, body = e.doAs("GET", sf, url.Values{"q": {"env"}, "session_id": {"fb-1"}}, "", kindTunnel)
	assert.NotContains(t, body, ".env")
}

func TestSensitivePathExtendedList(t *testing.T) {
	for _, p := range []string{"/h/.zsh_history", "/h/.bash_history", "/h/.psql_history", "/h/.local/share/keyrings/login.keyring",
		"/Users/u/Library/Application Support/Google/Chrome/Default/Cookies", "/Users/u/Library/Cookies/x", "/h/.terraform/terraform.tfstate",
		"/h/.vscode-server/data/x", "/h/.kube/config", "/h/kubeconfig", "/h/.gitconfig", "/h/.zshrc", "/h/.envrc", "/r/.git-credentials"} {
		assert.True(t, sensitivePath(p), p)
	}
	for _, p := range []string{"/r/src/history.go", "/r/docs/history.md", "/r/app/profile.go", "/r/zshrc-template/readme.md"} {
		assert.False(t, sensitivePath(p), p)
	}
}

// The agent CLIs' credentials (#3672, path audit #3669 finding 3): their files are denied
// anywhere by name, their folders in the home folder (and only there: a repository's checked-in
// .claude/ stays readable).
func TestSensitivePathAgentCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{".claude.json", ".credentials.json", "auth.json", "oauth_creds.json", ".CLAUDE.JSON"} {
		assert.True(t, sensitivePath(filepath.Join(home, name)), "in home: "+name)
		assert.True(t, sensitivePath(filepath.Join("/r", "devcontainer", name)), "anywhere: "+name)
	}
	for _, dir := range []string{".claude", ".codex", ".gemini"} {
		assert.True(t, sensitivePath(filepath.Join(home, dir)), dir)
		assert.True(t, sensitivePath(filepath.Join(home, dir, "settings.json")), dir+"/settings.json")
		assert.True(t, sensitivePath(filepath.Join(home, dir, "sessions", "x.jsonl")), dir+"/sessions/x.jsonl")
		assert.True(t, sensitivePath(filepath.Join(home, strings.ToUpper(dir), "settings.json")), "case: "+dir)
		// A repository's own folder of the same name is project configuration, not the user's.
		assert.False(t, sensitivePath(filepath.Join("/r", "repo", dir, "settings.json")), "repo "+dir+"/settings.json")
		assert.False(t, sensitivePath(filepath.Join(home, "projects", "repo", dir, "commands", "x.md")), "repo under home "+dir)
	}
	assert.False(t, sensitivePath(filepath.Join(home, ".claudeignore")), "a name that only starts like one")
	assert.False(t, sensitivePath(filepath.Join(home, "projects", "authorize.json")))
	// The home folder through a symlink is the same folder.
	link := filepath.Join(t.TempDir(), "home-link")
	linktest.Symlink(t, home, link)
	t.Setenv("HOME", link)
	t.Setenv("USERPROFILE", link) // Windows reads its home from USERPROFILE, not HOME (#7070)
	assert.True(t, sensitivePath(filepath.Join(realPath(home), ".codex", "config.toml")), "the resolved home")
	assert.True(t, sensitivePath(filepath.Join(link, ".codex", "config.toml")), "the home as given")
}

// A token in a remote URL lives in .git/config; no caller reads or lists the .git directory.
func TestGitDirectoryIsNeverServed(t *testing.T) {
	e := newBoundsEnv(t)
	require.NoError(t, exec.Command("git", "-C", e.repo, "remote", "add", "origin", "https://user:ghp_SECRETTOKEN@github.com/x/y.git").Run())
	for _, k := range []kind{kindTunnel, kindCurl, kindBearer, kindCrossSite, kindForgedFetch, kindDashboard} {
		for _, fp := range []string{".git/config", ".GIT/config", ".Git/HEAD", "./.git/config", filepath.Join(e.repo, ".git", "config"), ".git/hooks/pre-commit.sample"} {
			code, body := e.doAs("GET", fc, url.Values{"filepath": {fp}, "session_id": {"fb-1"}}, "", k)
			assert.Equal(t, 404, code, "%d %s", k, fp)
			assert.NotContains(t, body, "SECRETTOKEN", "%d %s", k, fp)
			assert.NotContains(t, body, "github.com", "%d %s", k, fp)
		}
		if k == kindDashboard {
			continue // the local explorer lists names only; reads (above) are refused for everyone
		}
		for _, dir := range []string{".git", ".git/hooks", "./.git"} {
			for _, q := range []url.Values{{"dir": {dir}}, {"dir": {dir}, "view": {"explorer"}}} {
				q.Set("session_id", "fb-1")
				_, body := e.doAs("GET", "/api/sessions/live/claude-fb/search-files", q, "", k)
				var resp struct {
					Entries []map[string]any `json:"entries"`
					Items   []map[string]any `json:"items"`
				}
				_ = json.Unmarshal([]byte(body), &resp)
				assert.Empty(t, resp.Entries, "%d %v: %s", k, q, body)
				assert.Empty(t, resp.Items, "%d %v: %s", k, q, body)
				assert.NotContains(t, body, "info/", "%d %v", k, q)
			}
		}
		code, _ := e.doAs("GET", "/api/sessions/live/claude-fb/resolve-path", url.Values{"filepath": {".git/config"}, "session_id": {"fb-1"}}, "", k)
		assert.Equal(t, 404, code, "%d resolve-path", k)
	}
	// The repository root listing does not show .git either.
	_, body := e.doAs("GET", "/api/sessions/live/claude-fb/search-files", url.Values{"dir": {"."}, "session_id": {"fb-1"}}, "", kindTunnel)
	assert.NotContains(t, body, ".git")
	// A worktree's .git file (a pointer) is covered by the same component rule, and git-aware
	// callers are unaffected because they run git rather than reading these paths.
	assert.True(t, sensitivePath("/r/wt/.git"))
	assert.True(t, sensitivePath("/r/.GIT/config"))
	assert.False(t, sensitivePath("/r/.github/workflows/ci.yml"))
	assert.False(t, sensitivePath("/r/.gitignore"))
}

// #3596 W1: SaveFileContent writes only inside the session's existing working tree, decided on
// the resolved path, and through the tunnel never anything git runs or obeys.
func TestSaveFileContentStaysInTheTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks and hard links")
	}
	e := newBoundsEnv(t)
	put := func(name, sid, fp string, via kind) int {
		code, _ := e.doAs("PUT", "/api/sessions/live/"+name+"/file-content", url.Values{"filepath": {fp}, "session_id": {sid}}, `{"content":"PWNED"}`, via)
		return code
	}
	absent := func(p string) {
		t.Helper()
		_, err := os.Stat(p)
		assert.True(t, os.IsNotExist(err), "%s was written", p)
	}
	outsideUnchanged := func() {
		t.Helper()
		b, _ := os.ReadFile(e.outside)
		assert.Equal(t, "outside-secret", string(b), "the outside file is unchanged")
	}

	// (a) A session whose working tree is gone: before, every path passed the check.
	gone := filepath.Join(e.base, "gone", "tree")
	require.NoError(t, e.ss.RegisterLiveSession(context.Background(), &store.LiveSession{AgentName: "claude-gone", AgentType: "claude", WorkingDir: gone, SessionID: "gone-1"}))
	assert.NotEqual(t, 200, put("claude-gone", "gone-1", "../../written-by-gone.txt", kindDashboard))
	absent(filepath.Join(e.base, "written-by-gone.txt"))

	// (b) A new file under a symlinked folder that leads out (the parent doesn't exist yet).
	assert.Equal(t, 403, put("claude-fb", "fb-1", "app/linkdir/new/f.txt", kindDashboard))
	absent(filepath.Join(e.base, "outdir", "new"))

	// A hard link inside the tree to the outside file: another name for it.
	require.NoError(t, os.Link(e.outside, filepath.Join(e.repo, "hard.txt")))
	assert.Equal(t, 403, put("claude-fb", "fb-1", "hard.txt", kindDashboard))
	outsideUnchanged()

	// (c) Through the tunnel, a link inside the repository into .git/hooks, as a folder or as
	// the file itself; .GIT in any case; .husky; core.hooksPath; a .git FILE.
	hooks := filepath.Join(e.repo, ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooks, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooks, "post-checkout"), []byte("#!/bin/sh\n"), 0o755))
	linktest.Symlink(t, hooks, filepath.Join(e.repo, "tools-link"))
	linktest.Symlink(t, filepath.Join(hooks, "post-checkout"), filepath.Join(e.repo, "notes-link"))
	require.NoError(t, exec.Command("git", "-C", e.repo, "config", "core.hooksPath", "ci/hooks").Run())
	for _, fp := range []string{"tools-link/pre-commit", "notes-link", ".GIT/hooks/pre-push", ".Git/config", ".husky/pre-commit", "ci/hooks/pre-commit", "CI/Hooks/pre-commit", "sub/.git"} {
		assert.Equal(t, 403, put("claude-fb", "fb-1", fp, kindTunnel), fp)
	}
	absent(filepath.Join(hooks, "pre-commit"))
	absent(filepath.Join(hooks, "pre-push"))
	absent(filepath.Join(e.repo, ".husky"))
	absent(filepath.Join(e.repo, "ci"))
	b, _ := os.ReadFile(filepath.Join(hooks, "post-checkout"))
	assert.Equal(t, "#!/bin/sh\n", string(b), "the hook is unchanged")

	// What should work still does.
	assert.Equal(t, 200, put("claude-fb", "fb-1", "app/new/dir/file.go", kindTunnel))
	assert.Equal(t, 200, put("claude-fb", "fb-1", "README.md", kindDashboard))
	got, _ := os.ReadFile(filepath.Join(e.repo, "app", "new", "dir", "file.go"))
	assert.Equal(t, "PWNED", string(got))
}

// #3596 W2, W3, W4: the base file, the changes diff and a stored diff get the same bounds for
// every caller that isn't this computer's own dashboard (the tunnel, a Bearer-key proxy, curl),
// and a session id in their path is one safe path element.
func TestDiffRoutesBoundEveryNonDashboardCaller(t *testing.T) {
	e := newBoundsEnv(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", e.repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+e.home)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	git("config", "user.email", "t@example.invalid")
	git("config", "user.name", "t")
	git("add", "-A")
	git("commit", "-qm", "init")
	// The committed .env is no longer in the working tree: only the diff base still has it.
	require.NoError(t, os.Remove(filepath.Join(e.repo, ".env")))
	require.NoError(t, os.WriteFile(filepath.Join(e.repo, "README.md"), []byte("changed"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(e.repo, ".env.new"), []byte("TOKEN=untracked-secret"), 0o644))

	strictKinds := map[string]kind{"tunnel": kindTunnel, "bearer": kindBearer, "curl": kindCurl}
	original := url.Values{"filepath": {".env"}, "session_id": {"fb-1"}}

	// W2: the base file.
	code, body := e.doAs("GET", "/api/sessions/live/claude-fb/file-original", original, "", kindDashboard)
	assert.Equal(t, 200, code)
	assert.Contains(t, body, "TOKEN=abc", "the local dashboard still sees it")
	for name, k := range strictKinds {
		code, body := e.doAs("GET", "/api/sessions/live/claude-fb/file-original", original, "", k)
		assert.Equal(t, 404, code, name)
		assert.NotContains(t, body, "TOKEN=abc", name)
	}

	// W3: the changes diff, live and by session id.
	for _, path := range []string{"/api/sessions/live/claude-fb/changes.diff", "/api/sessions/fb-1/changes"} {
		q := url.Values{"session_id": {"fb-1"}}
		for name, k := range strictKinds {
			code, body := e.doAs("GET", path, q, "", k)
			assert.Equal(t, 200, code, "%s %s", name, path)
			assert.NotContains(t, body, "untracked-secret", "%s %s: filtered", name, path)
			assert.NotContains(t, body, "TOKEN=abc", "%s %s: filtered", name, path)
		}
		code, body := e.doAs("GET", path, q, "", kindDashboard)
		assert.Equal(t, 200, code, path)
		assert.Contains(t, body, "untracked-secret", "the local dashboard gets the whole diff: %s", path)
	}

	// W4: a session id in the stored diff's path is one safe element.
	for _, sid := range []string{"..", ".", "-x", "a.b"} {
		for _, path := range []string{"/api/sessions/" + sid + "/changes", "/api/sessions/" + sid + "/changes.diff"} {
			code, _ := e.doAs("GET", path, url.Values{}, "", kindDashboard)
			assert.Equal(t, 400, code, path)
		}
	}
}

// #3596 W6: an agent-writable .git/config can make git report any folder as the repository's
// root (core.worktree). That folder must not become a root for the file routes or auto-attach.
func TestCoreWorktreeCannotWidenTheRoot(t *testing.T) {
	e := newBoundsEnv(t)
	elsewhere := filepath.Join(e.base, "outdir") // holds inner.txt; unrelated to the session
	workdir := filepath.Join(e.repo, "app")
	require.NoError(t, exec.Command("git", "-C", e.repo, "config", "core.worktree", elsewhere).Run())
	// The precondition: git really does report the unrelated folder.
	realElsewhere, _ := filepath.EvalSymlinks(elsewhere)
	got := gitutil.ResolveGitRoot(context.Background(), workdir)
	realGot, _ := filepath.EvalSymlinks(got)
	require.Equal(t, realElsewhere, realGot, "git reports core.worktree as the root")

	assert.Equal(t, workdir, trustedGitRoot(context.Background(), workdir), "Coral falls back to the working directory")
	_, dirsGit := SessionDirs(e.ss)(context.Background(), "fb-1")
	assert.Equal(t, workdir, dirsGit, "auto-attach's root too")

	// Through the tunnel the unrelated folder's file isn't readable.
	code, body := e.do("GET", fc, url.Values{"filepath": {filepath.Join(elsewhere, "inner.txt")}, "session_id": {"fb-1"}}, "", tunneled)
	assert.NotEqual(t, 200, code)
	assert.NotContains(t, body, "inner")

	// A real parent repository and a child repository still count.
	require.NoError(t, exec.Command("git", "-C", e.repo, "config", "--unset", "core.worktree").Run())
	realRepo, _ := filepath.EvalSymlinks(e.repo)
	realRoot, _ := filepath.EvalSymlinks(trustedGitRoot(context.Background(), workdir))
	assert.Equal(t, realRepo, realRoot, "the repository workdir is in")
}

// Security's W6 SHOULD: a forged core.worktree naming a real PARENT folder that isn't a
// repository (one holding other projects) isn't trusted; a real parent repository and a
// gitdir-file repository (a worktree) still are.
func TestTrustedGitRootNeedsARealRepository(t *testing.T) {
	e := newBoundsEnv(t)
	workdir := filepath.Join(e.repo, "app")
	// e.base holds the repository and other folders, and has no .git of its own.
	require.NoError(t, exec.Command("git", "-C", e.repo, "config", "core.worktree", e.base).Run())
	realBase, _ := filepath.EvalSymlinks(e.base)
	gotGit, _ := filepath.EvalSymlinks(gitutil.ResolveGitRoot(context.Background(), workdir))
	require.Equal(t, realBase, gotGit, "git reports the forged parent")
	assert.Equal(t, workdir, trustedGitRoot(context.Background(), workdir), "a parent that isn't a repository falls back to the workdir")
	require.NoError(t, exec.Command("git", "-C", e.repo, "config", "--unset", "core.worktree").Run())

	// A real parent repository (its own .git folder) still counts.
	realRepo, _ := filepath.EvalSymlinks(e.repo)
	got, _ := filepath.EvalSymlinks(trustedGitRoot(context.Background(), workdir))
	assert.Equal(t, realRepo, got)

	// A worktree, whose .git is a gitdir FILE, still counts.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", e.repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+e.home, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	run("add", "app/main.go")
	run("commit", "-qm", "init")
	wt := filepath.Join(e.base, "wt")
	run("worktree", "add", "-q", "-b", "w", wt)
	fi, err := os.Lstat(filepath.Join(wt, ".git"))
	require.NoError(t, err)
	require.True(t, fi.Mode().IsRegular(), "a worktree's .git is a file")
	realWT, _ := filepath.EvalSymlinks(wt)
	got, _ = filepath.EvalSymlinks(trustedGitRoot(context.Background(), filepath.Join(wt, "app")))
	assert.Equal(t, realWT, got)
}
