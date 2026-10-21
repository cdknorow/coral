package hooks

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agentenv"
	"github.com/subgentic/coral/internal/secfile"
)

// #3387: an agent tool sends its session token and names itself, with no action from the
// agent; without a token, nothing is added but the User-Agent.
func TestInstallIdentity(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Clone() }))
	defer srv.Close()
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })

	t.Setenv("CORAL_SESSION_TOKEN", "tok123")
	InstallIdentity("coral-board")
	InstallIdentity("coral-board") // twice is harmless
	_, err := http.Get(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "tok123", got.Get(SessionTokenHeader))
	assert.Equal(t, "coral-board/"+Version, got.Get("User-Agent"))

	http.DefaultTransport = prev
	t.Setenv("CORAL_SESSION_TOKEN", "")
	InstallIdentity("coral-hook-task-sync")
	_, err = http.Get(srv.URL)
	require.NoError(t, err)
	assert.Empty(t, got.Get(SessionTokenHeader))
	assert.Equal(t, "coral-hook-task-sync/"+Version, got.Get("User-Agent"))
}

// recorder answers every request itself (no network) and keeps what it was sent.
type recorder struct{ got []*http.Request }

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.got = append(r.got, req)
	return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}, Request: req}, nil
}

// #3581: the token goes only to loopback or the configured Coral's host.
func TestTokenOnlyToTheAgentsCoral(t *testing.T) {
	t.Setenv("CORAL_URL", "https://coral.example:9443")
	t.Setenv("CORAL_HOST", "")
	rec := &recorder{}
	client := &http.Client{Transport: identityTransport{base: rec, tool: "coral-board", token: "tok123", hosts: configuredHosts()}}
	for target, want := range map[string]string{
		"http://127.0.0.1:8420/api/board":     "tok123",
		"http://localhost:8420/api/board":     "tok123",
		"http://[::1]:8420/api/board":         "tok123",
		"https://coral.example:9443/api/x":    "tok123",
		"https://CORAL.example/api/x":         "tok123",
		"https://evil.example/api/board":      "",
		"http://127.0.0.1.evil.example/x":     "",
		"http://10.0.0.5:8420/api/board":      "",
		"https://coral.example.evil.example/": "",
	} {
		rec.got = nil
		req, _ := http.NewRequest("GET", target, nil)
		if target == "https://evil.example/api/board" {
			req.Header.Set(SessionTokenHeader, "tok123") // set by a caller: still dropped
		}
		_, err := client.Do(req)
		require.NoError(t, err)
		require.Len(t, rec.got, 1)
		assert.Equal(t, want, rec.got[0].Header.Get(SessionTokenHeader), target)
	}
}

// #3581: a redirect to another host or scheme isn't followed, and a followed redirect
// carries no token.
func TestRedirectsCarryNoToken(t *testing.T) {
	t.Setenv("CORAL_URL", "")
	t.Setenv("CORAL_HOST", "")
	var hops []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops = append(hops, r.Header.Clone())
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, "http://evil.example/collect", http.StatusFound)
		case "/away-scheme":
			http.Redirect(w, r, "https://"+r.Host+"/next", http.StatusFound)
		case "/here":
			http.Redirect(w, r, "/next", http.StatusFound)
		}
	}))
	defer srv.Close()
	evil := &recorder{}
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "evil.example" {
			return evil.RoundTrip(r)
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	client := &http.Client{Transport: identityTransport{base: base, tool: "coral-board", token: "tok123", hosts: configuredHosts()}}

	for _, path := range []string{"/away", "/away-scheme"} {
		hops = nil
		_, err := client.Get(srv.URL + path)
		require.ErrorIs(t, err, errRedirectRefused, path)
		require.Len(t, hops, 1, path)
		assert.Equal(t, "tok123", hops[0].Get(SessionTokenHeader), "the first request is to the agent's Coral")
	}
	assert.Empty(t, evil.got, "nothing was sent to the other host")

	hops = nil
	resp, err := client.Get(srv.URL + "/here")
	require.NoError(t, err)
	resp.Body.Close()
	require.Len(t, hops, 2)
	assert.Equal(t, "tok123", hops[0].Get(SessionTokenHeader))
	assert.Empty(t, hops[1].Get(SessionTokenHeader), "a followed redirect carries no token")

	// Even a token the caller set on the request itself (the client copies a request's own
	// headers onto a redirect) is dropped.
	hops = nil
	req, _ := http.NewRequest("GET", srv.URL+"/here", nil)
	req.Header.Set(SessionTokenHeader, "tok123")
	resp, err = client.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Len(t, hops, 2)
	assert.Empty(t, hops[1].Get(SessionTokenHeader), "a caller-set token isn't carried over a redirect")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTrustedServerURL(t *testing.T) {
	for _, c := range []struct {
		candidate, configured string
		want                  bool
	}{
		{"http://127.0.0.1:8450", "http://localhost:8420", true},
		{"http://localhost:9000/", "http://localhost:8420", true},
		{"http://[::1]:8420", "", true},
		{"https://coral.example:9443", "https://coral.example:9443/", true},
		{"https://coral.example:9444", "https://coral.example:9443", false},
		{"https://evil.example", "http://localhost:8420", false},
		{"http://127.0.0.1.evil.example", "", false},
		{"http://user:pw@127.0.0.1:8420", "", false},
		{"http://127.0.0.1:8420/api", "", false},
		{"ftp://127.0.0.1", "", false},
		{"", "http://localhost:8420", false},
	} {
		assert.Equal(t, c.want, TrustedServerURL(c.candidate, c.configured), c.candidate)
	}
}

// #13: inside a command Codex ran, which hides CORAL_SESSION_TOKEN, the token comes from the
// file agentenv.SessionAuthFileEnv names, read only when the variable is missing, never over
// it, and only from a private regular file.
func TestSessionTokenFallsBackToTheAuthFile(t *testing.T) {
	data := t.TempDir()
	t.Setenv("CORAL_DATA_DIR", data)
	dir := filepath.Join(data, "session_tokens")
	require.NoError(t, os.Mkdir(dir, 0o700))
	file := filepath.Join(dir, "t-13")
	require.NoError(t, secfile.WriteFileNoFollow(file, []byte("filetok\n"), 0o600))
	t.Setenv(agentenv.SessionAuthFileEnv, file)

	t.Setenv("CORAL_SESSION_TOKEN", "")
	assert.Equal(t, "filetok", sessionToken(), "the file when the variable is missing")
	t.Setenv("CORAL_SESSION_TOKEN", "envtok")
	assert.Equal(t, "envtok", sessionToken(), "the variable wins: the file never overrides it")

	t.Setenv("CORAL_SESSION_TOKEN", "")
	t.Setenv(agentenv.SessionAuthFileEnv, filepath.Join(dir, "missing"))
	assert.Empty(t, sessionToken())
	if runtime.GOOS != "windows" {
		open := filepath.Join(dir, "t-open")
		require.NoError(t, os.WriteFile(open, []byte("opentok"), 0o644))
		t.Setenv(agentenv.SessionAuthFileEnv, open)
		assert.Empty(t, sessionToken(), "a file others can read is not a token file")
	}
	link := filepath.Join(dir, "t-link")
	if os.Symlink(file, link) == nil {
		t.Setenv(agentenv.SessionAuthFileEnv, link)
		assert.Empty(t, sessionToken(), "never through a link")
	}
	// A private file anywhere but this Coral's session_tokens folder: no token (#13 review N1).
	elsewhere := filepath.Join(t.TempDir(), "t-13")
	require.NoError(t, secfile.WriteFileNoFollow(elsewhere, []byte("othertok"), 0o600))
	t.Setenv(agentenv.SessionAuthFileEnv, elsewhere)
	assert.Empty(t, sessionToken(), "outside <data>/session_tokens")
	t.Setenv(agentenv.SessionAuthFileEnv, filepath.Join(dir, "..", "session_tokens", "..", "t-13"))
	assert.Empty(t, sessionToken(), "a path that leaves the folder")

	t.Setenv(agentenv.SessionAuthFileEnv, "")
	assert.Empty(t, sessionToken())
}
