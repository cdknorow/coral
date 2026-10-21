package routes

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/linktest"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/internal/tmux"
)

// Settings > Terminal backend (#3634).

type backendRig struct {
	t  *testing.T
	h  *CloudHandler
	ss *store.SessionStore
}

func newBackendRig(t *testing.T, dataDir string) *backendRig {
	t.Helper()
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	ss := store.NewSessionStore(db)
	h := NewCloudHandler(ss, dataDir)
	h.SetPeerCheck(func(*http.Request) (bool, bool) { return true, true })
	h.BackendRunning = "tmux"
	return &backendRig{t: t, h: h, ss: ss}
}

// as: "dashboard" (this computer's dashboard, a person), "agent" (an agent's token with forged
// dashboard headers), "hub" (a forged Coral Hub stamp).
func (b *backendRig) req(method, path, body, as string) *httptest.ResponseRecorder {
	b.t.Helper()
	var r *http.Request
	switch as {
	case "hub":
		r = stamp(httptest.NewRequest(method, path, strings.NewReader(body)))
	default:
		r = httptest.NewRequest(method, "http://127.0.0.1:8420"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:5555"
		r.Header.Set("Origin", "http://127.0.0.1:8420")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if as == "dashboard" {
			r = r.WithContext(context.WithValue(r.Context(), callerKindKey{}, callerDashCookie))
		} else {
			r.Header.Set(store.SessionTokenHeader, "agent-token")
			r = r.WithContext(context.WithValue(r.Context(), verifiedSessionKey{}, &store.SessionTokenOwner{SessionID: "s1", SessionName: "agent-s1"}))
		}
	}
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	switch path {
	case "/api/system/restart":
		b.h.RestartServer(rec, r)
	case "/api/system/terminal-backend/end-leftover":
		b.h.EndLeftoverTmuxAgents(rec, r)
	default:
		b.h.TerminalBackend(rec, r)
	}
	return rec
}

func TestTerminalBackendSetting(t *testing.T) {
	if !backendChoice() {
		t.Skip("no tmux here: this system has no backend choice")
	}
	b := newBackendRig(t, t.TempDir())
	rec := b.req("GET", "/api/system/terminal-backend", "", "dashboard")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"running":"tmux"`)
	assert.Contains(t, rec.Body.String(), `"saved":""`)
	assert.Contains(t, rec.Body.String(), `"restart_required":false`)
	assert.Contains(t, rec.Body.String(), `"choice":true`)

	for _, bad := range []string{`{}`, `{"backend":"PTY"}`, `{"backend":" pty"}`, `{"backend":"screen"}`} {
		assert.Equal(t, 400, b.req("PUT", "/api/system/terminal-backend", bad, "dashboard").Code, bad)
	}
	rec = b.req("PUT", "/api/system/terminal-backend", `{"backend":"pty"}`, "dashboard")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"saved":"pty"`)
	assert.Contains(t, rec.Body.String(), `"restart_required":true`, "it applies at the next start")
	s, _ := b.ss.GetSettings(t.Context())
	assert.Equal(t, "pty", s[TerminalBackendSetting])

	// --backend forced the running one: nothing to apply, whatever is saved.
	b.h.BackendForced = true
	assert.Contains(t, b.req("GET", "/api/system/terminal-backend", "", "dashboard").Body.String(), `"restart_required":false`)
}

// Writing the backend, the restart and End them need a person on this computer: an agent's
// token (even with dashboard headers) and a Coral Hub request are refused (Security's REQ 1, 3).
func TestTerminalBackendNeedsAPerson(t *testing.T) {
	b := newBackendRig(t, t.TempDir())
	b.h.Restart = func() error { t.Fatal("must not restart"); return nil }
	b.h.BackendRunning = "pty"
	for _, as := range []string{"agent", "hub"} {
		for _, c := range [][3]string{
			{"PUT", "/api/system/terminal-backend", `{"backend":"tmux"}`},
			{"POST", "/api/system/restart", `{}`},
			{"POST", "/api/system/terminal-backend/end-leftover", `{}`},
		} {
			rec := b.req(c[0], c[1], c[2], as)
			assert.Equal(t, 403, rec.Code, "%s %s %s: %s", as, c[0], c[1], rec.Body.String())
		}
	}
	s, _ := b.ss.GetSettings(t.Context())
	assert.Empty(t, s[TerminalBackendSetting], "nothing saved by the refused callers")
}

// The generic settings PUT can't write the backend past its own route.
func TestPutSettingsRefusesTerminalBackend(t *testing.T) {
	db, err := storetest.Open(t, filepath.Join(t.TempDir(), "c.db"))
	require.NoError(t, err)
	defer db.Close()
	h := NewSystemHandler(db, nil)
	for _, k := range []string{"terminal_backend", "Terminal_Backend", " terminal_backend "} {
		r := httptest.NewRequest("PUT", "/api/settings", strings.NewReader(fmt.Sprintf(`{%q:"pty"}`, k)))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.PutSettings(rec, r)
		assert.Equal(t, 400, rec.Code, k)
	}
	s, _ := store.NewSessionStore(db).GetSettings(t.Context())
	assert.Empty(t, s["terminal_backend"])
}

// End them acts only on Coral's private socket and only on {type}-{uuid} sessions: a decoy
// session on that server and a whole second tmux server are left alone (Security's REQ 2, 3).
func TestEndLeftoverTmuxAgentsTouchesOnlyCoralsAgents(t *testing.T) {
	bin, ok := tmux.IsAvailable()
	if !ok {
		t.Skip("tmux not installed")
	}
	dataDir, err := os.MkdirTemp("/tmp", "cb") // a short path: tmux sockets have a length limit
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	private := tmux.PrivateSocketPath(dataDir)
	other := filepath.Join(dataDir, "other.sock")
	run := func(sock string, args ...string) {
		t.Helper()
		cmd := exec.Command(bin, append([]string{"-S", sock}, args...)...)
		cmd.Env = append(os.Environ(), "TMUX=")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	t.Cleanup(func() {
		for _, s := range []string{private, other} {
			exec.Command(bin, "-S", s, "kill-server").Run()
		}
	})
	a1 := "claude-0b2f6c1e-3d4a-4b5c-8d6e-7f8091a2b3c4"
	a2 := "terminal-a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
	run(private, "new-session", "-d", "-s", a1, "sleep 600")
	run(private, "new-session", "-d", "-s", a2, "sleep 600")
	run(private, "new-session", "-d", "-s", "decoy", "sleep 600")
	run(other, "new-session", "-d", "-s", "claude-11111111-2222-4333-8444-555555555555", "sleep 600")

	b := newBackendRig(t, dataDir)
	assert.Equal(t, 409, b.req("POST", "/api/system/terminal-backend/end-leftover", `{}`, "dashboard").Code, "on tmux there is nothing left over")
	b.h.BackendRunning = "pty"
	rec := b.req("GET", "/api/system/terminal-backend", "", "dashboard")
	assert.Contains(t, rec.Body.String(), `"leftover_tmux_agents":2`, rec.Body.String())
	rec = b.req("POST", "/api/system/terminal-backend/end-leftover", `{}`, "dashboard")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"ended":2,"left":0}`, rec.Body.String())

	list := func(sock string) string {
		out, _ := exec.Command(bin, "-S", sock, "list-sessions", "-F", "#{session_name}").CombinedOutput()
		return string(out)
	}
	assert.Equal(t, "decoy\n", list(private), "the decoy on Coral's server is left alone")
	assert.Contains(t, list(other), "claude-11111111-2222-4333-8444-555555555555", "another tmux server is never touched")
}

// A symlink at <data dir>/tmux.sock is not Coral's socket: nothing is listed or ended through it,
// and the server it points at is left alone (Security's Lstat check, #3634).
func TestEndLeftoverRefusesASymlinkedSocket(t *testing.T) {
	bin, ok := tmux.IsAvailable()
	if !ok {
		t.Skip("tmux not installed")
	}
	dataDir, err := os.MkdirTemp("/tmp", "cs")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	target := filepath.Join(dataDir, "elsewhere.sock")
	start := exec.Command(bin, "-S", target, "new-session", "-d", "-s", "claude-11111111-2222-4333-8444-555555555555", "sleep 600")
	start.Env = append(os.Environ(), "TMUX=")
	out, err := start.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() { exec.Command(bin, "-S", target, "kill-server").Run() })
	linktest.Symlink(t, target, tmux.PrivateSocketPath(dataDir))

	assert.Empty(t, tmux.PrivateAgentSessions(context.Background(), tmux.PrivateSocketPath(dataDir)))
	b := newBackendRig(t, dataDir)
	b.h.BackendRunning = "pty"
	rec := b.req("POST", "/api/system/terminal-backend/end-leftover", `{}`, "dashboard")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"ended":0,"left":0}`, rec.Body.String())
	ls, _ := exec.Command(bin, "-S", target, "list-sessions", "-F", "#{session_name}").CombinedOutput()
	assert.Contains(t, string(ls), "claude-11111111-2222-4333-8444-555555555555", "the server behind the link is untouched")
}
