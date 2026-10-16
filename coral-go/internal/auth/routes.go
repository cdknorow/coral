package auth

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/cdknorow/coral/internal/httputil"
)

// Routes provides HTTP handlers for authentication endpoints.
type Routes struct {
	ks *KeyStore
	// strictLocal: strict local auth is on (#3418), so /auth on this computer shows the
	// locked page instead of the API-key form.
	strictLocal bool
}

// SetStrictLocal makes /auth show LockedPageHTML to a browser on this computer that isn't
// signed in (strict local auth, #3418). Other computers keep the API-key page.
func (ar *Routes) SetStrictLocal(on bool) { ar.strictLocal = on }

// NewRoutes creates auth route handlers.
func NewRoutes(ks *KeyStore) *Routes {
	return &Routes{ks: ks}
}

// AuthPage serves the authentication page.
// GET /auth
func (ar *Routes) AuthPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if ar.strictLocal && localOperator(r) && !ar.ks.HasValidSession(r) {
		// 200, so the Coral app's window renders it; the app signs in again from /auth.
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(LockedPageHTML))
		return
	}
	w.Write([]byte(authPageHTML))
}

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

	token, err := ar.ks.CreateSession(ip, r.UserAgent())
	if err != nil {
		log.Printf("[auth] failed to create session: %v", err)
		httputil.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to create session"})
		return
	}
	ar.ks.setSessionCookie(w, r, token)
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
	if !localOperator(r) {
		httputil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "localhost only"})
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"key": ar.ks.Key()})
}

// RegenerateKey generates a new API key (localhost only).
// POST /api/system/api-key/regenerate
func (ar *Routes) RegenerateKey(w http.ResponseWriter, r *http.Request) {
	if !localOperator(r) {
		httputil.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "localhost only"})
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

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"authenticated": authenticated,
		"method":        method,
	})
}
