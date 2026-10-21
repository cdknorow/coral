package tunnel

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/secrets"
	"github.com/subgentic/coral/pkg/redact"
)

// #3958 (the operator's decision; Security's conditions a-f, the #3964 audit): the viewer's tier
// is read exactly, anything else is View.
func TestParseHubTier(t *testing.T) {
	for in, want := range map[string]Tier{
		"view": TierView, "interact": TierInteract, "launch": TierLaunch,
		"": TierView, "Launch": TierView, "LAUNCH": TierView, " launch": TierView, "launch ": TierView,
		"view,launch": TierView, "admin": TierView, "never": TierView,
	} {
		assert.Equal(t, want, viewerTier(Header{HubTier: in}), "%q", in)
	}
	ctx := WithHubTier(context.Background(), "Launch")
	assert.Empty(t, hubTierFrom(ctx), "the hub side ignores an inexact value too")
	assert.Equal(t, "interact", hubTierFrom(WithHubTier(context.Background(), "interact")))
}

// Redacted for a viewer who can only look: the lower of the viewer's tier and the computer's mode
// is View, and the owner's switch is on. Read per call, so a mode lowered mid-stream counts.
func TestRedactForTable(t *testing.T) {
	var mode atomic.Int32
	var on atomic.Bool
	l := &LocalHandler{MaxMode: func() Mode { return Mode(mode.Load()) }, RedactTokens: on.Load}
	for _, c := range []struct {
		viewer, mode Tier
		switchOn     bool
		want         bool
	}{
		{TierView, TierLaunch, true, true},
		{TierInteract, TierLaunch, true, false},
		{TierLaunch, TierLaunch, true, false},
		{TierLaunch, TierInteract, true, false},
		{TierLaunch, TierView, true, true}, // a tier above the computer's mode is capped
		{TierInteract, TierView, true, true},
		{TierView, TierLaunch, false, false}, // the owner's switch off: nothing is redacted
		{TierNever, TierLaunch, true, true},  // not a tier a viewer can have: as View
	} {
		mode.Store(int32(c.mode))
		on.Store(c.switchOn)
		assert.Equal(t, c.want, l.redactFor(c.viewer)(), "%+v", c)
	}
	f := l.redactFor(TierInteract)
	on.Store(true)
	mode.Store(int32(TierLaunch))
	assert.False(t, f())
	mode.Store(int32(TierView))
	assert.True(t, f(), "the computer's mode lowered: redacted from the next call on")
}

// Through a real hub session: a reply route and the terminal are raw for Interact and Launch,
// redacted for View and for a stream with no tier; a browser's X-Coral-Hub-Tier (any spelling)
// changes nothing and never reaches the Coral server; lowering the computer's mode redacts the
// open stream from its next frame; and the tier never opens a route (Never stays refused).
func TestHubTierThroughTheTunnel(t *testing.T) {
	tok := fake("ghp_", 36, alnum)
	var mu sync.Mutex
	var seen []http.Header
	frames := make(chan string, 4)
	local := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/ws/terminal/") {
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer c.CloseNow()
			for f := range frames {
				if c.Write(r.Context(), websocket.MessageBinary, []byte(f)) != nil {
					return
				}
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"output":"token `+tok+` here"}`)
	})
	var mode atomic.Int32
	mode.Store(int32(TierLaunch))
	cur := func() Mode { return Mode(mode.Load()) }
	hub := newHub(t, fastCfg())
	sess, _ := connect(t, hub, local, func(l *LocalHandler) {
		l.Redactor = secrets.Redactor()
		l.MaxMode = cur
		l.AllowPath = nil
		l.AllowRequest = AllowRequest(cur)
	})
	get := func(tier string, path string, hdr http.Header) (int, string) {
		t.Helper()
		ctx := context.Background()
		if tier != "" {
			ctx = WithHubTier(ctx, tier)
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://x"+path, nil)
		for k, vs := range hdr {
			req.Header[k] = vs
		}
		resp, err := sess.RoundTrip(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	const capture = "/api/sessions/live/a/capture"
	for tier, raw := range map[string]bool{"": false, "view": false, "interact": true, "launch": true} {
		code, body := get(tier, capture, nil)
		require.Equal(t, 200, code, tier)
		assert.Equal(t, raw, strings.Contains(body, tok), "tier %q: raw %v: %s", tier, raw, body)
		if !raw {
			assert.Contains(t, body, redact.Marker, tier)
		}
	}
	// A browser's copy of the header, in two spellings, is not a tier.
	_, body := get("view", capture, http.Header{"X-Coral-Hub-Tier": {"launch"}, "x-coral-hub-tier": {"launch"}})
	assert.NotContains(t, body, tok, "the header is never read; the stream's field is View")
	mu.Lock()
	for _, h := range seen {
		for k := range h {
			assert.NotEqual(t, "x-coral-hub-tier", strings.ToLower(k), "the tier never reaches the Coral server")
		}
	}
	mu.Unlock()
	// The tier redacts or not, nothing more: a Never route stays refused at Launch.
	req, _ := http.NewRequestWithContext(WithHubTier(context.Background(), "launch"), "GET", "http://x/api/settings", nil)
	_, err := sess.RoundTrip(req)
	require.Error(t, err, "never stays never")
	assert.Contains(t, err.Error(), "path_not_allowed")

	// The terminal: Interact gets the token as it is, until the computer's mode drops to View.
	ctx, cancel := context.WithTimeout(WithHubTier(context.Background(), "interact"), 10*time.Second)
	defer cancel()
	c, err := sess.DialWS(ctx, "/ws/terminal/a", "", nil)
	require.NoError(t, err)
	defer c.CloseNow()
	read := func() string {
		_, b, err := c.Read(ctx)
		require.NoError(t, err)
		return string(b)
	}
	frames <- "one " + tok + "\r\n"
	assert.Equal(t, "one "+tok+"\r\n", read(), "Interact: as it is")
	mode.Store(int32(TierView))
	frames <- "two " + tok + "\r\n"
	two := read()
	assert.NotContains(t, two, tok, "the mode lowered: redacted from the next frame")
	assert.Contains(t, two, redact.Marker)
	close(frames)
}
