package hubproxy

// Real-Chrome check of the 4429 terminal-refusal path (#3663): a browser that opens one too many
// terminals through Coral Hub must see the WebSocket close with code 4429 and one of the two fixed
// busy texts — not 1006 with an empty reason. The Go websocket client tolerates a second close
// frame, so the earlier Go test (ws_busy_test.go) could not catch the double-close bug that real
// Chrome reports as "Close received after close". This test drives headless Chrome over raw CDP
// (no CDP dependency; the transport is the websocket lib already in the tree), direct and through
// a forwarded hop.
//
// It is skipped unless CORAL_CHROME_TEST=1 and a Chrome binary is found, so it never runs in a
// plain `go test ./...`; run it with CORAL_CHROME_TEST=1 go test ./pkg/hubproxy -run Chrome4429.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cdknorow/coral/pkg/tunnel"
	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chromePath() string {
	if p := os.Getenv("CORAL_CHROME"); p != "" {
		return p
	}
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome", "/usr/bin/chromium", "/usr/bin/chromium-browser",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// busyMachine stands up a local WS "machine" (one that just holds the socket) behind a tunnel
// connector whose long-pool is 1, and a hub in front that also serves a tiny page at /. The page
// and the hub share an origin, so the browser's WebSocket Origin is accepted. It returns the hub.
func busyMachineHub(t *testing.T, extraOrigins ...string) *httptest.Server {
	t.Helper()
	// The "machine": a local API that accepts any /ws/terminal... socket and holds it open, so the
	// first terminal takes the connector's one long slot and the second is refused.
	machine := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		c.Read(r.Context())
	})
	sess := connectLocal(t, machine, func(lh *tunnel.LocalHandler) {
		lh.HubLimits = tunnel.HubLimits{Long: 1}
	})
	hub := httptest.NewUnstartedServer(nil)
	opts := Options{AllowedOrigins: append([]string{hub.Listener.Addr().String()}, extraOrigins...)}
	r := chi.NewRouter()
	r.Get("/page", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, busyPageHTML)
	})
	r.Handle("/api/remote/{server}/*", NewWithOptions(tunnelResolver{sess}, opts))
	hub.Config.Handler = r
	hub.Start()
	t.Cleanup(hub.Close)
	return hub
}

// busyPageHTML opens the terminal WS twice (same origin as the page). The first takes the one long
// slot; the second is refused. It records the second close's code and reason on window.
const busyPageHTML = `<!doctype html><meta charset=utf-8><script>
window.__done = false;
function term(){ return location.origin.replace(/^http/, 'ws') + '/api/remote/srv/ws/terminal/a'; }
const a = new WebSocket(term());
a.onopen = () => {
  const b = new WebSocket(term());
  b.onopen = () => { window.__code = -1; window.__reason = 'second opened, not refused'; window.__done = true; };
  b.onclose = (e) => { window.__code = e.code; window.__reason = e.reason; window.__done = true; };
};
a.onclose = (e) => { if (!window.__done) { window.__code = -2; window.__reason = 'first closed: ' + e.code; window.__done = true; } };
</script>`

// --- a tiny raw-CDP client over the websocket lib (no CDP dependency) ---

type cdp struct {
	ws  *websocket.Conn
	id  int
	ctx context.Context
}

func launchChrome(t *testing.T) (*cdp, func()) {
	t.Helper()
	bin := chromePath()
	// Not t.TempDir: Chrome's helper processes can still be writing the profile for a moment after
	// the browser is killed, and t.TempDir fails the test when its RemoveAll does. Removed best-effort
	// once the test is done, after a short wait for those helpers.
	profile, err := os.MkdirTemp("", "coral-chrome4429-")
	require.NoError(t, err)
	t.Cleanup(func() {
		for i := 0; i < 20; i++ {
			if os.RemoveAll(profile) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	// A free debug port: let Chrome pick it and read DevToolsActivePort.
	cmd := exec.Command(bin, "--headless=new", "--remote-debugging-port=0", "--no-first-run",
		"--no-default-browser-check", "--disable-gpu", "--user-data-dir="+profile, "about:blank")
	require.NoError(t, cmd.Start())
	stop := func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }
	// DevToolsActivePort file: first line is the port.
	var port string
	portFile := filepath.Join(profile, "DevToolsActivePort")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(portFile); err == nil {
			if nl := strings.IndexByte(string(b), '\n'); nl > 0 {
				port = strings.TrimSpace(string(b)[:nl])
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if port == "" {
		stop()
		t.Fatal("chrome did not report a debug port")
	}
	// The browser's page target websocket.
	resp, err := http.Get("http://127.0.0.1:" + port + "/json/new")
	if err != nil || resp.StatusCode != 200 {
		// Older Chrome: GET /json/new is a PUT; fall back to /json/list's first page.
		resp, err = http.Get("http://127.0.0.1:" + port + "/json/list")
	}
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var targets []struct {
		Type, WebSocketDebuggerURL string `json:"-"`
	}
	var raw []map[string]any
	wsURL := ""
	if json.Unmarshal(body, &raw) == nil { // /json/list -> array
		for _, m := range raw {
			if m["type"] == "page" {
				if u, ok := m["webSocketDebuggerUrl"].(string); ok {
					wsURL = u
					break
				}
			}
		}
	} else {
		var one map[string]any // /json/new -> object
		_ = json.Unmarshal(body, &one)
		if u, ok := one["webSocketDebuggerUrl"].(string); ok {
			wsURL = u
		}
	}
	_ = targets
	if wsURL == "" {
		stop()
		t.Fatalf("no page target: %s", string(body))
	}
	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		stop()
		t.Fatalf("cdp dial: %v", err)
	}
	c.SetReadLimit(8 << 20)
	return &cdp{ws: c, ctx: ctx}, func() { c.CloseNow(); stop() }
}

// send issues one CDP command and waits for its matching reply (ignoring events).
func (c *cdp) send(t *testing.T, method string, params map[string]any) map[string]any {
	t.Helper()
	c.id++
	id := c.id
	msg, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	require.NoError(t, c.ws.Write(c.ctx, websocket.MessageText, msg))
	dl := time.Now().Add(12 * time.Second)
	for time.Now().Before(dl) {
		_, data, err := c.ws.Read(c.ctx)
		require.NoError(t, err)
		var m map[string]any
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if fid, ok := m["id"].(float64); ok && int(fid) == id {
			return m
		}
	}
	t.Fatalf("cdp %s: no reply", method)
	return nil
}

// eval runs expression in the page and returns the JSON value (awaiting a promise if any).
func (c *cdp) eval(t *testing.T, expr string) any {
	t.Helper()
	r := c.send(t, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true})
	res, _ := r["result"].(map[string]any)
	inner, _ := res["result"].(map[string]any)
	return inner["value"]
}

// browserClose opens pageURL in Chrome and returns the second terminal's close {code, reason}.
func browserClose(t *testing.T, pageURL string) (int, string) {
	t.Helper()
	c, stop := launchChrome(t)
	defer stop()
	c.send(t, "Page.enable", nil)
	c.send(t, "Page.navigate", map[string]any{"url": pageURL})
	dl := time.Now().Add(20 * time.Second)
	for time.Now().Before(dl) {
		if done, _ := c.eval(t, "window.__done === true").(bool); done {
			code, _ := c.eval(t, "window.__code").(float64)
			reason, _ := c.eval(t, "window.__reason").(string)
			return int(code), reason
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("page did not report a terminal close in time")
	return 0, ""
}

func skipUnlessChrome(t *testing.T) {
	if os.Getenv("CORAL_CHROME_TEST") != "1" {
		t.Skip("set CORAL_CHROME_TEST=1 to run the real-Chrome 4429 test")
	}
	if chromePath() == "" {
		t.Skip("no Chrome binary found")
	}
	if runtime.GOOS == "windows" {
		t.Skip("chrome launch flags differ on windows; run on the mac/linux dev box")
	}
}

// TestChrome4429Direct: through the hub directly, the refused terminal reaches the browser as a
// 4429 close with one of the two fixed busy texts (not 1006/empty).
func TestChrome4429Direct(t *testing.T) {
	skipUnlessChrome(t)
	hub := busyMachineHub(t)
	code, reason := browserClose(t, hub.URL+"/page")
	assert.Equal(t, 4429, code, "the browser must see 4429, not 1006 (reason=%q)", reason)
	assert.Contains(t, []string{busyUserText, busyMachineText}, reason, "one of the two fixed texts")
}

// TestChrome4429ForwardedHop: the same, with a plain reverse proxy (a forwarded hop) in front of
// the hub, so a chained proxy doesn't lose the 4429 close frame or its reason.
func TestChrome4429ForwardedHop(t *testing.T) {
	skipUnlessChrome(t)
	// Pre-bind the front proxy so its address can be allowed as an Origin on the hub, then put a
	// reverse proxy in front of the hub. The page and its terminal WS are same-origin with the
	// front; the front proxies both (page + the WS upgrade) to the hub.
	front := httptest.NewUnstartedServer(nil)
	hub := busyMachineHub(t, front.Listener.Addr().String())
	hubURL, _ := url.Parse(hub.URL)
	front.Config.Handler = httputil.NewSingleHostReverseProxy(hubURL)
	front.Start()
	t.Cleanup(front.Close)
	code, reason := browserClose(t, front.URL+"/page")
	assert.Equal(t, 4429, code, "forwarded hop must still deliver 4429, not 1006 (reason=%q)", reason)
	assert.Contains(t, []string{busyUserText, busyMachineText}, reason)
}
