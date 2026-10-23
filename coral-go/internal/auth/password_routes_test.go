package auth

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pwRig struct {
	t   *testing.T
	ks  *KeyStore
	ar  *Routes
	cli bool
}

func newPwRig(t *testing.T) *pwRig {
	setupConsole = &strings.Builder{}
	ks := newTestKeyStore(t)
	g := &pwRig{t: t, ks: ks, ar: NewRoutes(ks)}
	g.ar.SetCLIVerified(func(*http.Request) bool { return g.cli })
	return g
}

// req builds a same-origin browser request from addr (10.0.0.5 is another computer).
func (g *pwRig) req(method, path, body, addr string, cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = addr + ":5000"
	r.Host = "coral.test:8420"
	if strings.HasPrefix(addr, "127.") {
		r.Host = "localhost:8420"
	}
	r.Header.Set("Origin", "http://"+r.Host)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) Chrome/129.0 Safari/537.36")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}

func (g *pwRig) do(h http.HandlerFunc, r *http.Request) (*httptest.ResponseRecorder, map[string]any) {
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if isSessionCookieName(c.Name) && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie in %v", w.Result().Header)
	return nil
}

func (g *pwRig) setPassword(pw string) {
	phc, err := HashPassword(pw)
	require.NoError(g.t, err)
	require.NoError(g.t, CreatePasswordHash(g.ks.dataDir(), phc))
}

func TestPasswordSignIn(t *testing.T) {
	g := newPwRig(t)
	body := `{"password":"open sesame","keep":false}`

	// No password set and a wrong one answer the same.
	w, out := g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", body, "10.0.0.5", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "wrong_password", out["code"])
	g.setPassword("open sesame")
	w, out = g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", `{"password":"nope"}`, "10.0.0.6", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, map[string]any{"code": "wrong_password", "error": "Wrong password"}, out)

	// Right: a browser-session cookie, HttpOnly, SameSite=Strict, not Secure over http.
	w, _ = g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", body, "10.0.0.7", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	c := sessionCookie(t, w)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, c.SameSite)
	assert.False(t, c.Secure)
	assert.Zero(t, c.MaxAge)
	assert.True(t, g.ks.ValidateSession(c.Value))
	// Keep me signed in: 30 days.
	w, _ = g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", `{"password":"open sesame","keep":true}`, "10.0.0.7", c))
	k := sessionCookie(t, w)
	assert.Equal(t, int(sessionKeepAge.Seconds()), k.MaxAge)
	assert.False(t, g.ks.ValidateSession(c.Value), "signing in again ends the cookie it came with")

	// Origin: none is fine (a script), a foreign one or cross-site is refused and gets no cookie.
	r := g.req("POST", "/auth/password", body, "10.0.0.8", nil)
	r.Header.Del("Origin")
	r.Header.Del("Sec-Fetch-Site")
	w, _ = g.do(g.ar.SignInWithPassword, r)
	assert.Equal(t, http.StatusOK, w.Code)
	r = g.req("POST", "/auth/password", body, "10.0.0.8", nil)
	r.Header.Set("Origin", "https://evil.example")
	w, out = g.do(g.ar.SignInWithPassword, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "cross_origin", out["code"])
	assert.Empty(t, w.Result().Cookies())
	r = g.req("POST", "/auth/password", body, "10.0.0.8", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w, _ = g.do(g.ar.SignInWithPassword, r)
	assert.Equal(t, http.StatusForbidden, w.Code)

	// An agent's tool never signs a browser in (Security, #3949 round 1).
	r = g.req("POST", "/auth/password", body, "127.0.0.1", nil)
	r.Header.Set(agentTokenHeader, "an-agent-token")
	w, out = g.do(g.ar.SignInWithPassword, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "forbidden_for_agent", out["code"])
	assert.Empty(t, w.Result().Cookies())

	// Too long: refused before any hash.
	w, out = g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", `{"password":"`+strings.Repeat("x", MaxPasswordBytes+1)+`"}`, "10.0.0.9", nil))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "too_long", out["code"])
}

// The throttle answers 429 with Retry-After, and never touches the API key or a session.
func TestPasswordSignInThrottle(t *testing.T) {
	g := newPwRig(t)
	g.setPassword("right")
	for i := 0; i < freeFails; i++ { // five free; the next must wait 2^0 s
		g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", `{"password":"wrong"}`, "10.0.0.5", nil))
	}
	w, out := g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", `{"password":"right"}`, "10.0.0.5", nil))
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "too_many_attempts", out["code"])
	assert.Equal(t, "1", w.Header().Get("Retry-After"))
	assert.EqualValues(t, 1, out["retry_after"])
	// Another address signs in; the throttled one still has the API key and its session.
	w, _ = g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", `{"password":"right"}`, "10.0.0.6", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	mw := Middleware(g.ks)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) }))
	r := g.req("GET", "/api/sessions/live", "", "10.0.0.5", nil)
	r.Header.Set("Authorization", "Bearer "+g.ks.Key())
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, r)
	assert.Equal(t, 299, rec.Code, "the API key is never throttled by password attempts")
}

func TestPasswordSetChangeAndSessions(t *testing.T) {
	g := newPwRig(t)
	// Signed in with a link (no password yet).
	tok, err := g.ks.NewSession(g.req("GET", "/", "", "10.0.0.5", nil), SessionByLink, false)
	require.NoError(t, err)
	me := &http.Cookie{Name: g.ks.cookieBase, Value: tok}

	// Set: needs a session (or the CLI), and an Origin.
	w, out := g.do(g.ar.SetPassword, g.req("POST", "/api/auth/password/set", `{"password":"first"}`, "10.0.0.5", nil))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "password_session_required", out["code"], "never dashboard_auth_required (#52473)")
	r := g.req("POST", "/api/auth/password/set", `{"password":"first"}`, "10.0.0.5", me)
	r.Header.Del("Origin")
	w, out = g.do(g.ar.SetPassword, r)
	assert.Equal(t, http.StatusForbidden, w.Code, "Security 2: a cookie route needs an Origin")
	assert.Equal(t, "cross_origin", out["code"])
	w, out = g.do(g.ar.SetPassword, g.req("POST", "/api/auth/password/set", `{"password":""}`, "10.0.0.5", me))
	assert.Equal(t, "empty", out["code"])
	w, _ = g.do(g.ar.SetPassword, g.req("POST", "/api/auth/password/set", `{"password":"first"}`, "10.0.0.5", me))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	w, out = g.do(g.ar.SetPassword, g.req("POST", "/api/auth/password/set", `{"password":"second"}`, "10.0.0.5", me))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "password_set", out["code"])

	// Two other browsers sign in.
	var others []*http.Cookie
	for _, ua := range []string{"Mozilla/5.0 (X11; Linux x86_64) Firefox/131.0", "<img src=x onerror=alert(1)>\x1b[0m"} {
		r := g.req("POST", "/auth/password", `{"password":"first"}`, "10.0.0.6", nil)
		r.Header.Set("User-Agent", ua)
		w, _ := g.do(g.ar.SignInWithPassword, r)
		others = append(others, sessionCookie(t, w))
	}
	// Sessions: three, this one marked; the hostile label is plain text; recent attempts.
	w, out = g.do(g.ar.Sessions, g.req("GET", "/api/auth/sessions", "", "10.0.0.5", me))
	require.Equal(t, http.StatusOK, w.Code)
	list := out["sessions"].([]any)
	assert.Len(t, list, 3)
	current := 0
	for _, s := range list {
		m := s.(map[string]any)
		if m["current"] == true {
			current++
		}
		assert.NotContains(t, m["label"], "\x1b")
		assert.Len(t, m["id"], 12)
	}
	assert.Equal(t, 1, current)
	assert.Contains(t, w.Body.String(), `"label":"\u003cimg src=x onerror=alert(1)\u003e[0m"`, "the client's text, as data: escaped JSON, rendered with textContent")
	assert.Len(t, out["recent"], 2)
	assert.NotContains(t, w.Body.String(), "first", "never the password")

	// Change: wrong current is 403; right ends every other session and renews this one.
	w, out = g.do(g.ar.ChangePassword, g.req("POST", "/api/auth/password/change", `{"current":"nope","password":"third"}`, "10.0.0.5", me))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "wrong_password", out["code"])
	w, out = g.do(g.ar.ChangePassword, g.req("POST", "/api/auth/password/change", `{"current":"first","password":"third"}`, "10.0.0.5", me))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.EqualValues(t, 2, out["signed_out"])
	renewed := sessionCookie(t, w)
	assert.False(t, g.ks.ValidateSession(me.Value), "this browser's old cookie ended")
	assert.True(t, g.ks.ValidateSession(renewed.Value))
	for _, c := range others {
		assert.False(t, g.ks.ValidateSession(c.Value))
	}
	phc, _ := ReadPasswordHash(g.ks.dataDir())
	assert.True(t, VerifyPassword(phc, "third"))

	// Sign out others.
	r = g.req("POST", "/auth/password", `{"password":"third"}`, "10.0.0.6", nil)
	w, _ = g.do(g.ar.SignInWithPassword, r)
	other := sessionCookie(t, w)
	w, out = g.do(g.ar.SignOutOthers, g.req("POST", "/api/auth/sessions/sign-out-others", `{}`, "10.0.0.5", renewed))
	require.Equal(t, http.StatusOK, w.Code)
	assert.EqualValues(t, 1, out["signed_out"])
	assert.False(t, g.ks.ValidateSession(other.Value))
	assert.True(t, g.ks.ValidateSession(renewed.Value))

	// Reset: the command line's signature only.
	w, out = g.do(g.ar.ResetPassword, g.req("POST", "/api/auth/password/reset", `{"password":"fourth"}`, "127.0.0.1", renewed))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "cli_only", out["code"])
	g.cli = true
	w, out = g.do(g.ar.ResetPassword, g.req("POST", "/api/auth/password/reset", `{"password":"fourth"}`, "127.0.0.1", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.False(t, g.ks.ValidateSession(renewed.Value), "a reset ends every session")
	w, _ = g.do(g.ar.ResetPassword, g.req("POST", "/api/auth/password/reset", `{"clear":true}`, "127.0.0.1", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.False(t, g.ks.PasswordSet())
}

// A session ends at its absolute expiry however recently it was used.
func TestSessionExpiryIsAbsolute(t *testing.T) {
	g := newPwRig(t)
	tok, _ := g.ks.NewSession(g.req("GET", "/", "", "10.0.0.5", nil), SessionByPassword, true)
	s, _, _ := g.ks.backend().GetSession(t.Context(), sessionHash(tok))
	assert.WithinDuration(t, time.Now().Add(sessionKeepAge), s.ExpiresAt, time.Minute)
	s.LastSeen = time.Now()
	s.ExpiresAt = time.Now().Add(-time.Second)
	require.NoError(t, g.ks.backend().PutSession(t.Context(), s))
	assert.False(t, g.ks.ValidateSession(tok))
}

// Creating the first password on the sign-in page: from this computer with no code, from anywhere
// else only with the setup code; one creation wins.
func TestCreatePasswordOnTheSignInPage(t *testing.T) {
	g := newPwRig(t)
	g.ar.SetPeerUser(func(*http.Request) (bool, bool) { return true, true })
	code := g.ks.SetupCode()
	require.NotEmpty(t, code)

	// Another computer: no code, a wrong one, then the right one.
	w, out := g.do(g.ar.CreatePassword, g.req("POST", "/auth/password/create", `{"password":"pw"}`, "10.0.0.5", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "bad_code", out["code"])
	w, out = g.do(g.ar.CreatePassword, g.req("POST", "/auth/password/create", `{"password":"pw","code":"AAAA-AAAA"}`, "10.0.0.5", nil))
	assert.Equal(t, "bad_code", out["code"])
	r := g.req("POST", "/auth/password/create", `{"password":"pw","code":"`+FormatSetupCode(code)+`"}`, "10.0.0.5", nil)
	r.Header.Del("Origin")
	w, out = g.do(g.ar.CreatePassword, r)
	assert.Equal(t, "cross_origin", out["code"], "Origin required")
	w, _ = g.do(g.ar.CreatePassword, g.req("POST", "/auth/password/create", `{"password":"pw","keep":true,"code":"`+FormatSetupCode(code)+`"}`, "10.0.0.5", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	c := sessionCookie(t, w)
	assert.Equal(t, int(sessionKeepAge.Seconds()), c.MaxAge)
	assert.True(t, g.ks.ValidateSession(c.Value))
	assert.Equal(t, "", g.ks.SetupCode(), "used: no code while a password is set")
	w, out = g.do(g.ar.CreatePassword, g.req("POST", "/auth/password/create", `{"password":"again","code":"`+FormatSetupCode(code)+`"}`, "10.0.0.6", nil))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "password_set", out["code"])

	// This computer (same user, positively): no code needed, unless it is an agent's tool.
	g2 := newPwRig(t)
	g2.ar.SetPeerUser(func(*http.Request) (bool, bool) { return true, true })
	r = g2.req("POST", "/auth/password/create", `{"password":"pw"}`, "127.0.0.1", nil)
	r.Header.Set(agentTokenHeader, "an-agent-token")
	w, out = g2.do(g2.ar.CreatePassword, r)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "forbidden_for_agent", out["code"])
	assert.False(t, g2.ks.PasswordSet())
	w, _ = g2.do(g2.ar.CreatePassword, g2.req("POST", "/auth/password/create", `{"password":"pw"}`, "127.0.0.1", nil))
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// This computer but the peer check can't tell, another user, a Hub stamp, or a proxy header:
	// the code is required.
	for name, tc := range map[string]struct {
		same, supported bool
		hdr             string
	}{
		"unsupported": {false, false, ""}, "another user": {false, true, ""},
		"hub stamp": {true, true, "X-Coral-Via-Tunnel"}, "proxy": {true, true, "X-Forwarded-For"},
	} {
		g3 := newPwRig(t)
		same, supported := tc.same, tc.supported
		g3.ar.SetPeerUser(func(*http.Request) (bool, bool) { return same, supported })
		r := g3.req("POST", "/auth/password/create", `{"password":"pw"}`, "127.0.0.1", nil)
		if tc.hdr != "" {
			r.Header.Set(tc.hdr, "x")
		}
		w, out := g3.do(g3.ar.CreatePassword, r)
		assert.Equal(t, "bad_code", out["code"], name)
		assert.Equal(t, http.StatusUnauthorized, w.Code, name)
	}

	// The command line reads the code; a browser can't.
	g4 := newPwRig(t)
	w, out = g4.do(g4.ar.GetSetupCode, g4.req("GET", "/api/auth/setup-code", "", "127.0.0.1", nil))
	assert.Equal(t, http.StatusForbidden, w.Code)
	g4.cli = true
	w, out = g4.do(g4.ar.GetSetupCode, g4.req("GET", "/api/auth/setup-code", "", "127.0.0.1", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, FormatSetupCode(g4.ks.SetupCode()), out["code"])
}

// No plaintext anywhere: after setting, signing in and failing, the password is in no file of the
// data folder.
func TestPasswordNeverStoredInPlaintext(t *testing.T) {
	g := newPwRig(t)
	const secret = "plaintext-canary-3949"
	g.ar.SetPeerUser(func(*http.Request) (bool, bool) { return true, true })
	g.do(g.ar.CreatePassword, g.req("POST", "/auth/password/create", `{"password":"`+secret+`"}`, "127.0.0.1", nil))
	g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", `{"password":"`+secret+`"}`, "10.0.0.5", nil))
	g.do(g.ar.SignInWithPassword, g.req("POST", "/auth/password", `{"password":"`+secret+`x"}`, "10.0.0.5", nil))
	require.True(t, g.ks.PasswordSet())
	_ = filepath.Walk(g.ks.dataDir(), func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			assert.NotContains(t, string(b), secret, p)
		}
		return nil
	})
}

// Security's conditions on the page (#3948/#3949): every state of GET /auth carries no-store, a
// CSP with frame-ancestors 'none' and scripts from this server only, X-Frame-Options DENY and
// no-referrer; the setup-code field is shown only when the server says it is needed.
func TestAuthPageHeadersInEveryState(t *testing.T) {
	for name, tc := range map[string]struct {
		set, sameUser bool
		addr          string
		wantCode      bool
	}{
		"create, this computer":    {false, true, "127.0.0.1", false},
		"create, another computer": {false, true, "10.0.0.5", true},
		"create, unknown peer":     {false, false, "127.0.0.1", true},
		"sign in":                  {true, true, "10.0.0.5", false},
	} {
		g := newPwRig(t)
		same := tc.sameUser
		g.ar.SetPeerUser(func(*http.Request) (bool, bool) { return same, same })
		g.ar.SetStrictLocal(true)
		if tc.set {
			g.setPassword("pw")
		}
		w := httptest.NewRecorder()
		g.ar.AuthPage(w, g.req("GET", "/auth", "", tc.addr, nil))
		h := w.Header()
		assert.Equal(t, http.StatusOK, w.Code, name)
		assert.Equal(t, "no-store", h.Get("Cache-Control"), name)
		csp := h.Get("Content-Security-Policy")
		assert.Contains(t, csp, "default-src 'self'", name)
		assert.Contains(t, csp, "frame-ancestors 'none'", name)
		assert.NotContains(t, csp, "script-src", name+": scripts fall back to 'self'")
		assert.Equal(t, "DENY", h.Get("X-Frame-Options"), name)
		assert.Equal(t, "no-referrer", h.Get("Referrer-Policy"), name)
		assert.Equal(t, tc.wantCode, strings.Contains(w.Body.String(), `id="setup-code"`), name)
		assert.NotContains(t, w.Body.String(), "<script>", name+": no inline script")
	}
}

// DevOps's #52473 (Security's ruling): every browser sign-in (link, key form, password) may list
// sessions and sign out others; with no browser session the answer is 403 password_session_required,
// never 401 dashboard_auth_required (which the dashboard takes as "signed out"), in every mode,
// sets no cookie, and reads the same whether or not a password exists.
func TestSettingsSessionsWorkForEverySignIn(t *testing.T) {
	g := newPwRig(t)
	link, err := g.ks.NewSession(g.req("GET", "/", "", "10.0.0.5", nil), SessionByLink, false)
	require.NoError(t, err)
	key, err := g.ks.NewSession(g.req("GET", "/", "", "10.0.0.6", nil), SessionByKey, false)
	require.NoError(t, err)
	for name, tok := range map[string]string{"link": link, "key": key} {
		c := &http.Cookie{Name: g.ks.cookieBase, Value: tok}
		w, out := g.do(g.ar.Sessions, g.req("GET", "/api/auth/sessions", "", "10.0.0.5", c))
		require.Equal(t, http.StatusOK, w.Code, "%s: %v", name, out)
	}
	w, out := g.do(g.ar.SignOutOthers, g.req("POST", "/api/auth/sessions/sign-out-others", `{}`, "10.0.0.5", &http.Cookie{Name: g.ks.cookieBase, Value: link}))
	require.Equal(t, http.StatusOK, w.Code, "%v", out)
	assert.False(t, g.ks.ValidateSession(key))

	noSession := func() map[string]string {
		bodies := map[string]string{}
		for _, strict := range []bool{false, true} {
			g.ar.SetStrictLocal(strict)
			for _, c := range []struct {
				name string
				h    http.HandlerFunc
				m, p string
			}{
				{"sessions", g.ar.Sessions, "GET", "/api/auth/sessions"},
				{"sign-out-others", g.ar.SignOutOthers, "POST", "/api/auth/sessions/sign-out-others"},
				{"change", g.ar.ChangePassword, "POST", "/api/auth/password/change"},
				{"set", g.ar.SetPassword, "POST", "/api/auth/password/set"},
			} {
				for _, addr := range []string{"127.0.0.1", "10.0.0.9"} {
					w, out := g.do(c.h, g.req(c.m, c.p, `{"current":"x","password":"y"}`, addr, nil))
					assert.Equal(t, http.StatusForbidden, w.Code, "%s strict=%v %s", c.name, strict, addr)
					assert.Equal(t, "password_session_required", out["code"], "%s strict=%v %s", c.name, strict, addr)
					assert.Empty(t, w.Result().Cookies(), c.name)
					bodies[c.name] = w.Body.String()
				}
			}
		}
		return bodies
	}
	without := noSession()
	g.setPassword("pw")
	with := noSession()
	assert.Equal(t, without, with, "the answer doesn't tell whether a password exists")
}

// The setup code where a person is (#6410, Security's #6408 rank 1 option A): Settings, the tray and the app show the code the terminal
// prints, to a browser session signed in on this computer. The same code and expiry as the command line's; an agent, a request through Coral Hub's
// tunnel, a session that is not on this computer, and a request with no session get nothing; once a password is set there is no code; and the
// code is never written to the log.
func TestShowSetupCodeToTheSignedInPersonOnThisComputer(t *testing.T) {
	var logged strings.Builder
	prev := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(prev) })

	g := newPwRig(t)
	g.ar.SetPeerUser(func(*http.Request) (bool, bool) { return true, true })
	tok, err := g.ks.NewSession(g.req("GET", "/", "", "127.0.0.1", nil), SessionByLink, false)
	require.NoError(t, err)
	me := &http.Cookie{Name: g.ks.cookieBase, Value: tok}
	show := func(mod func(*http.Request), addr string, cookie *http.Cookie) (*httptest.ResponseRecorder, map[string]any) {
		r := g.req("GET", "/api/auth/setup-code/show", "", addr, cookie)
		if mod != nil {
			mod(r)
		}
		return g.do(g.ar.ShowSetupCode, r)
	}

	w, out := show(nil, "127.0.0.1", me)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, FormatSetupCode(g.ks.SetupCode()), out["code"], "the same code the command line prints")
	left, _ := out["expires_in"].(float64)
	assert.True(t, left > 0 && left <= setupCodeTTL.Seconds(), "the same expiry: %v", out["expires_in"])
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))

	w, out = show(nil, "127.0.0.1", nil)
	assert.Equal(t, http.StatusForbidden, w.Code, "no session")
	assert.Equal(t, "password_session_required", out["code"])
	w, _ = show(func(r *http.Request) { r.Header.Set(agentTokenHeader, "agent") }, "127.0.0.1", me)
	assert.Equal(t, http.StatusForbidden, w.Code, "an agent's token")
	w, _ = show(func(r *http.Request) { r.Header.Set("X-Coral-Via-Tunnel", "1") }, "127.0.0.1", me)
	assert.Equal(t, http.StatusForbidden, w.Code, "through Coral Hub's tunnel")
	w, out = show(nil, "10.0.0.5", me)
	assert.Equal(t, http.StatusForbidden, w.Code, "a signed-in browser that is not on this computer")
	assert.NotContains(t, w.Body.String(), FormatSetupCode(g.ks.SetupCode()))
	assert.Equal(t, "this_computer_only", out["code"])
	w, _ = show(func(r *http.Request) { r.Header.Set("X-Forwarded-For", "203.0.113.9") }, "127.0.0.1", me)
	assert.Equal(t, http.StatusForbidden, w.Code, "a proxy header")

	assert.NotContains(t, logged.String(), g.ks.SetupCode(), "the code is never logged")
	assert.NotContains(t, logged.String(), FormatSetupCode(g.ks.SetupCode()))

	// a password is set: there is no code to show
	cw, _ := g.do(g.ar.CreatePassword, g.req("POST", "/auth/password/create", `{"password":"a password"}`, "127.0.0.1", nil))
	require.Equal(t, http.StatusOK, cw.Code, cw.Body.String())
	w, out = show(nil, "127.0.0.1", me)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "password_set", out["code"])
}
