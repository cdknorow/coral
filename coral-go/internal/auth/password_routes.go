package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/httputil"
)

// The password's routes (#3949). All are POST and same-origin (Sec-Fetch-Site, when sent, is
// same-origin); the cookie-authenticated ones also need an Origin naming this server (Security
// 2). None is in Coral Hub's tunnel table, and an agent's token never signs into any of them.

// agentTokenHeader is an agent session's token (store.SessionTokenHeader; store imports this
// package). A request carrying one is an agent's tool and never signs a browser in or creates
// the password (Security, #3949 round 1: on Linux the peer check would call it this computer).
const agentTokenHeader = "X-Coral-Session-Token"

func refuseAgent(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get(agentTokenHeader) == "" {
		return false
	}
	authError(w, http.StatusForbidden, "forbidden_for_agent", "An agent can't sign in or set the password: that is for the Operator, in a browser or with coral set-password")
	return true
}

// maxPasswordBody bounds a password request's body.
const maxPasswordBody = 16 << 10

// SetCLIVerified tells the routes how to recognise a request whose command-line signature the
// server checked (cliauth.Verified): the signed `coral set-password` and `coral reset-password`.
func (ar *Routes) SetCLIVerified(f func(*http.Request) bool) {
	if f != nil {
		ar.cliVerified = f
	}
}

func (ks *KeyStore) dataDir() string { return filepath.Dir(ks.keyPath) }

// PasswordSet reports whether a password is set (and readable).
func (ks *KeyStore) PasswordSet() bool {
	if ks == nil {
		return false
	}
	phc, err := ReadPasswordHash(ks.dataDir())
	return err == nil && phc != ""
}

func authError(w http.ResponseWriter, status int, code, msg string) {
	httputil.WriteJSON(w, status, map[string]any{"error": msg, "code": code})
}

// sameOriginWrite applies the routes' origin rule: a cross-site Sec-Fetch-Site or a foreign
// Origin is refused; needOrigin also refuses a request with no Origin.
func sameOriginWrite(r *http.Request, needOrigin bool) bool {
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" && s != "same-origin" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return !needOrigin
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

func readPasswordBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPasswordBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		authError(w, http.StatusBadRequest, "bad_request", "The request could not be read")
		return false
	}
	return true
}

func tooMany(w http.ResponseWriter, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	// A per-action limit (X-Coral-Limit: action), so a dashboard never takes it for an overloaded server.
	httputil.WriteActionLimitBody(w, secs, map[string]any{
		"error": fmt.Sprintf("Too many attempts. Try again in %d s.", secs), "code": "too_many_attempts", "retry_after": secs,
	})
}

// checkPassword runs one throttled password check for r's address against the stored hash. It
// answers 429 itself (and returns false, false) when the address must wait or no slot is free.
func (ar *Routes) checkPassword(w http.ResponseWriter, r *http.Request, pw string) (match, answered bool) {
	ip := clientIP(r)
	if wait := ar.throttle.delay(ip); wait > 0 {
		tooMany(w, wait)
		return false, true
	}
	release, ok := ar.throttle.acquire(ip)
	if !ok {
		tooMany(w, slotWait)
		return false, true
	}
	defer release()
	phc, err := ReadPasswordHash(ar.ks.dataDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("[auth] the password file can't be read: %v", err)
		phc = ""
	}
	match = VerifyPassword(phc, pw)
	if match {
		ar.throttle.success(ip)
	} else {
		ar.throttle.fail(ip)
	}
	return match, false
}

// SignInWithPassword signs a browser in with the password.
// POST /auth/password {"password": "...", "keep": bool}
func (ar *Routes) SignInWithPassword(w http.ResponseWriter, r *http.Request) {
	if refuseAgent(w, r) {
		return
	}
	// A script may send it without an Origin (it carries the password itself); a browser page
	// on another site never gets a cookie.
	if !sameOriginWrite(r, false) {
		authError(w, http.StatusForbidden, "cross_origin", "Sign in from Coral's own page")
		return
	}
	var body struct {
		Password string `json:"password"`
		Keep     bool   `json:"keep"`
	}
	if !readPasswordBody(w, r, &body) {
		return
	}
	if len(body.Password) > MaxPasswordBytes {
		authError(w, http.StatusBadRequest, "too_long", fmt.Sprintf("The password is longer than %d bytes", MaxPasswordBytes))
		return
	}
	match, answered := ar.checkPassword(w, r, body.Password)
	if answered {
		return
	}
	label := sessionLabel(r.UserAgent())
	ar.audit.add(clientIP(r), label, match)
	if !match {
		log.Printf("[auth] wrong password from %s (%s)", clientIP(r), label)
		authError(w, http.StatusUnauthorized, "wrong_password", "Wrong password")
		return
	}
	// A new session every time: the cookie this browser had (if any) ends here.
	if old := ar.ks.extractSessionCookie(r); old != "" {
		ar.ks.RevokeSession(old)
	}
	token, err := ar.ks.NewSession(r, SessionByPassword, body.Keep)
	if err != nil {
		log.Printf("[auth] password sign-in: %v", err)
		authError(w, http.StatusInternalServerError, "session_failed", "Could not start a session")
		return
	}
	log.Printf("[auth] signed in with the password from %s (%s)", clientIP(r), label)
	ar.ks.setSessionCookie(w, r, token, body.Keep)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// signedIn reports a live browser session on r.
func (ar *Routes) signedIn(r *http.Request) bool { return ar.ks.HasValidSession(r) }

// notSignedIn answers a request that has no browser session where one is needed. It is 403
// password_session_required, never 401 dashboard_auth_required: the dashboard takes that code as "you are
// signed out" and covers the whole page, while this is one Settings action (DevOps's #52473:
// with strict local auth off, a dashboard on this computer has no session cookie at all). The
// routes stay session-only (Security): any browser session counts (a link, the key form or the
// password), never the CLI signature or the API key alone.
func notSignedIn(w http.ResponseWriter) {
	authError(w, http.StatusForbidden, "password_session_required", "Sign in to the dashboard in this browser first (the password, or 'coral open' on this computer)")
}

// hashNew validates and hashes a new password, holding a hash slot (it costs what a check
// costs). It answers the error itself.
func (ar *Routes) hashNew(w http.ResponseWriter, r *http.Request, pw string) (string, bool) {
	if _, err := normalizePassword(pw); err != nil {
		if errors.Is(err, ErrPasswordEmpty) {
			authError(w, http.StatusBadRequest, "empty", "The password is empty")
		} else {
			authError(w, http.StatusBadRequest, "too_long", fmt.Sprintf("The password is longer than %d bytes", MaxPasswordBytes))
		}
		return "", false
	}
	release, ok := ar.throttle.acquire(clientIP(r))
	if !ok {
		tooMany(w, slotWait)
		return "", false
	}
	defer release()
	phc, err := HashPassword(pw)
	if err != nil {
		authError(w, http.StatusInternalServerError, "hash_failed", "The password could not be stored")
		return "", false
	}
	return phc, true
}

// passwordWriteMu orders the writes that replace the password (change, reset); creating one
// is atomic on its own (CreatePasswordHash).
var passwordWriteMu sync.Mutex

// SetPassword sets the first password, from a signed-in browser or the signed command line.
// POST /api/auth/password/set {"password": "..."}
func (ar *Routes) SetPassword(w http.ResponseWriter, r *http.Request) {
	cli := ar.cliVerified(r)
	if !cli && !sameOriginWrite(r, true) {
		authError(w, http.StatusForbidden, "cross_origin", "Set the password from Coral's own page")
		return
	}
	if !cli && !ar.signedIn(r) {
		notSignedIn(w)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readPasswordBody(w, r, &body) {
		return
	}
	if ar.ks.PasswordSet() { // cheap answer first; CreatePasswordHash settles a race
		authError(w, http.StatusConflict, "password_set", "A password is already set: change it in Settings, or run 'coral reset-password' on this computer")
		return
	}
	phc, ok := ar.hashNew(w, r, body.Password)
	if !ok {
		return
	}
	if err := CreatePasswordHash(ar.ks.dataDir(), phc); err != nil {
		if errors.Is(err, ErrPasswordExists) {
			authError(w, http.StatusConflict, "password_set", "A password is already set: change it in Settings, or run 'coral reset-password' on this computer")
			return
		}
		log.Printf("[auth] couldn't save the password: %v", err)
		authError(w, http.StatusInternalServerError, "save_failed", "The password could not be saved")
		return
	}
	ar.ks.retireSetupCode()
	log.Printf("[auth] a password was set (from %s)", map[bool]string{true: "the command line", false: "the dashboard"}[cli])
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ChangePassword changes the password from a signed-in browser that knows the current one; every
// other session ends and this browser's is renewed.
// POST /api/auth/password/change {"current": "...", "password": "..."}
func (ar *Routes) ChangePassword(w http.ResponseWriter, r *http.Request) {
	if !sameOriginWrite(r, true) {
		authError(w, http.StatusForbidden, "cross_origin", "Change the password from Coral's own page")
		return
	}
	token := ar.ks.extractSessionCookie(r)
	cur, live := ar.ks.lookupSession(token)
	if !live {
		notSignedIn(w)
		return
	}
	var body struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if !readPasswordBody(w, r, &body) {
		return
	}
	if _, err := normalizePassword(body.Password); err != nil {
		ar.hashNew(w, r, body.Password) // answers empty / too_long
		return
	}
	match, answered := ar.checkPassword(w, r, body.Current)
	if answered {
		return
	}
	if !match {
		authError(w, http.StatusForbidden, "wrong_password", "The current password is wrong")
		return
	}
	phc, ok := ar.hashNew(w, r, body.Password)
	if !ok {
		return
	}
	passwordWriteMu.Lock()
	defer passwordWriteMu.Unlock()
	if err := WritePasswordHash(ar.ks.dataDir(), phc); err != nil {
		log.Printf("[auth] couldn't save the new password: %v", err)
		authError(w, http.StatusInternalServerError, "save_failed", "The password could not be saved")
		return
	}
	n, err := ar.ks.RevokeAllSessions()
	if err != nil {
		log.Printf("[auth] password changed, but the sessions couldn't be ended: %v", err)
	}
	newToken, err := ar.ks.NewSession(r, SessionByPassword, cur.Persistent)
	if err == nil {
		ar.ks.setSessionCookie(w, r, newToken, cur.Persistent)
	}
	if n > 0 {
		n-- // this browser's own session, renewed
	}
	log.Printf("[auth] the password was changed; %d other session(s) ended", n)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "signed_out": n})
}

// ResetPassword replaces or clears the password from the signed command line only
// (`coral reset-password`); every session ends.
// POST /api/auth/password/reset {"password": "..."} or {"clear": true}
func (ar *Routes) ResetPassword(w http.ResponseWriter, r *http.Request) {
	if !ar.cliVerified(r) {
		authError(w, http.StatusForbidden, "cli_only", "Reset the password with 'coral reset-password' on the computer running Coral")
		return
	}
	var body struct {
		Password string `json:"password"`
		Clear    bool   `json:"clear"`
	}
	if !readPasswordBody(w, r, &body) {
		return
	}
	passwordWriteMu.Lock()
	defer passwordWriteMu.Unlock()
	if body.Clear {
		if err := RemovePasswordHash(ar.ks.dataDir()); err != nil {
			authError(w, http.StatusInternalServerError, "save_failed", "The password could not be removed")
			return
		}
	} else {
		phc, ok := ar.hashNew(w, r, body.Password)
		if !ok {
			return
		}
		if err := WritePasswordHash(ar.ks.dataDir(), phc); err != nil {
			authError(w, http.StatusInternalServerError, "save_failed", "The password could not be saved")
			return
		}
	}
	n, err := ar.ks.RevokeAllSessions()
	if err != nil {
		log.Printf("[auth] couldn't end the sessions after a reset: %v", err)
	}
	log.Printf("[auth] password reset from the command line (cleared: %v); %d session(s) ended", body.Clear, n)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "signed_out": n})
}

// Sessions lists the signed-in browsers and the recent password attempts.
// GET /api/auth/sessions
func (ar *Routes) Sessions(w http.ResponseWriter, r *http.Request) {
	if !ar.signedIn(r) {
		notSignedIn(w)
		return
	}
	current := ar.ks.currentSessionHash(r)
	list, err := ar.ks.backend().ListSessions(r.Context())
	if err != nil {
		authError(w, http.StatusInternalServerError, "read_failed", "The sessions could not be read")
		return
	}
	now := time.Now()
	sessions := []map[string]any{}
	for _, s := range list {
		if !now.Before(s.ExpiresAt) {
			continue
		}
		sessions = append(sessions, map[string]any{
			"id": s.IDHash[:12], "label": s.Label, "method": s.Method,
			"created_at": rfc3339(s.CreatedAt), "last_seen": rfc3339(s.LastSeen), "expires_at": rfc3339(s.ExpiresAt),
			"persistent": s.Persistent, "current": s.IDHash == current,
		})
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "recent": ar.audit.list()})
}

// SignOutOthers ends every session but this browser's.
// POST /api/auth/sessions/sign-out-others
func (ar *Routes) SignOutOthers(w http.ResponseWriter, r *http.Request) {
	if !sameOriginWrite(r, true) {
		authError(w, http.StatusForbidden, "cross_origin", "Sign out other browsers from Coral's own page")
		return
	}
	if !ar.signedIn(r) {
		notSignedIn(w)
		return
	}
	n, err := ar.ks.RevokeOtherSessions(r)
	if err != nil {
		authError(w, http.StatusInternalServerError, "revoke_failed", "The other sessions could not be ended")
		return
	}
	log.Printf("[auth] %d other session(s) signed out", n)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "signed_out": n})
}

// RevokeSessionByID ends one listed session by its id (the 12-character prefix Sessions lists);
// its open WebSockets close with it.
// DELETE /api/auth/sessions/{id}
func (ar *Routes) RevokeSessionByID(w http.ResponseWriter, r *http.Request) {
	if !sameOriginWrite(r, true) {
		authError(w, http.StatusForbidden, "cross_origin", "Sign a browser out from Coral's own page")
		return
	}
	if !ar.signedIn(r) {
		notSignedIn(w)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/auth/sessions/")
	if len(id) != 12 || strings.Trim(id, "0123456789abcdef") != "" {
		authError(w, http.StatusBadRequest, "bad_id", "Unknown session")
		return
	}
	list, err := ar.ks.backend().ListSessions(r.Context())
	if err != nil {
		authError(w, http.StatusInternalServerError, "read_failed", "The sessions could not be read")
		return
	}
	var match []SessionRecord
	for _, s := range list {
		if strings.HasPrefix(s.IDHash, id) {
			match = append(match, s)
		}
	}
	if len(match) != 1 {
		authError(w, http.StatusNotFound, "not_found", "Unknown session")
		return
	}
	s := match[0]
	if s.Method == SessionByHub && ar.hub != nil {
		ar.hub.endSession(r.Context(), s, "signed out from Settings")
	} else if err := ar.ks.endSession(r.Context(), s.IDHash); err != nil {
		authError(w, http.StatusInternalServerError, "revoke_failed", "The session could not be ended")
		return
	}
	log.Printf("[auth] a browser session (%s) was signed out from Settings", s.Method)
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// signInAudit keeps the last 50 password attempts (never the password).
type signInAudit struct {
	mu      sync.Mutex
	entries []map[string]any // newest first
}

func (a *signInAudit) add(address, label string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := map[string]any{"at": rfc3339(time.Now()), "address": address, "label": label, "ok": ok}
	a.entries = append([]map[string]any{e}, a.entries...)
	if len(a.entries) > 50 {
		a.entries = a.entries[:50]
	}
}

func (a *signInAudit) list() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]map[string]any{}, a.entries...)
}
