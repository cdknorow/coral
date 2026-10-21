package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cloud"
	"github.com/subgentic/coral/internal/secrets"
	"github.com/subgentic/coral/internal/server/routes"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/hubproxy"
	"github.com/subgentic/coral/pkg/redact"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #3958, end to end: the real hub proxy, a real tunnel and the connector in front of the real
// router. The hub's embedder gives each request its viewer's tier (tunnel.WithHubTier); the
// tier decides only whether answers are redacted: Interact and Launch read a file holding a
// token as it is, View and a request with no tier get the marker, a browser's X-Coral-Hub-Tier
// changes nothing, and at every tier a credential file stays 404 and a Never route refused
// (coral-hub Security's condition: the tier is about redaction only).
func TestHubTierEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	home, repo := filepath.Join(base, "home"), filepath.Join(base, "repo")
	require.NoError(t, os.MkdirAll(home, 0o755))
	setHome(t, home)
	t.Setenv("CORAL_PROJECT_ROOTS", "")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "app"), 0o755))
	require.NoError(t, exec.Command("git", "init", "-q", repo).Run())
	tok := "ghp_" + strings.Repeat("aB3dE6gH9k", 4)[:36]
	require.NoError(t, os.WriteFile(filepath.Join(repo, "app", "notes.txt"), []byte("token "+tok+" here\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".env"), []byte("TOKEN=abc\n"), 0o644))

	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	stack := newTermStack(t)
	srv := newTestServer(t, cfg, db, stack.backend, stack.terminal)
	local := httptest.NewServer(srv.Router())
	t.Cleanup(local.Close)
	ss := store.NewSessionStore(db)
	require.NoError(t, ss.RegisterLiveSession(context.Background(), &store.LiveSession{
		AgentName: "claude-tier", AgentType: "claude", WorkingDir: filepath.Join(repo, "app"), SessionID: "tier-1"}))
	require.NoError(t, ss.SetSetting(context.Background(), routes.MaxModeSetting, tunnel.TierLaunch.String()))
	cur := routes.MaxModeFromSettings(ss)

	sessions := make(chan *tunnel.Session, 1)
	ths := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s, err := tunnel.AcceptHTTP(w, r, func(context.Context, string) (any, error) { return "x", nil }, tunnel.SessionConfig{}); err == nil {
			sessions <- s
		}
	}))
	t.Cleanup(ths.Close)
	cs, err := tunnel.Dial(context.Background(), "ws"+strings.TrimPrefix(ths.URL, "http"), "tok", tunnel.ClientOptions{
		Handler:       &tunnel.LocalHandler{Redactor: secrets.Redactor(), BaseURL: local.URL, AllowRequest: tunnel.AllowRequest(cur), MaxMode: cur, ViaValue: cloud.ViaSecret()},
		AllowInsecure: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { cs.Close() })
	sess := <-sessions
	t.Cleanup(func() { sess.Close() })

	// The embedder: it authenticated its member and knows the member's tier; the proxy never
	// reads a tier from the request.
	var tier atomic.Value
	tier.Store("")
	hub := httptest.NewUnstartedServer(nil)
	r := chi.NewRouter()
	proxy := hubproxy.NewWithOptions(oneTunnel{sess}, hubproxy.Options{AllowedOrigins: []string{hub.Listener.Addr().String()}})
	r.Handle("/api/remote/{server}/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if v := tier.Load().(string); v != "" {
			req = req.WithContext(tunnel.WithHubTier(req.Context(), v))
		}
		proxy.ServeHTTP(w, req)
	}))
	hub.Config.Handler = r
	hub.Start()
	t.Cleanup(hub.Close)

	get := func(viewer, path string, hdr map[string]string) (int, string) {
		t.Helper()
		tier.Store(viewer)
		req, _ := http.NewRequest("GET", hub.URL+"/api/remote/srv"+path, bytes.NewReader(nil))
		req.Header.Set("Origin", hub.URL)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	const notes = "/api/sessions/live/claude-tier/file-content?session_id=tier-1&filepath=app/notes.txt"
	for viewer, raw := range map[string]bool{"": false, "view": false, "interact": true, "launch": true} {
		code, body := get(viewer, notes, nil)
		require.Equal(t, http.StatusOK, code, "%q: %s", viewer, body)
		assert.Equal(t, raw, strings.Contains(body, tok), "viewer %q: raw %v: %s", viewer, raw, body)
		if !raw {
			assert.Contains(t, body, redact.Marker, viewer)
		}
	}
	_, body := get("view", notes, map[string]string{"X-Coral-Hub-Tier": "launch"})
	assert.NotContains(t, body, tok, "a browser's X-Coral-Hub-Tier is not a tier")

	for _, viewer := range []string{"interact", "launch"} {
		code, _ := get(viewer, "/api/sessions/live/claude-tier/resolve-path?session_id=tier-1&filepath=.env", nil)
		assert.Equal(t, http.StatusNotFound, code, "%s: a credential file stays hidden", viewer)
		code, _ = get(viewer, "/api/sessions/live/claude-tier/file-content?session_id=tier-1&filepath=.env", nil)
		assert.Equal(t, http.StatusNotFound, code, "%s: and can't be read", viewer)
		code, _ = get(viewer, "/api/settings", nil)
		assert.Equal(t, http.StatusForbidden, code, "%s: a Never route stays refused", viewer)
	}
}
