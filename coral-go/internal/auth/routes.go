package auth

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/subgentic/coral/internal/httputil"
)

// Routes provides HTTP handlers for authentication endpoints.
type Routes struct {
	ks *KeyStore
	// strictLocal: strict local auth is on (#3418), so /auth on this computer shows the
	// locked page instead of the API-key form.
	strictLocal bool
	// The password (#3949): its throttle, the recent attempts, and how a handler knows the
	// command line's signature was verified (cliauth sits above this package).
	throttle *passwordThrottle
	audit    *signInAudit
	// pwGen counts the replacements of the password (change, reset): a password sign-in that began before one must not leave a session after it.
	pwGen       atomic.Uint64
	cliVerified func(*http.Request) bool
	// afterReset runs once a reset from the command line has changed the password (#4041:
	// the server rotates its command-line token, since a reset means "don't trust what was
	// out there").
	afterReset func()
	peerUser   func(*http.Request) (same, supported bool)
	// hub is Sign in with Coral Hub (#3966), nil when the server has none.
	hub *HubSignin
	// isOperator: the identity layer decided this request is the Operator's (the dashboard
	// sign-in, the API key, the signed command line). nil refuses every request (fail
	// closed: a server that did not wire the check shows no key). With strict local auth off a bare local request is not one (#4290).
	isOperator func(*http.Request) bool
}

// SetOperatorCheck sets how the key routes ask whether the identity layer verified the Operator.
func (ar *Routes) SetOperatorCheck(f func(*http.Request) bool) { ar.isOperator = f }

// keyRoutesRefuse writes the refusal and returns true unless r may read or rotate the API key: a
// request straight from this computer (never through Coral Hub) that the identity layer verified as
// the Operator, whatever strict local auth says (#4290, Security's SHOULD 3 in #4288).
func (ar *Routes) keyRoutesRefuse(w http.ResponseWriter, r *http.Request) bool {
	if !localPower(r) {
		httputil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "localhost only"})
		return true
	}
	if ar.isOperator == nil || !ar.isOperator(r) { // no check wired: refuse (fail closed)
		httputil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "the API key is shown only to a signed-in Operator: sign in (coral open) or send the key or a command-line signature", "code": "operator_only"})
		return true
	}
	return false
}

// SetStrictLocal makes /auth show LockedPageHTML to a browser on this computer that isn't
// signed in (strict local auth, #3418). Other computers keep the API-key page.
func (ar *Routes) SetStrictLocal(on bool) { ar.strictLocal = on }

// NewRoutes creates auth route handlers.
func NewRoutes(ks *KeyStore) *Routes {
	return &Routes{ks: ks, throttle: newPasswordThrottle(), audit: &signInAudit{}, cliVerified: func(*http.Request) bool { return false }, peerUser: func(*http.Request) (bool, bool) { return false, false }}
}

// AuthPage serves the authentication page.
// GET /auth
func (ar *Routes) AuthPage(w http.ResponseWriter, r *http.Request) {
	// One page for every state (#3948/#3949): sign in, or create the password (with the setup code
	// from another computer). The server decides the state; the create route checks it again per
	// request. 200 in every state, so the Coral app's window renders it and signs in again.
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", authPageCSP)
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	set, local, strict := ar.PageState(r)
	w.Write([]byte(AuthPage(PageState{PasswordSet: set, Local: local, Strict: strict, HubSignin: ar.hub.PageState(r), HubURL: ar.hub.HubURL()})))
}

// authPageCSP: scripts only from this server (the page has no inline script), never framed;
// inline style is allowed for the page's own <style> block.
const authPageCSP = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"base-uri 'none'; form-action 'self'; object-src 'none'; frame-ancestors 'none'"

// ValidateKey validates an API key and sets a session cookie.
// POST /auth/key
func (ar *Routes) ValidateKey(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB limit
	var body struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	// Only wrong keys count against the limit (as in Middleware): a user who signs in often,
	// or several people behind one address, never get a 429 for using the right key.
	ip := clientIP(r)
	if ar.ks.RateLimited(ip) {
		httputil.WriteActionLimit(w, 0, "too_many_attempts", "Too many attempts. Try again later.") // #3434
		return
	}

	if !ar.ks.ValidateKey(body.Key) {
		ar.ks.RecordFailure(ip)
		httputil.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "Invalid API key"})
		return
	}

	token, err := ar.ks.NewSessionRotating(r, SessionByKey, false)
	if err != nil {
		log.Printf("[auth] failed to create session: %v", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create session"})
		return
	}
	ar.ks.setSessionCookie(w, r, token, false)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Logout ends the caller's session and clears its cookie (#3356). Only a same-origin request
// is honoured (a page on another site can't sign the user out); one without a session just
// gets the cookie cleared.
// POST /auth/logout
func (ar *Routes) Logout(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		if u, err := url.Parse(origin); err != nil || !strings.EqualFold(u.Host, r.Host) {
			httputil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin sign-out refused"})
			return
		}
	}
	ar.ks.signOut(w, r)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// GetAPIKey returns the API key (localhost only).
// GET /api/system/api-key
func (ar *Routes) GetAPIKey(w http.ResponseWriter, r *http.Request) {
	if ar.keyRoutesRefuse(w, r) {
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"key": ar.ks.Key()})
}

// RegenerateKey generates a new API key (localhost only).
// POST /api/system/api-key/regenerate
func (ar *Routes) RegenerateKey(w http.ResponseWriter, r *http.Request) {
	if ar.keyRoutesRefuse(w, r) {
		return
	}
	newKey, err := ar.ks.RegenerateKey()
	if err != nil {
		log.Printf("[auth] failed to regenerate key: %v", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to regenerate key"})
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"key": newKey})
}

// AuthStatus returns the current authentication status.
// GET /api/system/auth-status
func (ar *Routes) AuthStatus(w http.ResponseWriter, r *http.Request) {
	method := "none"
	authenticated := false

	// In strict mode (the default since #3936) locality alone is not a credential, so it is
	// never reported as one.
	if !ar.strictLocal && localOperator(r) {
		method = "localhost"
		authenticated = true
	} else if key := extractAPIKey(r); key != "" && ar.ks.ValidateKey(key) {
		method = "key"
		authenticated = true
	} else if token := ar.ks.extractSessionCookie(r); token != "" && ar.ks.ValidateSession(token) {
		method = "session"
		authenticated = true
	}

	// password_set and local (#3949): the dashboard asks for a password, or says "Not now" for
	// good, from these; local is the server's own check, never the page's hostname.
	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"authenticated": authenticated,
		"method":        method,
		"password_set":  ar.ks.PasswordSet(),
		"local":         localOperator(r),
		// strict_local_auth (#4290): false means any program on this computer can use Coral
		// without a credential; the dashboard says so in a banner.
		"strict_local_auth": ar.strictLocal,
	})
}
