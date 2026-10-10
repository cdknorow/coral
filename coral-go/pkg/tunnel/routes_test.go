package tunnel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

func TestClassifyTiers(t *testing.T) {
	for _, tc := range []struct {
		method, path, query string
		want                Tier
	}{
		{"GET", "/api/health", "", TierView},
		{"HEAD", "/api/sessions/live", "", TierView},
		{"GET", "/api/sessions/live/a/chat", "", TierView},
		{"GET", "/ws/terminal/a", "", TierView},
		{"GET", "/ws/coral", "", TierView},
		{"GET", "/api/board/p/tasks/7", "", TierView},
		{"POST", "/api/sessions/live/a/send", "", TierInteract},
		{"POST", "/api/sessions/live/a/keys", "", TierInteract},
		{"POST", "/api/sessions/live/a/answer-prompt", "", TierInteract},
		{"POST", "/api/board/p/messages", "", TierInteract},
		{"POST", "/api/board/p/tasks", "", TierInteract},
		{"PUT", "/api/sessions/live/a/display-name", "", TierInteract},
		{"DELETE", "/api/board/p/messages/3", "", TierInteract},
		{"POST", "/api/sessions/live/a/kill", "", TierInteract},
		{"POST", "/api/sessions/launch", "", TierLaunch},
		{"POST", "/api/sessions/live/a/open-in-editor", "", TierLaunch},
		{"POST", "/api/board/p/resume", "", TierLaunch},
		{"POST", "/api/sessions/live/team/t/reset", "", TierLaunch},
		{"GET", "/api/filesystem/list", "path=/Users/x", TierLaunch},
		// Never: settings, secrets, host access, outbound requests, connector, agent API.
		{"GET", "/api/settings", "", TierNever},
		{"PUT", "/api/settings", "", TierNever},
		{"GET", "/api/system/api-key", "", TierNever},
		{"POST", "/api/system/api-key/regenerate", "", TierNever},
		{"GET", "/api/system/status", "", TierNever},
		{"PUT", "/api/sessions/live/a/file-content", "", TierLaunch},
		{"POST", "/api/upload", "", TierLaunch},
		{"POST", "/api/webhooks", "", TierNever},
		{"POST", "/api/workflows", "", TierNever},
		{"PUT", "/api/workflows/1", "", TierNever},
		{"POST", "/api/scheduled/jobs", "", TierNever},
		{"POST", "/api/servers", "", TierNever},
		{"POST", "/api/board/remotes", "", TierNever},
		{"GET", "/api/agent/ui", "", TierNever},
		{"GET", "/api/cloud/status", "", TierNever},
		{"POST", "/api/license/activate", "", TierNever},
		// Method and shape.
		{"DELETE", "/api/sessions/live/a/chat", "", TierNever},
		{"TRACE", "/api/health", "", TierNever},
		{"GET", "/api/health/", "", TierNever},
		{"GET", "/API/health", "", TierNever},
		{"GET", "/api//health", "", TierNever},
		{"GET", "/api/sessions/live/../settings", "", TierNever},
		{"GET", "/api/sessions/live/a/chat/extra", "", TierNever},
		{"GET", "/api/sessions/live/a\\b/chat", "", TierNever},
		{"GET", "", "", TierNever},
		{"GET", "/", "", TierNever},
		{"GET", "http://evil.example/api/health", "", TierNever},
		{"GET", "//evil.example/api/health", "", TierNever},
	} {
		assert.Equal(t, tc.want, Classify(tc.method, tc.path, tc.query), "%s %s?%s", tc.method, tc.path, tc.query)
	}
}

// P-8: file routes carry project-relative paths only; the connector is the barrier
// until the local API itself is fixed.
func TestFileParamsMustBeRelative(t *testing.T) {
	routes := []string{
		"/api/sessions/live/a/file-content", "/api/sessions/live/a/file-original", "/api/sessions/live/a/resolve-path",
		"/api/sessions/live/a/search-files", "/api/sessions/live/a/diff",
	}
	bad := []string{"/etc/passwd", "~/.ssh/id_rsa", "~", "../../etc/passwd", "a/../../b", "..", "-rf", "C:\\Windows", "c:/x",
		"a\\b", "a\x00b", "/Users/x/.coral/api_key"}
	for _, r := range routes {
		for _, param := range []string{"filepath", "dir", "path", "ref", "file"} {
			for _, v := range bad {
				q := url.Values{param: {v}}.Encode()
				assert.Equal(t, TierNever, Classify("GET", r, q), "%s ?%s", r, q)
			}
		}
		// A second, harmless value cannot hide a bad one.
		assert.Equal(t, TierNever, Classify("GET", r, "filepath=ok.go&filepath=/etc/passwd"))
		for _, ok := range []string{"main.go", "src/app/main.go", "a b.txt", "src/", "dir.with.dots/x", "..hidden", "a..b"} {
			assert.Equal(t, TierView, Classify("GET", r, url.Values{"filepath": {ok}, "dir": {ok}}.Encode()), "%s %q", r, ok)
		}
	}
	// Launch-tier folder browsing legitimately takes absolute paths.
	assert.Equal(t, TierLaunch, Classify("GET", "/api/filesystem/list", "path=/Users/me/code"))
	// Raw control characters in a query are refused outright.
	assert.Equal(t, TierNever, Classify("GET", "/api/health", "a=b\r\nX: y"))
}

func TestModeGate(t *testing.T) {
	mode := TierView
	allow := AllowRequest(func() Mode { return mode })
	assert.True(t, allow("GET", "/api/sessions/live", ""))
	assert.False(t, allow("POST", "/api/sessions/live/a/send", ""))
	assert.False(t, allow("POST", "/api/sessions/launch", ""))
	mode = TierInteract
	assert.True(t, allow("POST", "/api/sessions/live/a/send", ""))
	assert.False(t, allow("POST", "/api/sessions/launch", ""))
	assert.False(t, allow("GET", "/api/filesystem/list", "path=/"))
	mode = TierLaunch
	assert.True(t, allow("POST", "/api/sessions/launch", ""))
	assert.False(t, allow("GET", "/api/settings", ""), "never stays never in launch mode")
	mode = TierNever // garbage mode values fall back to view
	assert.True(t, allow("GET", "/api/health", ""))
	assert.False(t, allow("POST", "/api/sessions/live/a/send", ""))
	assert.False(t, AllowRequest(nil)("POST", "/api/sessions/live/a/send", ""), "nil mode function means view")

	assert.Equal(t, TierView, Lower(TierView, TierLaunch))
	assert.Equal(t, TierInteract, Lower(TierLaunch, TierInteract))
	for s, want := range map[string]Mode{"view": TierView, "Interact": TierInteract, " launch ": TierLaunch} {
		m, ok := ParseMode(s)
		assert.True(t, ok)
		assert.Equal(t, want, m)
	}
	for _, s := range []string{"", "admin", "never", "all"} {
		_, ok := ParseMode(s)
		assert.False(t, ok, s)
	}
}

func TestFrameTier(t *testing.T) {
	assert.Equal(t, TierInteract, FrameTier("/ws/terminal/a", []byte(`{"type":"terminal_input","data":"ls\n"}`)))
	assert.Equal(t, TierInteract, FrameTier("/ws/terminal/a", []byte(`{"type":"terminal_resize","cols":80,"rows":24}`)))
	assert.Equal(t, TierInteract, FrameTier("/ws/terminal/a", []byte(`not json`)), "unparseable means input")
	assert.Equal(t, TierInteract, FrameTier("/ws/terminal/a", []byte(`{"type":"anything_new"}`)), "unknown types are input")
	assert.Equal(t, TierView, FrameTier("/ws/terminal/a", []byte(`{"type":"ping"}`)))
	assert.Equal(t, TierView, FrameTier("/ws/coral", []byte(`whatever`)))
}

func TestPathRulesPrefixMustEndInSlash(t *testing.T) {
	_, err := NewPathRules([]PathRule{{Prefix: "/api/session"}}, nil)
	require.Error(t, err, "'/api/session' would match '/api/sessions-admin'")
	_, err = NewPathRules([]PathRule{{Prefix: "api/"}}, nil)
	require.Error(t, err)
	r, err := NewPathRules([]PathRule{{Prefix: "/api/sessions/"}, {Prefix: "/api/health", Exact: true}}, nil)
	require.NoError(t, err)
	assert.True(t, r.Allow("GET", "/api/sessions/x"))
	assert.False(t, r.Allow("GET", "/api/sessions-admin"))
	assert.True(t, r.Allow("GET", "/api/health"))
	// Built by hand with a bad prefix: matches nothing (fail closed).
	bad := PathRules{Rules: []PathRule{{Prefix: "/api/session"}}}
	assert.False(t, bad.Allow("GET", "/api/sessions-admin"))
	assert.False(t, bad.Allow("GET", "/api/session/x"))
}

// ── end to end: the mode gate and per-frame WebSocket gating on a real LocalHandler ─

type fakeCoral struct {
	mu     sync.Mutex
	inputs []string
	paths  []string
}

func (f *fakeCoral) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/ws/terminal/") {
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer c.CloseNow()
			_ = c.Write(r.Context(), websocket.MessageBinary, []byte("prompt$ "))
			for {
				_, b, err := c.Read(r.Context())
				if err != nil {
					return
				}
				f.mu.Lock()
				f.inputs = append(f.inputs, string(b))
				f.mu.Unlock()
			}
		}
		w.Write([]byte("ok"))
	})
}

func (f *fakeCoral) seenInputs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inputs...)
}
func (f *fakeCoral) seenPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func TestModeTableEndToEnd(t *testing.T) {
	hub := newHub(t, fastCfg())
	fc := &fakeCoral{}
	var mode atomic.Int32
	mode.Store(int32(TierView))
	cur := func() Mode { return Mode(mode.Load()) }
	sess, _ := connect(t, hub, fc.handler(), func(l *LocalHandler) {
		l.AllowRequest = AllowRequest(cur)
		l.MaxMode = cur
	})

	status := func(method, path string) int {
		req, _ := http.NewRequest(method, "http://x"+path, strings.NewReader("{}"))
		resp, err := sess.RoundTrip(req)
		if err != nil {
			return 0
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// View: reads only; relative file refs work, absolute ones never reach Coral.
	assert.Equal(t, 200, status("GET", "/api/sessions/live"))
	assert.Equal(t, 200, status("GET", "/api/sessions/live/a/file-content?filepath=main.go"))
	assert.Equal(t, 0, status("GET", "/api/sessions/live/a/file-content?filepath=/etc/passwd"))
	assert.Equal(t, 0, status("GET", "/api/sessions/live/a/file-content?filepath=%7E/.ssh/id_rsa"))
	assert.Equal(t, 0, status("POST", "/api/sessions/live/a/send"))
	assert.Equal(t, 0, status("POST", "/api/sessions/launch"))
	assert.Equal(t, 0, status("GET", "/api/settings"))
	for _, p := range fc.seenPaths() {
		assert.NotContains(t, p, "etc/passwd")
		assert.NotContains(t, p, ".ssh")
	}
	n := len(fc.seenPaths())

	mode.Store(int32(TierInteract))
	assert.Equal(t, 200, status("POST", "/api/sessions/live/a/send"))
	assert.Equal(t, 0, status("POST", "/api/sessions/launch"))
	mode.Store(int32(TierLaunch))
	assert.Equal(t, 200, status("POST", "/api/sessions/launch"))
	assert.Greater(t, len(fc.seenPaths()), n)

	// Terminal WebSocket: output is view; input needs interact, checked on every frame.
	mode.Store(int32(TierView))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, err := sess.DialWS(ctx, "/ws/terminal/a", "", nil)
	require.NoError(t, err)
	defer ws.CloseNow()
	_, out, err := ws.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, "prompt$ ", string(out), "view mode can read the terminal")

	in, _ := json.Marshal(map[string]string{"type": "terminal_input", "data": "rm -rf ~\n"})
	require.NoError(t, ws.Write(ctx, websocket.MessageText, in))
	_, _, err = ws.Read(ctx)
	require.Error(t, err, "input in view mode closes the stream")
	assert.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err))
	assert.Empty(t, fc.seenInputs(), "input never reached the local terminal")

	// Interact: input flows; dropping the mode stops it on the already-open stream.
	mode.Store(int32(TierInteract))
	ws2, err := sess.DialWS(ctx, "/ws/terminal/a", "", nil)
	require.NoError(t, err)
	defer ws2.CloseNow()
	_, _, _ = ws2.Read(ctx)
	ok, _ := json.Marshal(map[string]string{"type": "terminal_input", "data": "ls\n"})
	require.NoError(t, ws2.Write(ctx, websocket.MessageText, ok))
	require.Eventually(t, func() bool { return len(fc.seenInputs()) == 1 }, 3*time.Second, 10*time.Millisecond)

	mode.Store(int32(TierView)) // the local user lowers the mode while the stream is open
	require.NoError(t, ws2.Write(ctx, websocket.MessageText, ok))
	_, _, err = ws2.Read(ctx)
	require.Error(t, err)
	assert.Equal(t, websocket.StatusPolicyViolation, websocket.CloseStatus(err))
	time.Sleep(100 * time.Millisecond)
	assert.Len(t, fc.seenInputs(), 1, "no input after the mode dropped")
}

func TestHelloCarriesLocalModeAsAHintOnly(t *testing.T) {
	hub := newHub(t, fastCfg())
	for _, tc := range []struct {
		mode func() Mode
		want Mode
	}{{nil, TierView}, {func() Mode { return TierInteract }, TierInteract}, {func() Mode { return TierLaunch }, TierLaunch}} {
		ls := httptest.NewServer(http.NotFoundHandler())
		defer ls.Close()
		cs, err := Dial(context.Background(), hub.URL(), testToken, ClientOptions{
			SessionConfig: hub.Cfg, Handler: &LocalHandler{BaseURL: ls.URL}, CoralVersion: "t", AllowInsecure: true, LocalMode: tc.mode,
		})
		require.NoError(t, err)
		s := hub.next(t)
		assert.Equal(t, tc.want, s.LocalMode())
		cs.Close()
	}
	assert.Equal(t, TierView, parseLocalMode("garbage"))
	assert.Equal(t, TierView, parseLocalMode(""))
}
