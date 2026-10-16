package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/auth"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/server/routes"
	"github.com/cdknorow/coral/internal/store"
	"github.com/cdknorow/coral/internal/store/storetest"
	"github.com/cdknorow/coral/pkg/secretbox"
	"github.com/cdknorow/coral/pkg/tunnel"
)

// #4040: Sign in with Coral Hub, end to end, against a fake hub over a REAL tunnel and through
// the REAL router. coral-hub's own side isn't merged yet, so this guards the whole chain: the
// connector dials the fake hub, the browser flow (start -> consent redirect -> callback with the
// flow cookie -> redeem over the tunnel -> session cookie) signs the owner in, the deny table
// holds for that Hub session, a callback without the flow cookie is refused, the off states end
// it, and CheckSignin over the tunnel maps each hub answer to the right error class. The
// check-loop's timing (the 10-minute grace, the session ending) is driven by the clock in
// auth's TestHubSessionChecks; here we confirm the real wire produces the classes it relies on.

const (
	e2eOwnerAcct = "11111111-2222-4333-8444-555555555555"
	e2eOtherAcct = "99999999-2222-4333-8444-555555555555"
	e2eGrantID   = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	e2eHubToken  = "coralhub_srv_abcd1234_" + "x0123456789012345678901234567890123456789012"
)

// signinReplies answers each sign-in op with a JSON body and status, and records what it was
// sent (so the test can read the state the connector generated). Mirrors cloud's signinHub.
type signinReplies struct {
	mu      sync.Mutex
	replies map[string]string
	status  map[string]int
	got     map[string][]map[string]any
}

func newSigninReplies() *signinReplies {
	return &signinReplies{replies: map[string]string{}, status: map[string]int{}, got: map[string][]map[string]any{}}
}

func (s *signinReplies) set(op, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies[op] = body
}

func (s *signinReplies) setStatus(op string, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if code == 0 {
		delete(s.status, op)
		return
	}
	s.status[op] = code
}

func (s *signinReplies) sent(op string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got[op]
}

func (s *signinReplies) answer(req tunnel.SigninRequest) tunnel.SigninResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body map[string]any
	_ = json.Unmarshal(req.Body, &body)
	s.got[req.Op] = append(s.got[req.Op], body)
	st := 200
	if c, ok := s.status[req.Op]; ok {
		st = c
	}
	return tunnel.SigninResponse{Status: st, Body: []byte(s.replies[req.Op])}
}

// signinFakeHub is a TLS hub that speaks enough of the connector's wire to enroll and hold a
// tunnel that offers Hub sign-in (hub-signin v1). TLS so release-tag builds, which refuse a
// cleartext hub, run it too. A trimmed copy of cloud's fakeHub.
type signinFakeHub struct {
	srv      *httptest.Server
	polls    atomic.Int64
	sessions chan *tunnel.Session
	signin   *signinReplies
}

func newSigninFakeHub(t *testing.T, account string, sr *signinReplies) *signinFakeHub {
	t.Helper()
	h := &signinFakeHub{sessions: make(chan *tunnel.Session, 8), signin: sr}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/enroll/start", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": strings.Repeat("d", 43), "user_code": "ABCD-EFGH",
			"verification_url": h.srv.URL + "/enroll?code=ABCD-EFGH", "interval": 1, "expires_in": 600,
		})
	})
	mux.HandleFunc("POST /api/enroll/poll", func(w http.ResponseWriter, r *http.Request) {
		if h.polls.Add(1) < 2 { // pending once, then approved (approveAt = 2)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		reply := map[string]any{"server_token": e2eHubToken, "server_id": "srv_1", "label": "My Laptop"}
		if account != "" {
			reply["account"] = account
		}
		_ = json.NewEncoder(w).Encode(reply)
	})
	mux.HandleFunc("GET /tunnel", func(w http.ResponseWriter, r *http.Request) {
		s, err := tunnel.AcceptHTTP(w, r, func(_ context.Context, tok string) (any, error) {
			if tok != e2eHubToken {
				return nil, tunnel.ErrUnauthorized
			}
			return "srv_1", nil
		}, tunnel.SessionConfig{
			Capabilities: func(any) []string { return []string{tunnel.CapabilitySignin} },
			OnSignin: func(_ context.Context, _ *tunnel.Session, req tunnel.SigninRequest) tunnel.SigninResponse {
				return h.signin.answer(req)
			},
		})
		if err == nil {
			h.sessions <- s
		}
	})
	h.srv = httptest.NewTLSServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *signinFakeHub) session(t *testing.T) *tunnel.Session {
	t.Helper()
	select {
	case s := <-h.sessions:
		t.Cleanup(func() { s.Close() })
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("connector did not connect over the tunnel")
		return nil
	}
}

// TestHubSigninFullFlowOverTheTunnel drives the whole feature end to end.
func TestHubSigninFullFlowOverTheTunnel(t *testing.T) {
	t.Setenv(secretbox.EnvKey, "") // no key file: don't block on encryption setup
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	cfg.StrictLocalAuth = true
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := NewWithKey(cfg, db, stack.backend, stack.terminal, "")
	t.Cleanup(srv.Close)
	require.NotNil(t, srv.hubSignin)

	local := httptest.NewServer(srv.Router())
	t.Cleanup(local.Close)
	// Over plain HTTP the request origin auth derives is "http://" + Host, i.e. local.URL, so the
	// return origin the connector offers the hub must be exactly that.
	origin, err := auth.CanonicalOrigin(local.URL)
	require.NoError(t, err)

	sr := newSigninReplies()
	sr.set(tunnel.SigninOrigins, `{"ok":true,"account":"`+e2eOwnerAcct+`"}`)
	hub := newSigninFakeHub(t, e2eOwnerAcct, sr)

	// A real connector: enroll over the fake hub, hold the tunnel, pin the owner, accept the origin.
	cs := store.NewCloudStore(db, store.NewRemoteServerStore(db, dir))
	m := cloud.New(cloud.Options{
		HubURL: hub.srv.URL, HTTPClient: hub.srv.Client(), Store: cs, LocalBaseURL: local.URL,
		CoralVersion: "test", PollInterval: 20 * time.Millisecond,
		Backoff:       tunnel.Backoff{Min: 20 * time.Millisecond, Max: 100 * time.Millisecond},
		SigninOrigins: func() []string { return []string{origin} },
		OnSigninEvent: srv.HubSigninEvent,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	require.NoError(t, m.Connect(context.Background()))
	require.Eventually(t, func() bool { return m.Status().State == cloud.StateConnected }, 10*time.Second, 20*time.Millisecond, "tunnel up")
	hub.session(t)
	require.Eventually(t, func() bool { return m.SigninReady(context.Background(), origin) }, 10*time.Second, 20*time.Millisecond, "owner pinned and origin accepted")

	srv.SetCloud(m)
	srv.cloudHandler.SetLinkedCheck(func() bool { return true })
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), HubSigninEnabledSetting, "true"))

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, cookies ...*http.Cookie) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, local.URL+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", local.URL)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := noFollow.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp
	}

	// ── Step 1: start a sign-in. The hub's begin names a consent page on its own https host. ──
	hubHost := strings.TrimPrefix(hub.srv.URL, "https://")
	hubPage := "https://" + hubHost + "/signin/coral?flow=" + strings.Repeat("f", 26)
	sr.set(tunnel.SigninBegin, `{"ok":true,"flow":"`+strings.Repeat("f", 26)+`","url":"`+hubPage+`","ttl":300}`)

	start := do("POST", "/auth/hub/start")
	require.Equal(t, http.StatusSeeOther, start.StatusCode, "start redirects the browser to the hub")
	assert.Equal(t, hubPage, start.Header.Get("Location"), "to the consent page the hub named")
	var flow *http.Cookie
	for _, c := range start.Cookies() {
		if strings.Contains(c.Name, "coral_hub_flow") && c.Value != "" {
			flow = c
		}
	}
	require.NotNil(t, flow, "a flow cookie is set")
	begun := sr.sent(tunnel.SigninBegin)
	require.Len(t, begun, 1, "begin reached the hub over the tunnel")
	state, _ := begun[0]["state"].(string)
	require.NotEmpty(t, state, "the connector sent a state")
	assert.Equal(t, origin, begun[0]["return"], "and our return origin")

	// ── Step 2: the callback redeems the code over the tunnel and mints a Hub session. ──
	sr.set(tunnel.SigninRedeem, `{"ok":true,"grant":"`+e2eGrantID+`","account":"`+e2eOwnerAcct+`","display":"Ann","ttl":3600}`)

	// Without the flow cookie: refused, no session.
	noCookie := do("GET", "/auth/hub/callback?code="+strings.Repeat("c", 43)+"&state="+state)
	assert.Equal(t, "/auth?hub=failed", noCookie.Header.Get("Location"), "a callback without the flow cookie is refused")
	assert.Empty(t, sessionCookie(t, srv, local, noCookie), "no session from it")

	// A wrong state, with the cookie: also refused (and it burns the flow, so re-run start).
	badState := do("GET", "/auth/hub/callback?code="+strings.Repeat("c", 43)+"&state=nope", flow)
	assert.Equal(t, "/auth?hub=failed", badState.Header.Get("Location"), "a mismatched state is refused")

	// Fresh flow, correct state and the cookie: signed in.
	start = do("POST", "/auth/hub/start")
	require.Equal(t, http.StatusSeeOther, start.StatusCode)
	for _, c := range start.Cookies() {
		if strings.Contains(c.Name, "coral_hub_flow") && c.Value != "" {
			flow = c
		}
	}
	begun = sr.sent(tunnel.SigninBegin)
	state, _ = begun[len(begun)-1]["state"].(string)
	cb := do("GET", "/auth/hub/callback?code="+strings.Repeat("c", 43)+"&state="+state, flow)
	require.Equal(t, http.StatusSeeOther, cb.StatusCode)
	assert.Equal(t, "/", cb.Header.Get("Location"), "signed in, back to the dashboard")
	session := sessionCookie(t, srv, local, cb)
	require.NotNil(t, session, "a session cookie is set")

	// ── Step 3: the owner has full control. ──
	assert.Equal(t, http.StatusOK, do("GET", "/api/sessions/live", session).StatusCode, "a full Operator session")

	// ── The deny table holds for a Hub session (the Operator-only and self-management routes). ──
	for _, p := range [][2]string{
		{"GET", "/api/auth/sessions"}, {"POST", "/api/auth/password/change"}, {"PUT", "/api/auth/hub-signin"},
		{"PUT", "/api/auth/hub-origins"}, {"POST", "/api/system/login-token"}, {"GET", "/api/system/api-key"},
		{"POST", "/api/cloud/disconnect"}, {"PUT", "/api/cloud/max-mode"}, {"PUT", "/api/system/hub-operator"},
	} {
		assert.Equal(t, http.StatusForbidden, do(p[0], p[1], session).StatusCode, "%s %s", p[0], p[1])
	}
	// Nor through the generic settings PUT: refused, nothing stored.
	for _, k := range []string{routes.MaxModeSetting, routes.AllowHubAutoAcceptSetting, routes.RedactTokensSetting} {
		req, _ := http.NewRequest("PUT", local.URL+"/api/settings", strings.NewReader(`{"`+k+`":"launch"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", local.URL)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.AddCookie(session)
		resp, err := noFollow.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, k)
		st, _ := store.NewSessionStore(db).GetSettings(context.Background())
		assert.Empty(t, st[k], k)
	}

	// ── The check loop's results over the REAL tunnel: each hub answer maps to its error class.
	// nil keeps the session; ErrSigninRefused ends it at once; ErrSigninBusy (a 429) is transient
	// (it wraps ErrSigninUnanswered, so the 10-minute grace applies). The grace timing and the
	// actual session-ending are driven by the clock in auth's TestHubSessionChecks. ──
	chk := context.Background()
	sr.set(tunnel.SigninCheck, `{"ok":true,"status":"ok","account":"`+e2eOwnerAcct+`"}`)
	assert.NoError(t, m.CheckSignin(chk, e2eGrantID), "ok keeps the session")

	sr.set(tunnel.SigninCheck, `{"ok":false,"status":"revoked"}`)
	err = m.CheckSignin(chk, e2eGrantID)
	assert.ErrorIs(t, err, cloud.ErrSigninRefused, "revoked ends it at once")
	assert.NotErrorIs(t, err, cloud.ErrSigninUnanswered, "revoked is not transient")

	// A 429 (the hub busy): transient, not a refusal. Body is the same fixed {"ok":false}.
	sr.set(tunnel.SigninCheck, `{"ok":false}`)
	sr.setStatus(tunnel.SigninCheck, http.StatusTooManyRequests)
	err = m.CheckSignin(chk, e2eGrantID)
	assert.ErrorIs(t, err, cloud.ErrSigninBusy, "a 429 check is busy")
	assert.ErrorIs(t, err, cloud.ErrSigninUnanswered, "and busy is transient like no answer")
	assert.NotErrorIs(t, err, cloud.ErrSigninRefused, "a 429 never ends the session on its own")
	sr.setStatus(tunnel.SigninCheck, 0)

	// ── Off states end the session and refuse a new sign-in. ──
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), HubSigninEnabledSetting, "false"))
	require.Eventually(t, func() bool { return do("GET", "/api/sessions/live", session).StatusCode == http.StatusUnauthorized },
		3*time.Second, 50*time.Millisecond, "turning Hub sign-in off ends the session")
	require.NoError(t, store.NewSessionStore(db).SetSetting(context.Background(), HubSigninEnabledSetting, "true"))
	assert.Equal(t, http.StatusUnauthorized, do("GET", "/api/sessions/live", session).StatusCode, "it stays ended")

	srv.cloudHandler.SetLinkedCheck(func() bool { return false }) // link removed
	unavail := do("POST", "/auth/hub/start")
	assert.Equal(t, http.StatusSeeOther, unavail.StatusCode)
	assert.Equal(t, "/auth?hub=unavailable", unavail.Header.Get("Location"), "no link: a sign-in can't start")
	srv.cloudHandler.SetLinkedCheck(func() bool { return true })

	// ── Wrong account (done last: it blocks the owner pin until re-linking). Over the tunnel this
	// is a refusal that also fires an owner-mismatch, which ends every Hub session. ──
	sr.set(tunnel.SigninCheck, `{"ok":true,"status":"ok","account":"`+e2eOtherAcct+`"}`)
	assert.ErrorIs(t, m.CheckSignin(chk, e2eGrantID), cloud.ErrSigninRefused, "another account ends it at once")
}

// sessionCookie returns the dashboard session cookie set on resp, or nil.
func sessionCookie(t *testing.T, srv *Server, local *httptest.Server, resp *http.Response) *http.Cookie {
	t.Helper()
	name := srv.keyStore.SessionCookieName(httptest.NewRequest("GET", local.URL+"/", nil))
	for _, c := range resp.Cookies() {
		if c.Name == name && c.Value != "" {
			return c
		}
	}
	return nil
}
