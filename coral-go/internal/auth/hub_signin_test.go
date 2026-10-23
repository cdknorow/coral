package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3966: Sign in with Coral Hub, the local half, against a fake hub.

type fakeHubBackend struct {
	mu         sync.Mutex
	linked     bool
	readyFor   map[string]bool
	startErr   error
	challenges []string
	states     []string
	redeemed   []string // verifiers
	grant      HubGrant
	redeemErr  error
	checkErr   error
	checks     int
	linkCalls  int
	linkState  string
}

func (f *fakeHubBackend) Linked() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.linked }
func (f *fakeHubBackend) Ready(_ context.Context, o string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readyFor[o]
}
func (f *fakeHubBackend) Start(_ context.Context, ch, st, o string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", f.startErr
	}
	f.challenges, f.states = append(f.challenges, ch), append(f.states, st)
	return "https://hub.example/signin/coral?flow=" + strings.Repeat("f", 26), nil
}
func (f *fakeHubBackend) Redeem(_ context.Context, code, verifier string) (HubGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.redeemed = append(f.redeemed, verifier)
	return f.grant, f.redeemErr
}
func (f *fakeHubBackend) Check(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.checkErr
}
func (f *fakeHubBackend) Link(context.Context) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linkCalls++
	f.linkState = "enrolling"
	return "https://hub.example/enroll?code=ABCD-EFGH", "ABCD-EFGH", nil
}
func (f *fakeHubBackend) HubPage() string { return "https://hub.example" }
func (f *fakeHubBackend) LinkState() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.linkState, ""
}

type fakeHubSettings struct {
	mu        sync.Mutex
	enabled   bool
	origins   []string
	dismissed bool
}

func (s *fakeHubSettings) SigninEnabled() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.enabled }
func (s *fakeHubSettings) SetSigninEnabled(on bool) error {
	s.mu.Lock()
	s.enabled = on
	s.mu.Unlock()
	return nil
}
func (s *fakeHubSettings) Origins() []string { s.mu.Lock(); defer s.mu.Unlock(); return s.origins }
func (s *fakeHubSettings) SetOrigins(o []string) error {
	s.mu.Lock()
	s.origins = o
	s.mu.Unlock()
	return nil
}
func (s *fakeHubSettings) OfferDismissed() bool { return s.dismissed }
func (s *fakeHubSettings) DismissOffer() error  { s.dismissed = true; return nil }

type hubRig struct {
	*pwRig
	hub      *fakeHubBackend
	set      *fakeHubSettings
	hs       *HubSignin
	operator bool
	actMu    sync.Mutex // the guard and the test both record and read actions
	actions  []string
	now      time.Time
}

// recorded is a copy of the actions recorded so far.
func (g *hubRig) recorded() []string {
	g.actMu.Lock()
	defer g.actMu.Unlock()
	return append([]string(nil), g.actions...)
}

const lanOrigin = "http://10.0.0.9:8420"

// req is pwRig's request, reached on this computer's LAN address (http to a host name is no
// return origin) unless it comes from loopback.
func (g *hubRig) req(method, path, body, addr string, cookie *http.Cookie) *http.Request {
	r := g.pwRig.req(method, path, body, addr, cookie)
	if !strings.HasPrefix(addr, "127.") {
		r.Host = "10.0.0.9:8420"
		r.Header.Set("Origin", "http://"+r.Host)
	}
	return r
}

func newHubRig(t *testing.T) *hubRig {
	g := &hubRig{pwRig: newPwRig(t), operator: true, now: time.Now()}
	g.hub = &fakeHubBackend{linked: true, readyFor: map[string]bool{lanOrigin: true},
		grant: HubGrant{Grant: "g-1", Account: "acct", Display: "Ann", TTL: time.Hour}}
	g.set = &fakeHubSettings{enabled: true}
	g.hs = g.ar.NewHubSignin(HubSigninConfig{
		Hub: g.hub, Settings: g.set, OperatorAllowed: func() bool { return g.operator },
		LoopbackOrigins: func() []string { return []string{"http://localhost:8420"} },
		OnAction: func(_ context.Context, who, method, path string, _ int) {
			g.actMu.Lock()
			g.actions = append(g.actions, who+" "+method+" "+path)
			g.actMu.Unlock()
		},
	})
	g.hs.now = func() time.Time { return g.now }
	return g
}

// start posts the sign-in page's form from another computer and returns the response.
func (g *hubRig) start(addr string) *httptest.ResponseRecorder {
	w, _ := g.do(g.hs.Start, g.req("POST", "/auth/hub/start", "", addr, nil))
	return w
}

func flowCookieOf(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == hubFlowCookie {
			return c
		}
	}
	t.Fatal("no flow cookie")
	return nil
}

func (g *hubRig) callback(state, code string, flow *http.Cookie, host string) *httptest.ResponseRecorder {
	r := g.req("GET", "/auth/hub/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), "", "10.0.0.5", flow)
	if host != "" {
		r.Host = host
	}
	r.Header.Del("Origin")
	r.Header.Set("Sec-Fetch-Site", "cross-site") // a top-level navigation from the hub
	w, _ := g.do(g.hs.Callback, r)
	return w
}

func TestHubSigninStartAndCallbackSignsIn(t *testing.T) {
	g := newHubRig(t)
	w := g.start("10.0.0.5")
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	assert.Equal(t, "https://hub.example/signin/coral?flow="+strings.Repeat("f", 26), w.Header().Get("Location"))
	assert.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
	fc := flowCookieOf(t, w)
	assert.True(t, fc.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, fc.SameSite, "Lax: the callback is a cross-site top-level GET")
	assert.Equal(t, "/auth/hub/", fc.Path)
	assert.Equal(t, 300, fc.MaxAge)
	require.Len(t, g.hub.states, 1)

	w = g.callback(g.hub.states[0], strings.Repeat("c", 43), fc, "")
	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/", w.Header().Get("Location"), "a clean path: no code or state")
	assert.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	// The verifier redeemed is the one the challenge was made from.
	require.Len(t, g.hub.redeemed, 1)
	sum := sha256.Sum256([]byte(g.hub.redeemed[0]))
	assert.Equal(t, g.hub.challenges[0], base64.RawURLEncoding.EncodeToString(sum[:]))
	sc := sessionCookie(t, w)
	assert.Equal(t, http.SameSiteStrictMode, sc.SameSite)
	assert.True(t, sc.HttpOnly)
	cleared := false
	for _, c := range w.Result().Cookies() {
		if c.Name == hubFlowCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	assert.True(t, cleared, "the flow cookie is cleared")
	s, ok := g.ks.lookupSession(sc.Value)
	require.True(t, ok)
	assert.Equal(t, SessionByHub, s.Method)
	assert.Equal(t, "Coral Hub: Ann", s.Label)
	assert.Equal(t, "g-1", s.HubGrant)
	assert.WithinDuration(t, time.Now().Add(time.Hour), s.ExpiresAt, 5*time.Second, "the grant's expiry, not 24 h")
}

func TestHubSigninCallbackRefusals(t *testing.T) {
	g := newHubRig(t)
	code := strings.Repeat("c", 43)
	type flow struct {
		state  string
		cookie *http.Cookie
	}
	begin := func() flow {
		w := g.start("10.0.0.5")
		require.Equal(t, http.StatusSeeOther, w.Code)
		return flow{g.hub.states[len(g.hub.states)-1], flowCookieOf(t, w)}
	}
	failed := func(w *httptest.ResponseRecorder, name string) {
		t.Helper()
		assert.Equal(t, "/auth?hub=failed", w.Header().Get("Location"), name)
		for _, c := range w.Result().Cookies() {
			assert.False(t, isSessionCookieName(c.Name) && c.Value != "", "%s: no session", name)
		}
	}
	a, b := begin(), begin()
	failed(g.callback(a.state, code, nil, ""), "no flow cookie")
	failed(g.callback(a.state, code, a.cookie, ""), "the flow was burned by the failure")
	failed(g.callback(b.state, code, &http.Cookie{Name: hubFlowCookie, Value: "x"}, ""), "another cookie")
	c, d := begin(), begin()
	failed(g.callback(c.state, code, d.cookie, ""), "another flow's cookie")
	assert.Len(t, g.hub.redeemed, 0, "nothing was redeemed")
	require.Equal(t, "/", g.callback(d.state, code, d.cookie, "").Header().Get("Location"), "a failure burns only the flow it named")
	e := begin()
	failed(g.callback(e.state, code, e.cookie, "10.0.0.10:8420"), "another Host")
	f := begin()
	g.now = g.now.Add(hubFlowTTL + time.Second)
	failed(g.callback(f.state, code, f.cookie, ""), "after 5 minutes")
	g.now = time.Now()
	h := begin()
	require.Equal(t, "/", g.callback(h.state, code, h.cookie, "").Header().Get("Location"))
	failed(g.callback(h.state, code, h.cookie, ""), "replayed")
	assert.Len(t, g.hub.redeemed, 2, "only the good ones were redeemed")
	// A redeem the hub refuses is a failure too, and burns the flow.
	g.hub.redeemErr = errors.New("refused")
	i := begin()
	failed(g.callback(i.state, code, i.cookie, ""), "redeem refused")
}

func TestHubSigninStartRefused(t *testing.T) {
	for name, tc := range map[string]func(g *hubRig, r *http.Request){
		"setting off":         func(g *hubRig, _ *http.Request) { g.set.enabled = false },
		"operator switch off": func(g *hubRig, _ *http.Request) { g.operator = false },
		"not linked":          func(g *hubRig, _ *http.Request) { g.hub.linked = false },
		"origin not accepted": func(g *hubRig, r *http.Request) {
			r.Host = "10.0.0.10:8420"
			r.Header.Set("Origin", "http://10.0.0.10:8420")
		},
	} {
		g := newHubRig(t)
		r := g.req("POST", "/auth/hub/start", "", "10.0.0.5", nil)
		tc(g, r)
		w, _ := g.do(g.hs.Start, r)
		assert.Equal(t, "/auth?hub=unavailable", w.Header().Get("Location"), name)
		assert.Empty(t, g.hub.states, name)
	}
	g := newHubRig(t)
	r := g.req("POST", "/auth/hub/start", "", "10.0.0.5", nil)
	r.Header.Set(agentTokenHeader, "tok")
	w, _ := g.do(g.hs.Start, r)
	assert.Equal(t, http.StatusForbidden, w.Code, "an agent")
	r = g.req("POST", "/auth/hub/start", "", "10.0.0.5", nil)
	r.Header.Set("Origin", "https://evil.example")
	w, _ = g.do(g.hs.Start, r)
	assert.Equal(t, http.StatusForbidden, w.Code, "another site")
	assert.Empty(t, g.hub.states)
}

func TestHubSigninStartLimits(t *testing.T) {
	g := newHubRig(t)
	for i := 0; i < hubFlowsMax; i++ {
		require.Equal(t, http.StatusSeeOther, g.start(fmt.Sprintf("10.0.0.%d", 50+i)).Code)
	}
	assert.Equal(t, http.StatusTooManyRequests, g.start("10.0.0.6").Code, "five young flows of other clients are never evicted")
	g.now = g.now.Add(hubFlowYoungest + time.Second)
	assert.Equal(t, http.StatusSeeOther, g.start("10.0.0.6").Code, "an old one makes room")
	// Per address: ten a minute.
	g2 := newHubRig(t)
	clear := func() { g2.hs.mu.Lock(); g2.hs.flows = nil; g2.hs.mu.Unlock() }
	for i := 0; i < hubStartsPerAddr; i++ {
		clear() // room for flows: only the rate limit is tested here
		require.Equal(t, http.StatusSeeOther, g2.start("10.0.0.5").Code, i)
	}
	clear()
	assert.Equal(t, http.StatusTooManyRequests, g2.start("10.0.0.5").Code)
	assert.Equal(t, http.StatusSeeOther, g2.start("10.0.0.7").Code, "another address")
}

func TestHubSigninDisabledInHub(t *testing.T) {
	g := newHubRig(t)
	g.hub.startErr = ErrHubDisabled
	w := g.start("10.0.0.5")
	assert.Equal(t, "/auth?hub=disabled", w.Header().Get("Location"))
	assert.Empty(t, w.Result().Cookies(), "no flow")
	r := g.req("GET", "/auth", "", "10.0.0.5", nil)
	assert.Equal(t, "disabled_in_hub", g.hs.PageState(r))
	g.now = g.now.Add(hubDisabledShown + time.Second)
	assert.Equal(t, "ready", g.hs.PageState(r))
}

func TestHubSigninPageState(t *testing.T) {
	g := newHubRig(t)
	r := g.req("GET", "/auth", "", "10.0.0.5", nil)
	assert.Equal(t, "ready", g.hs.PageState(r))
	g.set.enabled = false
	assert.Equal(t, "", g.hs.PageState(r))
	g.hub.linked = false
	assert.Equal(t, "unpaired", g.hs.PageState(r))
	g.operator = false
	assert.Equal(t, "", g.hs.PageState(r), "#3404 on: no Hub UI at all")
	var nilHS *HubSignin
	assert.Equal(t, "", nilHS.PageState(r))
}

// signIn makes a Hub session and returns its cookie.
func (g *hubRig) signIn(t *testing.T) *http.Cookie {
	t.Helper()
	w := g.start("10.0.0.5")
	w = g.callback(g.hub.states[len(g.hub.states)-1], strings.Repeat("c", 43), flowCookieOf(t, w), "")
	return sessionCookie(t, w)
}

func TestHubSessionDenyTable(t *testing.T) {
	g := newHubRig(t)
	hub := g.signIn(t)
	pw := g.passwordSession(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	guard := g.hs.HubSessionGuard(ok)
	for _, rt := range [][2]string{
		{"POST", "/api/auth/password/set"}, {"POST", "/api/auth/password/change"}, {"POST", "/api/auth/password/reset"},
		{"GET", "/api/auth/sessions"}, {"POST", "/api/auth/sessions/sign-out-others"}, {"DELETE", "/api/auth/sessions/abcdefabcdef"},
		{"GET", "/api/auth/setup-code"}, {"GET", "/api/auth/setup-code/show"}, {"PUT", "/api/auth/hub-signin"}, {"GET", "/api/auth/hub-signin"}, {"PUT", "/api/auth/hub-origins"},
		{"POST", "/api/system/login-token"}, {"GET", "/api/system/api-key"}, {"POST", "/api/system/api-key/regenerate"},
		{"POST", "/api/cloud/connect"}, {"POST", "/api/cloud/disconnect"}, {"POST", "/api/cloud/rekey"}, {"PUT", "/api/cloud/max-mode"},
		{"PUT", "/api/cloud/allow-auto-accept"}, {"PUT", "/api/cloud/redact-tokens"}, {"POST", "/api/cloud/redeem"},
		{"POST", "/api/cloud/connect/cancel"}, {"PUT", "/api/system/hub-operator"}, {"PUT", "/api/system/hub-mode"},
	} {
		w := httptest.NewRecorder()
		guard.ServeHTTP(w, g.req(rt[0], rt[1], "", "10.0.0.5", hub))
		assert.Equal(t, http.StatusForbidden, w.Code, "%s %s", rt[0], rt[1])
		assert.Contains(t, w.Body.String(), hubDeniedCode)
		w = httptest.NewRecorder()
		guard.ServeHTTP(w, g.req(rt[0], rt[1], "", "10.0.0.5", pw))
		assert.Equal(t, 200, w.Code, "a password session is not limited: %s %s", rt[0], rt[1])
	}
	for _, rt := range [][2]string{
		{"GET", "/api/cloud/status"}, {"GET", "/api/system/hub-operator"}, {"POST", "/api/sessions/live/a/send"},
		{"POST", "/api/sessions/launch"}, {"PUT", "/api/settings"}, {"GET", "/api/sessions/live"},
	} {
		w := httptest.NewRecorder()
		guard.ServeHTTP(w, g.req(rt[0], rt[1], "", "10.0.0.5", hub))
		assert.Equal(t, 200, w.Code, "a full Operator otherwise: %s %s", rt[0], rt[1])
	}
	assert.Contains(t, g.recorded(), "Coral Hub: Ann POST /api/sessions/live/a/send", "its writes are in the Operator-actions log")
	assert.NotContains(t, g.recorded(), "Coral Hub: Ann GET /api/sessions/live", "plain reads are not")
}

// passwordSession signs a browser in with a password session directly.
func (g *hubRig) passwordSession(t *testing.T) *http.Cookie {
	t.Helper()
	tok, err := g.ks.NewSession(g.req("GET", "/", "", "10.0.0.5", nil), SessionByPassword, false)
	require.NoError(t, err)
	return &http.Cookie{Name: g.ks.cookieName(g.req("GET", "/", "", "10.0.0.5", nil)), Value: tok}
}

func TestHubSessionChecks(t *testing.T) {
	g := newHubRig(t)
	ctx := context.Background()
	sc := g.signIn(t)
	g.now = time.Now() // the session's last good check is its sign-in, on the real clock
	live := func() bool { _, ok := g.ks.lookupSession(sc.Value); return ok }

	g.hs.checkOnce(ctx)
	assert.Equal(t, 0, g.hub.checks, "not due yet")
	g.now = g.now.Add(hubCheckEvery)
	g.hs.checkOnce(ctx)
	assert.Equal(t, 1, g.hub.checks)
	assert.True(t, live())

	// Unanswered: kept until 10 minutes after the last good check, then ended.
	g.hub.checkErr = ErrHubUnanswered
	for i := 0; i < 9; i++ {
		g.now = g.now.Add(hubCheckEvery)
		g.hs.checkOnce(ctx)
		assert.True(t, live(), "minute %d without an answer", i+2)
	}
	g.now = g.now.Add(hubCheckEvery)
	g.hs.checkOnce(ctx)
	assert.False(t, live(), "ten minutes without an answer ends it")

	// Answered: ends at once.
	g.hub.checkErr = nil
	sc = g.signIn(t)
	g.now = time.Now()
	g.hub.checkErr = errors.New("revoked")
	g.now = g.now.Add(hubCheckEvery)
	g.hs.checkOnce(ctx)
	assert.False(t, live(), "an answered failure ends it at once")
}

func TestHubSessionsEndWhenSigninIsNotAllowed(t *testing.T) {
	for name, off := range map[string]func(g *hubRig){
		"setting off": func(g *hubRig) { g.set.enabled = false },
		"#3404 on":    func(g *hubRig) { g.operator = false },
		"not linked":  func(g *hubRig) { g.hub.linked = false },
	} {
		g := newHubRig(t)
		sc := g.signIn(t)
		off(g)
		g.hs.checkOnce(context.Background())
		_, ok := g.ks.lookupSession(sc.Value)
		assert.False(t, ok, name)
		assert.Zero(t, g.hub.checks, "%s: no hub call needed", name)
	}
}

// A switch turned off ends a Hub session at its next request, not only at the next check.
func TestHubSessionGuardEndsItWhenNotAllowed(t *testing.T) {
	g := newHubRig(t)
	sc := g.signIn(t)
	g.operator = false // #3404 turned on
	w := httptest.NewRecorder()
	g.hs.HubSessionGuard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("must not run") })).
		ServeHTTP(w, g.req("GET", "/api/sessions/live", "", "10.0.0.5", sc))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	_, ok := g.ks.lookupSession(sc.Value)
	assert.False(t, ok)
}

func TestHubSessionEndClosesItsRequests(t *testing.T) {
	g := newHubRig(t)
	sc := g.signIn(t)
	entered, done := make(chan struct{}), make(chan struct{})
	h := g.ks.SessionRequests(g.hs.HubSessionGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done() // a WebSocket's read loop
		close(done)
	})))
	go h.ServeHTTP(httptest.NewRecorder(), g.req("GET", "/ws/terminal/a", "", "10.0.0.5", sc))
	<-entered
	assert.Equal(t, 1, g.hs.EndAll(context.Background(), "test"))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ending the session didn't close its request")
	}
}

func TestHubSigninSettingsRoutes(t *testing.T) {
	g := newHubRig(t)
	pw := g.passwordSession(t)
	hub := g.signIn(t)
	put := func(c *http.Cookie, body string, mod func(*http.Request)) (*httptest.ResponseRecorder, map[string]any) {
		r := g.req("PUT", "/api/auth/hub-signin", body, "10.0.0.5", c)
		if mod != nil {
			mod(r)
		}
		return g.do(g.hs.SetSettings, r)
	}
	for name, mod := range map[string]func(*http.Request){
		"agent token":  func(r *http.Request) { r.Header.Set(agentTokenHeader, "t") },
		"tunnel":       func(r *http.Request) { r.Header.Set("X-Coral-Via-Tunnel", "x") },
		"another site": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"no Origin":    func(r *http.Request) { r.Header.Del("Origin") },
	} {
		w, _ := put(pw, `{"enabled":true}`, mod)
		assert.Equal(t, http.StatusForbidden, w.Code, name)
	}
	w, _ := put(hub, `{"enabled":false}`, nil)
	assert.Equal(t, http.StatusForbidden, w.Code, "a Hub session can't change it")
	w, outNoSession := put(nil, `{"enabled":true}`, nil)
	assert.Equal(t, http.StatusForbidden, w.Code, "no browser session")
	assert.Equal(t, "password_session_required", outNoSession["code"])
	w, _ = put(pw, `{}`, nil)
	assert.Equal(t, http.StatusBadRequest, w.Code, "no value is no change")

	// Off ends every Hub session at once.
	w, out := put(pw, `{"enabled":false}`, nil)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.EqualValues(t, 1, out["signed_out"])
	_, ok := g.ks.lookupSession(hub.Value)
	assert.False(t, ok)
	assert.False(t, g.set.enabled)
	w, _ = put(pw, `{"enabled":true}`, nil)
	assert.Equal(t, 200, w.Code)
	assert.True(t, g.set.enabled)

	// Origins: validated, de-duplicated, the hub told.
	changed := 0
	g.hs.originsChanged = func() { changed++ }
	r := g.req("PUT", "/api/auth/hub-origins", `{"origins":["HTTPS://Coral.Example.","http://192.168.1.5:8420","https://coral.example:443"]}`, "10.0.0.5", pw)
	w, out = g.do(g.hs.SetOrigins, r)
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, []any{"https://coral.example", "http://192.168.1.5:8420"}, out["origins"])
	assert.Equal(t, 1, changed)
	r = g.req("PUT", "/api/auth/hub-origins", `{"origins":["https://ok.example","http://example.com"]}`, "10.0.0.5", pw)
	w, out = g.do(g.hs.SetOrigins, r)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.EqualValues(t, 1, out["index"])
	assert.Equal(t, []string{"https://coral.example", "http://192.168.1.5:8420"}, g.set.origins, "a refused list changes nothing")
	assert.Equal(t, []string{"http://localhost:8420", "https://coral.example", "http://192.168.1.5:8420"}, g.hs.Origins())
}

func TestCanonicalOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://coral.example":           "https://coral.example",
		"HTTPS://Coral.Example.":          "https://coral.example",
		"https://coral.example:443":       "https://coral.example",
		"https://coral.example:8443/":     "https://coral.example:8443",
		"http://192.168.1.5:8420":         "http://192.168.1.5:8420",
		"http://10.1.2.3":                 "http://10.1.2.3",
		"http://172.16.0.1:80":            "http://172.16.0.1",
		"http://100.101.102.103:8420":     "http://100.101.102.103:8420",
		"http://[fd7a:115c:a1e0::1]:8420": "http://[fd7a:115c:a1e0::1]:8420",
		"http://[fd00::5]:8420":           "http://[fd00::5]:8420",
		"http://127.0.0.1:8420":           "http://127.0.0.1:8420",
		"http://[::1]:8420":               "http://[::1]:8420",
		"http://LocalHost:8420":           "http://localhost:8420",
	} {
		got, err := CanonicalOrigin(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{
		"http://example.com", "http://8.8.8.8", "http://100.63.0.1", "https://user:pw@coral.example", "https://coral.example/path",
		"https://coral.example?x=1", "https://coral.example#f", "https://*.coral.example", "ftp://coral.example", "coral.example",
		"https://coral.example:0", "https://coral.example:70000", "javascript:alert(1)", "", "http://localhost.evil.example:8420",
		"https://..", "https://.", "https://.coral.example", "https://coral..example", "https://coral.example..", "https://..:8443",
	} {
		_, err := CanonicalOrigin(bad)
		assert.Error(t, err, bad)
	}
}

func TestHubLinkFromTheSignedOutPage(t *testing.T) {
	g := newHubRig(t)
	link := func(body, addr string) (*httptest.ResponseRecorder, map[string]any) {
		return g.do(g.hs.Link, g.req("POST", "/auth/hub/link", body, addr, nil))
	}
	w, out := link(`{}`, "10.0.0.5")
	assert.Equal(t, http.StatusConflict, w.Code, "already linked: re-linking is disconnect + connect")
	assert.Equal(t, "already_linked", out["code"])

	g.hub.linked = false
	g.set.enabled = false
	w, out = link(`{}`, "10.0.0.5")
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "setup_code_required", out["code"])
	w, out = link(`{"setup_code":"WRONG-CODE"}`, "10.0.0.5")
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "setup_code_wrong", out["code"])
	assert.Zero(t, g.hub.linkCalls)

	// The setup code (no password yet) proves control from another computer; the box unticked.
	code := g.ks.SetupCode()
	require.NotEmpty(t, code)
	w, out = link(`{"setup_code":"`+code+`","enable_signin":false}`, "10.0.0.5")
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, "ABCD-EFGH", out["user_code"])
	var lc *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == hubLinkCookie {
			lc = c
		}
	}
	require.NotNil(t, lc)
	g.hub.mu.Lock()
	g.hub.linkState, g.hub.linked = "linked", true
	g.hub.mu.Unlock()
	w, out = g.do(g.hs.LinkStatus, g.req("GET", "/auth/hub/link/status", "", "10.0.0.5", lc))
	require.Equal(t, 200, w.Code)
	assert.Equal(t, "linked", out["state"])
	assert.False(t, g.set.enabled, "unticked: linked only")

	// Ticked, from this computer (no code needed).
	g.hub.linked = false
	g.ar.SetPeerUser(func(*http.Request) (bool, bool) { return true, true })
	w, _ = link(`{"enable_signin":true}`, "127.0.0.1")
	require.Equal(t, 200, w.Code, w.Body.String())
	for _, c := range w.Result().Cookies() {
		if c.Name == hubLinkCookie {
			lc = c
		}
	}
	g.hub.mu.Lock()
	g.hub.linkState, g.hub.linked = "linked", true
	g.hub.mu.Unlock()
	_, out = g.do(g.hs.LinkStatus, g.req("GET", "/auth/hub/link/status", "", "127.0.0.1", lc))
	assert.Equal(t, "linked", out["state"])
	assert.True(t, g.set.enabled, "the person's tick turns it on, once")
	assert.Contains(t, g.recorded(), "this computer EVENT Sign in with Coral Hub enabled while linking")

	_, out = g.do(g.hs.LinkStatus, g.req("GET", "/auth/hub/link/status", "", "10.0.0.5", nil))
	assert.Equal(t, "no_link", out["code"], "only the browser that started it")
}

func TestRevokeSessionByID(t *testing.T) {
	g := newHubRig(t)
	pw := g.passwordSession(t)
	hub := g.signIn(t)
	s, _ := g.ks.lookupSession(hub.Value)
	w, _ := g.do(g.ar.RevokeSessionByID, g.req("DELETE", "/api/auth/sessions/"+s.IDHash[:12], "", "10.0.0.5", pw))
	require.Equal(t, 200, w.Code, w.Body.String())
	_, ok := g.ks.lookupSession(hub.Value)
	assert.False(t, ok)
	w, _ = g.do(g.ar.RevokeSessionByID, g.req("DELETE", "/api/auth/sessions/zzzz", "", "10.0.0.5", pw))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w, _ = g.do(g.ar.RevokeSessionByID, g.req("DELETE", "/api/auth/sessions/"+strings.Repeat("0", 12), "", "10.0.0.5", pw))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// Security's ruling (a): a Hub session's reads of file content, diffs, transcripts, terminal
// capture, history, peek and artifacts are recorded, once a minute per route; other reads not.
func TestHubSessionSensitiveReadsAreRecordedOnceAMinute(t *testing.T) {
	g := newHubRig(t)
	sc := g.signIn(t)
	guard := g.hs.HubSessionGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	get := func(path string) {
		guard.ServeHTTP(httptest.NewRecorder(), g.req("GET", path, "", "10.0.0.5", sc))
	}
	for i := 0; i < 3; i++ {
		get("/api/sessions/live/a/file-content?filepath=x")
		get("/api/sessions/live/b/file-content?filepath=y")
		get("/api/sessions/live/a/poll")
		get("/api/artifacts/abc")
		get("/api/sessions/live") // a plain list
	}
	count := func(line string) int {
		n := 0
		for _, a := range g.recorded() {
			if a == line {
				n++
			}
		}
		return n
	}
	assert.Equal(t, 1, count("Coral Hub: Ann GET /api/sessions/live/{name}/file-content"), "one line a minute for the route")
	assert.Equal(t, 1, count("Coral Hub: Ann GET /api/sessions/live/{name}/poll"))
	assert.Equal(t, 1, count("Coral Hub: Ann GET /api/artifacts/*"))
	assert.Zero(t, count("Coral Hub: Ann GET /api/sessions/live"))
	g.now = g.now.Add(time.Minute)
	get("/api/sessions/live/a/file-content")
	assert.Equal(t, 2, count("Coral Hub: Ann GET /api/sessions/live/{name}/file-content"), "again after a minute")

	for path, want := range map[string]string{
		"/api/sessions/live/a/diff":             "/api/sessions/live/{name}/diff",
		"/api/sessions/live/a/changes.diff":     "/api/sessions/live/{name}/changes.diff",
		"/api/sessions/live/a/chat":             "/api/sessions/live/{name}/chat",
		"/api/sessions/live/a/capture":          "/api/sessions/live/{name}/capture",
		"/api/sessions/live/a/history/search":   "/api/sessions/live/{name}/history/search",
		"/api/sessions/s1/changes.diff":         "/api/sessions/{id}/changes.diff",
		"/api/sessions/history/s1":              "/api/sessions/history/*",
		"/api/agent/history/search":             "/api/agent/history/*",
		"/api/board/p/peek":                     "/api/board/{project}/peek",
		"/api/board/p/tasks/7/artifact-content": "/api/board/{project}/tasks/{id}/artifact-content",
		"/api/board/p/tasks/7/changes.diff":     "/api/board/{project}/tasks/{id}/changes.diff",
		"/api/sessions/live/a/info":             "",
		"/api/board/p/messages":                 "",
	} {
		assert.Equal(t, want, hubSensitiveRead("GET", path), path)
	}
	assert.Empty(t, hubSensitiveRead("POST", "/api/sessions/live/a/file-content"), "writes are recorded as writes")
}

// A dashboard with no browser session (this computer without a cookie, an API-key caller) is
// answered 403 password_session_required by every Hub sign-in settings route, never the 401
// that reads as "signed out" (UX's real-server run; Backend's f2966d5a).
func TestHubSigninSettingsNeedABrowserSession(t *testing.T) {
	g := newHubRig(t)
	for name, h := range map[string]http.HandlerFunc{
		"GET hub-signin": g.hs.Settings, "PUT hub-signin": g.hs.SetSettings, "POST dismiss": g.hs.DismissOffer,
		"GET hub-origins": g.hs.GetOrigins, "PUT hub-origins": g.hs.SetOrigins,
	} {
		method := strings.Fields(name)[0]
		w, out := g.do(h, g.req(method, "/api/auth/x", `{"enabled":true,"origins":[]}`, "10.0.0.5", nil))
		assert.Equal(t, http.StatusForbidden, w.Code, name)
		assert.Equal(t, "password_session_required", out["code"], name)
	}
	assert.False(t, g.set.dismissed)
}

func TestHubSigninHubURL(t *testing.T) {
	g := newHubRig(t)
	assert.Equal(t, "https://hub.example", g.hs.HubURL())
	var none *HubSignin
	assert.Empty(t, none.HubURL())
}

// GET /api/auth/hub-origins carries this server's loopback origins beside the configured ones,
// for the Settings rows (UX's #4003).
func TestHubOriginsListsLoopback(t *testing.T) {
	g := newHubRig(t)
	g.set.origins = []string{"https://coral.example"}
	pw := g.passwordSession(t)
	w, out := g.do(g.hs.GetOrigins, g.req("GET", "/api/auth/hub-origins", "", "10.0.0.5", pw))
	require.Equal(t, 200, w.Code, w.Body.String())
	assert.Equal(t, []any{"https://coral.example"}, out["origins"])
	assert.Equal(t, []any{"http://localhost:8420"}, out["loopback"])
}

// #4977: one client cannot hold every place. Its third start replaces its own oldest flow, so five starts a minute from one address (or one IPv6 /64)
// no longer keep everyone else's sign-in refused.
func TestHubSigninOneClientCannotFillTheFlows(t *testing.T) {
	g := newHubRig(t)
	for i := 0; i < 20; i++ {
		g.hs.starts, g.hs.startsAll = map[string][]time.Time{}, nil // the rate limits are not under test here
		require.Equal(t, http.StatusSeeOther, g.start("10.0.0.5").Code, i)
		g.hs.mu.Lock()
		held := 0
		for _, f := range g.hs.flows {
			if f.addr == "10.0.0.5" {
				held++
			}
		}
		g.hs.mu.Unlock()
		assert.LessOrEqual(t, held, hubFlowsPerAddr, "start %d", i)
	}
	assert.Equal(t, http.StatusSeeOther, g.start("10.0.0.6").Code, "another person can still start a sign-in")
	// an IPv6 client counts by its /64
	g2 := newHubRig(t)
	for i := 0; i < 6; i++ {
		g2.hs.starts = map[string][]time.Time{}
		g2.hs.startsAll = nil
		require.Equal(t, http.StatusSeeOther, g2.start(fmt.Sprintf("[2001:db8:1:2::%x]", i+1)).Code, i)
	}
	g2.hs.mu.Lock()
	assert.Len(t, g2.hs.flows, hubFlowsPerAddr, "one /64 holds at most its share")
	g2.hs.mu.Unlock()
}

// #4977: a Hub session's sensitive read is named from the routed path: an encoded slash in a name stays one segment, so the read is still recorded.
func TestHubSensitiveReadIsNamedFromTheRoutedPath(t *testing.T) {
	assert.Equal(t, "/api/sessions/live/{name}/file-content", hubSensitiveRead("GET", "/api/sessions/live/a%2Fb/file-content"))
	assert.Equal(t, "", hubSensitiveRead("GET", "/api/sessions/live/a/b/file-content"), "the decoded spelling has an extra segment and names no such route")

	// through the guard: the read of a name with an encoded slash is recorded, and an encoded spelling of a denied route stays denied
	g := newHubRig(t)
	hub := g.signIn(t)
	guard := g.hs.HubSessionGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	w := httptest.NewRecorder()
	guard.ServeHTTP(w, g.req("GET", "/api/sessions/live/a%2Fb/file-content", "", "10.0.0.5", hub))
	assert.Equal(t, 200, w.Code)
	assert.Contains(t, g.recorded(), "Coral Hub: Ann GET /api/sessions/live/{name}/file-content", "the read is in the Operator-actions log")
	w = httptest.NewRecorder()
	guard.ServeHTTP(w, g.req("GET", "/api/auth%2Fsessions", "", "10.0.0.5", hub))
	assert.Equal(t, http.StatusForbidden, w.Code, "the deny list reads the decoded path: a superset, so an encoded spelling cannot slip past it")
}
