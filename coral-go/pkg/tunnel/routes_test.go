package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		{"DELETE", "/api/board/p/messages/3", "", TierLaunch}, // #3942
		{"POST", "/api/sessions/live/a/kill", "", TierLaunch},
		{"POST", "/api/sessions/launch", "", TierLaunch},
		{"POST", "/api/sessions/live/a/open-in-editor", "", TierNever}, // local only (#3619, H4)
		{"POST", "/api/board/p/tasks/7/approve", "", TierNever},        // a reviewer's verdict (#3725)
		{"POST", "/api/board/p/tasks/7/reject", "", TierNever},
		{"POST", "/api/board/p/tasks/7/review", "", TierNever},
		{"POST", "/api/board/p/resume", "", TierLaunch},
		{"POST", "/api/sessions/live/team/t/reset", "", TierLaunch},
		{"GET", "/api/filesystem/list", "path=/Users/x", TierLaunch},
		{"GET", "/api/board/remotes", "", TierNever},
		{"DELETE", "/api/board/remotes", "", TierNever},
		{"POST", "/api/board/remotes", "", TierNever},
		{"GET", "/api/board/remotes/proxy/s/p/messages/all", "", TierNever},
		// Never: settings, secrets, host access, outbound requests, connector, agent API.
		{"GET", "/api/settings", "", TierNever},
		{"PUT", "/api/settings", "", TierNever},
		{"PUT", "/api/settings/git-diff-mode", "", TierInteract}, // the one narrow setting route (three validated values)
		{"GET", "/api/settings/git-diff-mode", "", TierNever},
		{"POST", "/api/settings/git-diff-mode", "", TierNever},
		{"DELETE", "/api/settings/git-diff-mode", "", TierNever},
		{"PUT", "/api/settings/git-diff-mode", "mode=main_head", TierNever}, // no query parameters
		{"PUT", "/api/settings/cli_path_claude", "", TierNever},
		{"PUT", "/api/settings/git-diff-mode/x", "", TierNever},
		{"GET", "/api/system/api-key", "", TierNever},
		{"DELETE", "/api/system/debug-input-log", "", TierNever}, // the terminal input log: only at the machine (#3256)
		{"GET", "/api/system/privacy", "", TierNever},
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
		{"PUT", "/api/agent/ui/x", "", TierNever}, // Agent UI reads are listed (TestAgentUIRoutes); publishing a panel is never reachable
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

// P-8 and #2788: file routes accept only the query parameters their handler reads, file
// references are decoded to a fixed point before they are judged, and unknown parameters
// are refused. The strings below are the reviewer's probes plus encoding variants.
func TestFileRefsAreNormalisedAndPerRoute(t *testing.T) {
	type routeParam struct{ path, param string }
	fileRoutes := []routeParam{
		{"/api/sessions/live/a/file-content", "filepath"}, {"/api/sessions/live/a/file-original", "filepath"},
		{"/api/sessions/live/a/resolve-path", "filepath"}, {"/api/sessions/live/a/diff", "filepath"},
		{"/api/sessions/live/a/search-files", "dir"}, {"/api/sessions/live/a/chat", "working_directory"},
	}
	bad := []string{
		"/etc/passwd", "~/.ssh/id_rsa", "~", "../../etc/passwd", "a/../../b", "..", "-rf", "C:\\Windows", "c:/x", "a\\b",
		"a\x00b", "/Users/x/.coral/api_key",
		// Reviewer's probes: Coral decodes again and strips file://, so these must die here.
		"%2Fetc%2Fpasswd", "%2fetc%2fpasswd", "%252Fetc%252Fpasswd", "%252fetc%252fpasswd",
		"file:///etc/passwd", "file%3A%2F%2F%2Fetc%2Fpasswd", "file://localhost/etc/passwd", "file%3A%2F%2Flocalhost%2Fetc%2Fpasswd",
		"%257E%252F.ssh%252Fid_rsa", "%7E%2F.ssh%2Fid_rsa", "%2e%2e%2fsecret", "%252e%252e%252fsecret", "%252E%252E/secret",
		"%25252e%25252e%25252fsecret", "%2525252e%2525252e%2525252fsecret", "a%2F..%2F..%2Fb", "http://evil.example/x",
		"data:text/plain,x", "a#b", "a?b=c", "100%", "%zz", "..\u2025/x", "\u2025\u2025/x", "a\uff0f..\uff0fb", "\uff0e\uff0e/x",
		"\ufe52\ufe52/x", "x\u2215y", "a\nb", "a\x7fb",
	}
	for _, rp := range fileRoutes {
		for _, v := range bad {
			q := url.Values{rp.param: {v}}.Encode()
			assert.Equal(t, TierNever, Classify("GET", rp.path, q), "%s ?%s", rp.path, q)
			// Pre-encoded once more, as a client could send it raw.
			assert.Equal(t, TierNever, Classify("GET", rp.path, rp.param+"="+url.QueryEscape(url.QueryEscape(v))), "%s double-encoded %q", rp.path, v)
		}
		// A second, harmless value cannot hide a bad one.
		assert.Equal(t, TierNever, Classify("GET", rp.path, rp.param+"=ok.go&"+rp.param+"=/etc/passwd"))
		for _, ok := range []string{"main.go", "src/app/main.go", "a b.txt", "src/", "dir.with.dots/x", "..hidden", "a..b", ""} {
			assert.Equal(t, TierView, Classify("GET", rp.path, url.Values{rp.param: {ok}}.Encode()), "%s %q", rp.path, ok)
		}
	}
	// Unknown parameters are never allowed, and a parameter belongs to the routes that read it.
	assert.Equal(t, TierNever, Classify("GET", "/api/sessions/live/a/file-content", "filepath=a.go&extra=1"))
	assert.Equal(t, TierNever, Classify("GET", "/api/sessions/live/a/file-content", "dir=src"), "dir is a search-files parameter")
	assert.Equal(t, TierNever, Classify("GET", "/api/sessions/live/a/file-content", "path=a.go"))
	assert.Equal(t, TierNever, Classify("GET", "/api/health", "x=1"), "a route that reads no parameters accepts none")
	assert.Equal(t, TierNever, Classify("GET", "/api/sessions/live/a/chat", "FilePath=x"), "parameter names are case sensitive")
	// working_directory on chat is a file ref (reviewer: chat?working_directory=/Users/x/other was VIEW).
	assert.Equal(t, TierNever, Classify("GET", "/api/sessions/live/a/chat", "working_directory=/Users/x/other"))
	assert.Equal(t, TierView, Classify("GET", "/api/sessions/live/a/chat", "limit=50&session_id=abc"))
	// Launch-tier folder browsing takes absolute paths, but still not traversal, URLs or nested encodings.
	assert.Equal(t, TierLaunch, Classify("GET", "/api/filesystem/list", "path=/Users/me/code"))
	assert.Equal(t, TierLaunch, Classify("GET", "/api/filesystem/is-git", url.Values{"path": {"/Users/me/my code"}}.Encode()))
	assert.Equal(t, TierLaunch, Classify("GET", "/api/filesystem/list", "path=%2FUsers%2Fme"), "an encoded absolute path decodes to the same safe path")
	assert.Equal(t, TierLaunch, Classify("GET", "/api/filesystem/list", "path="), "an empty path is the machine's home")
	for _, v := range []string{"/Users/me/../../etc", "file:///etc", "/a%252F..%252Fb", "/a\\b"} {
		assert.Equal(t, TierNever, Classify("GET", "/api/filesystem/list", url.Values{"path": {v}}.Encode()), v)
	}
	// Raw control characters in a query are refused outright.
	assert.Equal(t, TierNever, Classify("GET", "/api/sessions/live", "a=b\r\nX: y"))
}

func FuzzClassifyNeverExposesAbsoluteReads(f *testing.F) {
	for _, s := range []string{"/etc/passwd", "%2Fetc", "%252F", "file:///x", "~/x", "..%2f", "a.go", "src/b.go"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, v string) {
		q := url.Values{"filepath": {v}}.Encode()
		for _, form := range []string{q, "filepath=" + v} {
			if Classify("GET", "/api/sessions/live/a/file-content", form) == TierNever {
				continue
			}
			// Allowed: then the fixed point of its decoding must be a plain relative path.
			qq, err := url.ParseQuery(form)
			if err != nil {
				t.Fatalf("allowed an unparsable query %q", form)
			}
			for _, got := range qq["filepath"] {
				n, ok := normalizeRef(got)
				bad := !ok || strings.HasPrefix(n, "/") || strings.HasPrefix(n, "~") || strings.ContainsAny(n, ":\\")
				for _, seg := range strings.Split(n, "/") {
					if seg == ".." {
						bad = true
					}
				}
				if bad {
					t.Fatalf("allowed %q (decodes to %q)", form, n)
				}
			}
		}
	})
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

// A request that fails only because this machine's own maximum mode is too low gets a distinct
// refusal carrying that mode; unknown, never-routes and bad paths stay opaque.
func TestLocalModeRefusalIsDistinctAndNarrow(t *testing.T) {
	hub := newHub(t, fastCfg())
	mode := TierView
	sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}), func(l *LocalHandler) {
		l.AllowRequest = AllowRequest(func() Mode { return mode })
		l.MaxMode = func() Mode { return mode }
	})
	do := func(method, path string) error {
		req, _ := http.NewRequest(method, "http://x"+path, nil)
		resp, err := sess.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	var se *StreamError
	require.NoError(t, do("GET", "/api/sessions/live"))

	err := do("POST", "/api/sessions/live/a/send") // interact route, machine allows view
	require.ErrorAs(t, err, &se)
	assert.Equal(t, CodeLocalMode, se.Code)
	assert.Equal(t, "view", se.Message)

	err = do("POST", "/api/sessions/launch") // launch route
	require.ErrorAs(t, err, &se)
	assert.Equal(t, CodeLocalMode, se.Code)

	for _, p := range []struct{ m, p string }{{"GET", "/api/system/api-key"}, {"POST", "/api/webhooks"}, {"GET", "/api/nope/nothing"}} {
		err = do(p.m, p.p) // never or unlisted: opaque, whatever the mode
		require.ErrorAs(t, err, &se, p.p)
		assert.Equal(t, CodePathNotAllowed, se.Code, p.p)
	}
	err = do("GET", "/api/sessions/live/a/file-content?filepath=/etc/passwd") // unsafe query: never
	require.ErrorAs(t, err, &se)
	assert.Equal(t, CodePathNotAllowed, se.Code)

	mode = TierInteract
	require.NoError(t, do("POST", "/api/sessions/live/a/send"))
	err = do("POST", "/api/sessions/launch")
	require.ErrorAs(t, err, &se)
	assert.Equal(t, "interact", se.Message)
}

// Requests the shipped UI really sends (recorded from a real-Chrome pass through the hub, plus the handlers'
// own helpers such as queryInt(r, "limit", 50)). The per-route query table must never refuse them: a refusal is
// a dead panel for the user. If a UI change adds a parameter, tools/gen_route_queries.py needs it (EXTRA).
func TestRealUIQueriesAreNotRefused(t *testing.T) {
	for _, c := range []struct{ method, path, query string }{
		{"GET", "/api/sessions/live/001/events", "limit=50&session_id=abc"},
		{"GET", "/api/sessions/live/001/search-files", "session_id=abc&dir=.&view=explorer&offset=0"},
		{"GET", "/api/sessions/live/001/search-files", "session_id=abc&dir=src&view=explorer&offset=100"},
		{"GET", "/api/sessions/live/001/search-files", "session_id=abc&q=main"},
		{"GET", "/api/sessions/live/001/chat", "session_id=abc&agent_type=claude&after=0&limit=100&offset=0"},
		{"GET", "/api/sessions/live/001/chat", "session_id=abc&agent_type=claude&limit=200&before=900&offset=105"},
		// events_limit came from coral-hub's capacity tool, not the dashboard; the hub drops it in the
		// release that pins #3942, and the tunnel refuses it from then on (asserted below).
		{"GET", "/api/sessions/live/001/poll", "session_id=abc&agent_type=claude"},
		{"GET", "/api/sessions/live/001/tasks", "session_id=abc&limit=50"},
		{"GET", "/api/sessions/live/001/git", "session_id=abc&limit=10"},
		{"GET", "/api/sessions/live/001/history/search", "q=x&limit=10&offset=0&session_id=abc"},
		{"GET", "/api/board/proj/messages", "limit=50&subscriber_id=ops"}, // no session_id since #3743
		{"GET", "/api/board/proj/messages/all", "limit=100&offset=0&before_id=5"},
		{"GET", "/api/board/proj/tasks", "limit=50&status=open&session_id=abc"},
		{"GET", "/api/sessions/history/search", "q=x&page=1&page_size=50"},
		{"GET", "/api/scheduled/runs/recent", "limit=20"},
		{"GET", "/api/workflows/runs/recent", "limit=20&offset=0&status=failed"},
	} {
		assert.NotEqual(t, TierNever, Classify(c.method, c.path, c.query), "%s %s?%s is refused but the UI sends it", c.method, c.path, c.query)
	}
	assert.Equal(t, TierNever, Classify("GET", "/api/sessions/live/001/poll", "session_id=abc&events_limit=50"), "#3942")
	assert.Equal(t, TierNever, Classify("GET", "/api/token-usage", "team_id=t"), "#3942")
	// Path parameters are not query parameters: they must not be accepted as such.
	assert.Equal(t, TierNever, Classify("GET", "/api/board/proj/messages", "project=other"))
	assert.Equal(t, TierNever, Classify("GET", "/api/sessions/live/001/events", "name=other"))
}

func TestArtifactRouteIsExactDigestOnly(t *testing.T) {
	hex := strings.Repeat("ab12", 16) // 64 lowercase hex digits
	assert.Equal(t, TierView, Classify("GET", "/api/artifacts/"+hex, ""))
	assert.Equal(t, TierView, Classify("HEAD", "/api/artifacts/"+hex, ""))
	assert.True(t, AllowRequest(func() Mode { return TierView })("GET", "/api/artifacts/"+hex, ""), "readable in view mode")
	// The machine-wide list (#5575) is a View read with exactly its three query parameters, and a delete is Launch: both are the owner's alone, which the machine checks (hubOwnerRequest).
	assert.Equal(t, TierView, Classify("GET", "/api/artifacts", ""))
	assert.Equal(t, TierView, Classify("GET", "/api/artifacts", "limit=50&cursor=abc&board=team-1"))
	assert.Equal(t, TierNever, Classify("GET", "/api/artifacts", "q=x"), "no other parameter")
	assert.Equal(t, TierLaunch, Classify("DELETE", "/api/artifacts/"+hex, ""))
	assert.Equal(t, TierNever, Classify("DELETE", "/api/artifacts/"+hex, "force=1"), "no query on a delete")
	assert.Equal(t, TierNever, Classify("DELETE", "/api/artifacts/"+hex[:63], ""))
	assert.Equal(t, TierNever, Classify("DELETE", "/api/artifacts/"+strings.ToUpper(hex), ""))
	assert.Equal(t, TierNever, Classify("DELETE", "/api/artifacts", ""), "no delete of the list")
	assert.Equal(t, TierNever, Classify("POST", "/api/artifacts/"+hex, ""))
	assert.False(t, AllowRequest(func() Mode { return TierView })("DELETE", "/api/artifacts/"+hex, ""), "a delete needs launch mode")
	assert.False(t, AllowRequest(func() Mode { return TierInteract })("DELETE", "/api/artifacts/"+hex, ""), "a delete needs launch mode")
	assert.True(t, AllowRequest(func() Mode { return TierLaunch })("DELETE", "/api/artifacts/"+hex, ""))
	// Who can read an artifact (#5577): a View read of exactly the id, no query, owner only (the machine checks)
	assert.Equal(t, TierView, Classify("GET", "/api/artifacts/"+hex+"/readers", ""))
	assert.Equal(t, TierNever, Classify("GET", "/api/artifacts/"+hex+"/readers", "x=1"), "no query")
	assert.Equal(t, TierNever, Classify("POST", "/api/artifacts/"+hex+"/readers", ""))
	assert.Equal(t, TierNever, Classify("GET", "/api/artifacts/"+hex[:63]+"/readers", ""))
	for _, probe := range []struct{ method, path, query string }{
		{"GET", "/api/artifacts/" + hex[:63], ""},                    // 63 digits
		{"GET", "/api/artifacts/" + hex + "0", ""},                   // 65 digits
		{"GET", "/api/artifacts/" + strings.ToUpper(hex), ""},        // upper case
		{"GET", "/api/artifacts/" + hex + "/", ""},                   // trailing slash
		{"GET", "/api/artifacts/" + hex + ".json", ""},               // metadata sidecar suffix
		{"GET", "/api/artifacts/" + hex + "/..", ""},                 // %2f.. after decoding
		{"GET", "/api/artifacts/" + hex + "/x", ""},                  // extra segment
		{"GET", "/api/artifacts/" + hex[:32] + "%2f" + hex[32:], ""}, // encoded slash left in the segment
		{"GET", "/api/artifacts/" + hex[:63] + "g", ""},              // not hex
		{"GET", "/api/artifacts/" + hex[:63] + "١", ""},              // look-alike digit
		{"GET", "/api/artifacts/" + hex[:62] + "\x00a", ""},          // NUL
		{"GET", "/api/artifacts/", ""},                               // empty id
		{"GET", "/api/artifacts/x", ""},                              // wildcard-looking id
		{"GET", "/api/artifacts/*", ""},                              //
		{"GET", "/api/artifacts/" + hex, "download=1"},               // no query parameters at all
		{"GET", "/api/artifacts/" + hex, "x"},                        //
		{"POST", "/api/artifacts/" + hex, ""},                        // writes
		{"PUT", "/api/artifacts/" + hex, ""},                         //
		{"POST", "/api/artifacts", ""},                               // upload is not part of this change
		{"POST", "/api/agent/artifacts", ""},                         // the agent upload route
		{"GET", "/api/agent/artifacts", ""},                          //
		{"GET", "/api/artifacts/" + hex + "/../../agent/ui", ""},     //
		{"GET", "/api//artifacts/" + hex, ""},                        // empty segment
		{"GET", "/api/artifacts/./" + hex, ""},                       // dot segment
		{"GET", "/api/artifacts/" + hex[:32] + "\\" + hex[32:], ""},  // backslash
	} {
		assert.Equal(t, TierNever, Classify(probe.method, probe.path, probe.query), "%s %q ?%s must not be reachable", probe.method, probe.path, probe.query)
	}
}

// Agent UI on the hub: panels are listed and read at View, interactions and requests need Interact, and publishing or removing a
// panel (PUT/DELETE) is never reachable. The panel id is exactly the machine's shape.
func TestAgentUIRoutes(t *testing.T) {
	id := "panel_1-A"
	for _, tc := range []struct {
		method, path, query string
		want                Tier
	}{
		{"GET", "/api/agent/ui", "session_id=s1", TierView},
		{"GET", "/api/agent/ui/" + id + "/content", "session_id=s1&revision=3", TierView},
		{"GET", "/api/agent/ui/" + id + "/events", "session_id=s1&after=12", TierView},
		{"HEAD", "/api/agent/ui", "session_id=s1", TierView},
		{"POST", "/api/agent/ui/" + id + "/events", "session_id=s1", TierInteract},
		{"POST", "/api/agent/ui-request", "session_id=s1", TierInteract},
		// publishing and removing stay with the agent on its machine
		{"PUT", "/api/agent/ui/" + id, "session_id=s1", TierNever},
		{"DELETE", "/api/agent/ui/" + id, "session_id=s1", TierNever},
		{"PUT", "/api/agent/ui", "", TierNever},
		{"POST", "/api/agent/ui", "", TierNever},
		{"DELETE", "/api/agent/ui", "", TierNever},
		{"PUT", "/api/agent/ui/" + id + "/content", "", TierNever},
		{"POST", "/api/agent/ui/" + id + "/content", "", TierNever},
		{"DELETE", "/api/agent/ui/" + id + "/events", "", TierNever},
		{"PUT", "/api/agent/ui/" + id + "/events", "", TierNever},
		{"GET", "/api/agent/ui-request", "", TierNever},
		{"PUT", "/api/agent/ui-request", "", TierNever},
		// shapes and parameters
		{"GET", "/api/agent/ui", "", TierView},                   // the machine decides what a missing session means
		{"GET", "/api/agent/ui", "session_id=s1&x=1", TierNever}, // unlisted parameter
		{"GET", "/api/agent/ui/" + id + "/content", "revision=1&evil=1", TierNever},
		{"GET", "/api/agent/ui/" + id + "/events", "after=1&limit=5", TierNever},
		{"POST", "/api/agent/ui/" + id + "/events", "after=1", TierNever},
		{"GET", "/api/agent/ui/", "", TierNever},
		{"GET", "/api/agent/ui/" + id, "", TierNever}, // no bare panel read: only content, events
		{"GET", "/api/agent/ui/" + id + "/content/x", "", TierNever},
		{"GET", "/api/agent/ui/" + id + "/other", "", TierNever},
		{"GET", "/api/agent/ui/" + strings.Repeat("a", 65) + "/content", "", TierNever}, // longer than the machine's 64
		{"GET", "/api/agent/ui/" + strings.Repeat("a", 64) + "/content", "", TierView},
		{"GET", "/api/agent/ui/a.b/content", "", TierNever},
		{"GET", "/api/agent/ui/a%2fb/content", "", TierNever},
		{"GET", "/api/agent/ui/a b/content", "", TierNever},
		{"GET", "/api/agent/ui/a\\b/content", "", TierNever},
		{"GET", "/api/agent/ui/../history/content", "", TierNever},
		{"GET", "/api/agent/ui/\u00e9/content", "", TierNever},
		{"GET", "/api/agent/ui//content", "", TierNever},
		// the rest of /api/agent/* is the machine's own agent API and stays unreachable
		{"POST", "/api/agent/artifacts", "", TierNever},
		{"GET", "/api/agent/tasks", "", TierNever},
		{"GET", "/api/agent/history/search", "", TierNever},
		{"POST", "/api/agent/ui-x", "", TierNever},
	} {
		assert.Equal(t, tc.want, Classify(tc.method, tc.path, tc.query), "%s %s ?%s", tc.method, tc.path, tc.query)
	}
	assert.True(t, AllowRequest(func() Mode { return TierView })("GET", "/api/agent/ui/"+id+"/content", "session_id=s1"), "a viewer can read a panel")
	assert.False(t, AllowRequest(func() Mode { return TierView })("POST", "/api/agent/ui/"+id+"/events", "session_id=s1"), "but not interact with one")
	assert.True(t, AllowRequest(func() Mode { return TierInteract })("POST", "/api/agent/ui-request", "session_id=s1"))
}

// The hub's machine feed connects with feed=hub (a client that must not consume the machine's UI notifications). Exactly that value,
// once, on exactly that route: look-alikes, other values, repeats and the parameter anywhere else are refused.
func TestFeedHubIsAnExactValueOnTheCoralFeedOnly(t *testing.T) {
	for _, q := range []string{"feed=hub", "feed=hub&session_id=abc", "agent_type=claude&feed=hub", "feed=%68ub" /* decodes to exactly hub, as Coral reads it */} {
		assert.Equal(t, TierView, Classify("GET", "/ws/coral", q), q)
	}
	for _, q := range []string{
		"feed=", "feed", "feed=Hub", "feed=HUB", "feed=hub2", "feed=hubb", "feed=hu", "feed=%68ubx", "feed=hub%00", "feed=hub+", "feed= hub",
		"feed=hub&feed=hub", "feed=hub&feed=x", "feed=x&feed=hub", "feed=hub;x=1", "Feed=hub", "feed[]=hub", "feeds=hub", "feed=hub&bogus=1",
	} {
		assert.Equal(t, TierNever, Classify("GET", "/ws/coral", q), q)
	}
	for _, p := range []string{"/ws/terminal/a", "/api/sessions/live", "/api/health", "/api/agent/ui"} {
		assert.Equal(t, TierNever, Classify("GET", p, "feed=hub"), p)
	}
	assert.Equal(t, TierView, Classify("GET", "/ws/coral", ""), "no query still works (older hubs)")
	// the table text is what the Go code and the generator agree on
	assert.Contains(t, routeQueries["GET /ws/coral"], "feed=hub")
}

// Since v1.4.12 (#3168) every artifact link is /api/artifacts/<digest>?v=2. Through the tunnel exactly that passes, as does
// the bare link; any other value, a repeat or another parameter is refused (#3179).
func TestArtifactLinkVersionIsAnExactValue(t *testing.T) {
	hex := strings.Repeat("ab", 32)
	p := "/api/artifacts/" + hex
	for _, q := range []string{"", "v=2", "v=%32" /* decodes to exactly 2 */} {
		assert.Equal(t, TierView, Classify("GET", p, q), q)
		assert.True(t, AllowRequest(func() Mode { return TierView })("GET", p, q), "readable in view mode: %q", q)
	}
	for _, q := range []string{"v=3", "v=", "v", "v=02", "v=2&v=2", "v=2&x=1", "x=1", "V=2", "v=2;x", "v=2%00", "download=1"} {
		assert.Equal(t, TierNever, Classify("GET", p, q), q)
	}
	assert.Equal(t, "v=2", routeQueries["GET /api/artifacts/{}"])
}

// #3433: deleting a whole board can't be undone, so through Coral Hub it needs Launch; its
// smaller board writes stay at Interact.
func TestDeletingABoardNeedsLaunch(t *testing.T) {
	if got := Classify("DELETE", "/api/board/team", ""); got != TierLaunch {
		t.Fatalf("DELETE /api/board/team: %v, want launch", got)
	}
	if got := Classify("DELETE", "/api/board/team/reminder", ""); got != TierInteract {
		t.Errorf("DELETE reminder: %v, want interact", got)
	}
	allow := AllowRequest(func() Mode { return TierInteract })
	if allow("DELETE", "/api/board/team", "") {
		t.Error("a board delete is allowed at Interact")
	}
	if !AllowRequest(func() Mode { return TierLaunch })("DELETE", "/api/board/team", "") {
		t.Error("a board delete is refused at Launch")
	}
	// Killing a whole team is Launch, and so is one agent's kill (#4274, coral-hub's tiers).
	if got := Classify("POST", "/api/sessions/live/team/alpha/kill", ""); got != TierLaunch {
		t.Errorf("team kill: %v, want launch", got)
	}
	if got := Classify("POST", "/api/sessions/live/agent-1/kill", ""); got != TierLaunch {
		t.Errorf("single kill: %v, want launch", got)
	}
}

// #3942 (coral-hub's tiers, Security's #3901 item 2): sleeping a team or every agent, pausing
// a board and deleting a board message are Launch, like what undoes them; the agents' own
// board calls are never reachable through the tunnel. The dashboard's subscribe, resolve-path and
// a session's resolve keep their tiers (one agent's sleep moved to Launch in #4274).
func TestHubTierMoves3942(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         Tier
	}{
		{"POST", "/api/sessions/live/team/alpha/sleep", TierLaunch},
		{"POST", "/api/sessions/live/sleep-all", TierLaunch},
		{"POST", "/api/board/team/pause", TierLaunch},
		{"DELETE", "/api/board/team/messages/7", TierLaunch},
		{"POST", "/api/board/team/resume", TierLaunch}, // the undo, already Launch

		{"POST", "/api/board/team/messages/mark-read", TierNever},
		{"POST", "/api/board/team/tasks/claim", TierNever},
		{"POST", "/api/board/team/tasks/current", TierNever},
		{"POST", "/api/board/team/tasks/9/submit-review", TierNever},
		{"POST", "/api/board/team/tasks/9/release-review", TierNever},
		{"GET", "/api/board/team/waits", TierNever},
		{"GET", "/api/board/team/waits/poll", TierNever},
		{"POST", "/api/board/team/waits", TierNever},
		{"DELETE", "/api/board/team/waits", TierNever},
		{"GET", "/api/board/team/groups", TierNever},
		{"GET", "/api/board/team/groups/g/members", TierNever},
		{"POST", "/api/board/team/groups/g/members", TierNever},
		{"DELETE", "/api/board/team/groups/g/members/s", TierNever},
		{"GET", "/api/board/team/messages/check", TierNever},
		{"GET", "/api/board/team/peek", TierNever},
		{"GET", "/api/sessions/resolve", TierNever},
		{"DELETE", "/api/board/team/subscribe", TierNever},

		{"POST", "/api/board/team/subscribe", TierInteract},
		{"POST", "/api/board/team/messages", TierInteract},
		{"GET", "/api/sessions/live/agent-1/resolve-path", TierView},
		{"GET", "/api/sessions/0b1e2c3d-0000-4000-8000-000000000001/resolve", TierView},
	} {
		assert.Equal(t, c.want, Classify(c.method, c.path, ""), "%s %s", c.method, c.path)
	}
}

// #3942: the generator's question, "does the tunnel let this route shape through at all?".
func TestShapeAllowed(t *testing.T) {
	for shape, want := range map[string]bool{
		"GET /api/sessions/live/{}/poll":                  true,
		"GET /api/artifacts/{}":                           true, // a {hex64} segment
		"DELETE /api/board/{}/messages/{}":                true, // Launch
		"GET /api/board/{}/peek":                          false,
		"GET /api/sessions/resolve":                       false,
		"GET /api/agent/history/search":                   false,
		"GET /api/board/remotes/proxy/{}/{}/messages/all": false,
		"GET /api/system/status":                          false,
	} {
		method, path, _ := strings.Cut(shape, " ")
		assert.Equal(t, want, ShapeAllowed(method, path), shape)
	}
}

// #4274 (agreed with coral-hub, Security's #3901 items 2 and 3): one agent's sleep and kill are
// Launch like the team's and like what undoes them; nine routes Coral's own dashboard never calls are
// no longer reachable; and the routes coral-hub relies on keep their tiers.
func TestHubTierChanges4274(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         Tier
	}{
		{"POST", "/api/sessions/live/agent-1/sleep", TierLaunch},
		{"POST", "/api/sessions/live/agent-1/kill", TierLaunch},
		{"POST", "/api/sessions/live/agent-1/wake", TierLaunch}, // the undo
		{"POST", "/api/sessions/live/agent-1/archive", TierInteract},

		{"POST", "/api/board/team/tasks/9/reassign", TierNever},
		{"POST", "/api/board/team/tasks/9/snooze", TierNever},
		{"PUT", "/api/board/team/working-mode/presets/p", TierNever},
		{"POST", "/api/board/team/working-mode/presets/p/reset", TierNever},
		{"POST", "/api/sessions/live/agent-1/tasks/reorder", TierNever},
		{"GET", "/api/tasks/runs", TierNever},
		{"GET", "/api/tasks/runs/7", TierNever},
		{"POST", "/api/tasks/runs/7/kill", TierNever},
		{"GET", "/api/board/team/tasks/9/changes.diff", TierNever},

		// What coral-hub relies on stays as it was.
		{"GET", "/api/sessions/live/agent-1/changes.diff", TierView},
		{"GET", "/api/sessions/live/agent-1/diff", TierView},
		{"GET", "/api/sessions/live/agent-1/capture", TierView},
		{"GET", "/api/sessions/live/agent-1/file-content", TierView},
		{"GET", "/api/sessions/live/agent-1/file-original", TierView},
		{"GET", "/api/sessions/0b1e2c3d-0000-4000-8000-000000000001/changes.diff", TierView},
		{"GET", "/api/tasks/active", TierView},
		{"POST", "/api/board/team/tasks/9/nudge", TierInteract},
		{"POST", "/api/board/team/tasks/9/complete", TierInteract},
	} {
		assert.Equal(t, c.want, Classify(c.method, c.path, ""), "%s %s", c.method, c.path)
	}
}

// Browse through Coral Hub shows the home as "~" and asks for "~/folder" (#4527): the machine takes that form back on the two
// routes that show it, with the same checks as any file reference on the relative part, and nowhere else.
func TestHomeRelativeBrowsePath(t *testing.T) {
	q := func(v string) string { return url.Values{"path": {v}}.Encode() }
	for _, route := range []string{"/api/filesystem/list", "/api/filesystem/is-git"} {
		for _, ok := range []string{"~", "~/", "~/code", "~/code/my%20project", "~/a/b/c/d/e", "~/.config-free/x", "%7E", "~%2Fcode", "~/code%2Fsub"} {
			assert.Equal(t, TierLaunch, Classify("GET", route, "path="+ok), "%s %s", route, ok)
		}
		for _, bad := range []string{
			"~user", "~user/code", "~root", "~/../x", "~/a/../../x", "~//etc", "~/%2e%2e/x", "~/a%252F..%252Fb", "~/a\\b", "~/~x",
			"~/file:///etc", "~/a:b", "~/\x00x", "~/a\nb", "~/-x", "~~", "~ /x", "~\\x", "~/a#b", "~/a?b",
			"~/\u2024/x", "~/%25", "~%2F..%2Fx",
		} {
			assert.Equal(t, TierNever, Classify("GET", route, q(bad)), "%s %q", route, bad)
		}
	}
	// Only the path parameter of those two routes: a file reference, a directory or a working directory elsewhere is refused with "~".
	for _, c := range []struct{ route, query string }{
		{"/api/sessions/live/a/file-content", "filepath=~/x"},
		{"/api/sessions/live/a/search-files", "dir=~"},
		{"/api/sessions/live/a/chat", "working_directory=~/x"},
		{"/api/sessions/live/a/diff", "filepath=~"},
	} {
		assert.Equal(t, TierNever, Classify("GET", c.route, c.query), c.route+"?"+c.query)
	}
	// The other parameters of the two routes are not path parameters.
	assert.Equal(t, TierNever, Classify("GET", "/api/filesystem/list", "path=~&other=~"))
}

// #5035: the Retry-After a hub repeats is whole seconds from 1 to a minute; a date, text, zero, a negative or a huge number is dropped or capped.
func TestCleanRetryAfter(t *testing.T) {
	for in, want := range map[string]string{"1": "1", " 7 ": "7", "60": "60", "61": "60", "3600": "60", "999999999999": "60", "99999999999999999999999": "", "0": "", "-5": "", "": "", "soon": "",
		"Wed, 21 Oct 2026 07:28:00 GMT": "", "5 seconds": "", "5\r\nSet-Cookie: x=y": ""} {
		if got := CleanRetryAfter(in); got != want {
			t.Errorf("CleanRetryAfter(%q) = %q, want %q", in, got, want)
		}
	}
}

// #5035: GET /api/agents/models (the model chip's list) is a Launch route that accepts only its type parameter.
func TestAgentModelsListIsALaunchRouteWithOnlyType(t *testing.T) {
	_, tier, ok := RuleFor("GET", "/api/agents/models")
	if !ok || tier != TierLaunch {
		t.Fatalf("GET /api/agents/models: ok=%v tier=%v, want Launch", ok, tier)
	}
	if _, _, ok := RuleFor("POST", "/api/agents/models/refresh"); ok {
		t.Fatal("the refresh route must stay local")
	}
	if got := routeQueries["GET /api/agents/models"]; got != "type" {
		t.Fatalf("query parameters = %q, want only type", got)
	}
}

// #5035 end to end: the local Coral refuses a terminal upgrade with 429 and a Retry-After; the hub's DialWS error carries the status and the
// cleaned Retry-After (so the hub proxy can answer "busy" and say when), and a hostile Retry-After is dropped.
func TestRefused429UpgradeCarriesItsRetryAfterToTheHub(t *testing.T) {
	for _, c := range []struct{ header, want string }{{"7", "7"}, {"3600", "60"}, {"soon", ""}, {"", ""}} {
		hub := newHub(t, fastCfg())
		sess, _ := connect(t, hub, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c.header != "" {
				w.Header().Set("Retry-After", c.header)
			}
			http.Error(w, "too many terminal connections are open", http.StatusTooManyRequests)
		}), nil)
		_, err := sess.DialWS(context.Background(), "/ws/terminal/a", "", nil)
		var se *StreamError
		if !errors.As(err, &se) {
			t.Fatalf("Retry-After %q: want a StreamError, got %v", c.header, err)
		}
		if se.Code != CodeUpstream || se.Status != http.StatusTooManyRequests || se.RetryAfter != c.want {
			t.Fatalf("Retry-After %q: StreamError = %+v, want code %s, status 429, retry %q", c.header, se, CodeUpstream, c.want)
		}
	}
}

// The comments on an artifact through the hub (#5176, #7052): reading is GET (and HEAD) at view with the two queries its handler reads. A comment or reply, a resolve and a reopen are the owner's alone at interact (a
// hub session at view never gets them). Editing, deleting, sending a thread to an agent, the lock and an agent's reply are refused whatever the mode, and a bad id or path is not a comments route.
func TestArtifactCommentRoutesThroughTheHub(t *testing.T) {
	hex := strings.Repeat("ab", 32)
	base := "/api/artifacts/" + hex + "/comments"
	assert.Equal(t, TierView, Classify("GET", base, ""))
	assert.Equal(t, TierView, Classify("GET", base, "state=open&board=team-a"))
	assert.Equal(t, TierView, Classify("HEAD", base, "state=resolved"))
	assert.Equal(t, TierNever, Classify("GET", base, "state=open&evil=1"), "a query the handler does not read")
	assert.Equal(t, TierNever, Classify("GET", base, "author=x"))
	for _, c := range []struct{ method, path string }{{"POST", base}, {"POST", base + "/7/resolve"}, {"POST", base + "/7/reopen"}} {
		assert.Equal(t, TierInteract, Classify(c.method, c.path, ""), c.method+" "+c.path)
		assert.False(t, AllowRequest(func() Mode { return TierView })(c.method, c.path, ""), c.method+" "+c.path+" at view")
		for _, mode := range []Tier{TierInteract, TierLaunch} {
			assert.True(t, AllowRequest(func() Mode { return mode })(c.method, c.path, ""), c.method+" "+c.path+" at "+mode.String())
		}
		_, m := OwnerRoute(c.method, c.path)
		assert.Equal(t, OwnerRefuse, m, c.method+" "+c.path+" is the owner's alone")
	}
	for _, c := range []struct{ method, path string }{
		{"PATCH", base + "/7"}, {"DELETE", base + "/7"}, {"POST", base + "/7/send"}, {"POST", base + "/7/reply"}, {"PUT", base + "/lock"}, {"DELETE", base + "/lock"},
		{"PUT", base}, {"POST", base + "/lock"},
	} {
		for _, mode := range []Tier{TierView, TierInteract, TierLaunch} {
			assert.False(t, AllowRequest(func() Mode { return mode })(c.method, c.path, ""), c.method+" "+c.path+" at "+mode.String())
		}
		assert.Equal(t, TierNever, Classify(c.method, c.path, ""), c.method+" "+c.path)
	}
	for _, bad := range []string{"/api/artifacts/" + hex[:63] + "/comments", "/api/artifacts/" + strings.ToUpper(hex) + "/comments", base + "/", base + "/7", "/api/artifacts/" + hex + "/comments/7/extra/more"} {
		assert.Equal(t, TierNever, Classify("GET", bad, ""), bad+" is not a comments route")
	}
	for _, bad := range []string{"/api/artifacts/" + hex[:63] + "/comments", "/api/artifacts/" + strings.ToUpper(hex) + "/comments"} {
		assert.Equal(t, TierNever, Classify("POST", bad, ""), "POST "+bad+" is not a comments route")
	}
}
