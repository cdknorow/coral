package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/secfile"
)

// Dashboard login tokens (#3387, identity phase 1). Like Jupyter's: a single-use
// token, valid for LoginTokenTTL, that a link carries (/?login_token=…). Opening it
// trades the token for the usual session cookie and redirects to the same page without
// it. Coral mints one at startup (written to <data>/login_token, 0600) and on request
// for `coral open`. In phase 1 the dashboard still works on this computer without the
// cookie; the identity report counts those requests.

// LoginTokenTTL is how long an unused login token is valid.
const LoginTokenTTL = 10 * time.Minute

// loginTokenTTL is LoginTokenTTL, or in a dev build only, a shorter
// CORAL_TEST_LOGIN_TOKEN_TTL (a Go duration, at most LoginTokenTTL) so the stress suite
// can see a link expire (#3387).
func loginTokenTTL() time.Duration {
	if config.TierName == "dev" {
		if d, err := time.ParseDuration(os.Getenv("CORAL_TEST_LOGIN_TOKEN_TTL")); err == nil && d > 0 && d < LoginTokenTTL {
			return d
		}
	}
	return LoginTokenTTL
}

// loginFileExpiry is when the login_token file of an unused token is removed: when the token
// expires. Tests shorten it.
var loginFileExpiry = loginTokenTTL

// LoginTokenParam is the query parameter that carries a login token.
const LoginTokenParam = "login_token"

// AppClientParam is the query parameter the Coral app adds to the sign-in link it opens (AppClientApp as its value): the session that link makes
// is named "Coral app on ..." on the Sessions page (#6100), so a person can tell it from their own browser and sign it out. Only a name.
const (
	AppClientParam = "coral_client"
	AppClientApp   = "app"
)

// LoginTokenFile is the file in the data folder that holds the startup token.
const LoginTokenFile = "login_token"

func hashLoginToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// MintLoginToken returns a new single-use login token.
func (ks *KeyStore) MintLoginToken() (string, error) {
	token, err := generateToken(64)
	if err != nil {
		return "", err
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	if ks.loginTokens == nil {
		ks.loginTokens = map[string]time.Time{}
	}
	now := time.Now()
	for h, exp := range ks.loginTokens {
		if now.After(exp) {
			delete(ks.loginTokens, h)
		}
	}
	ks.loginTokens[hashLoginToken(token)] = now.Add(loginTokenTTL())
	return token, nil
}

// consumeLoginToken reports whether token is a valid, unused, unexpired login token,
// and uses it up.
func (ks *KeyStore) consumeLoginToken(token string) bool {
	if token == "" {
		return false
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	h := hashLoginToken(token)
	exp, ok := ks.loginTokens[h]
	delete(ks.loginTokens, h)
	if ok && h == ks.loginFileHash {
		ks.removeLoginFileLocked() // used: the file's token is worth nothing now
	}
	return ok && time.Now().Before(exp)
}

// removeLoginFileLocked removes the login_token file when it still holds the token Coral
// wrote (a newer file, or one a person replaced, stays), and forgets it. ks.mu is held.
func (ks *KeyStore) removeLoginFileLocked() {
	path, h := ks.loginFile, ks.loginFileHash
	ks.loginFile, ks.loginFileHash = "", ""
	if path == "" {
		return
	}
	if b, err := os.ReadFile(path); err == nil && hashLoginToken(strings.TrimSpace(string(b))) == h {
		os.Remove(path)
	}
}

// WriteLoginTokenFile mints a token and writes it to <dir>/login_token (0600),
// replacing an older one. The file is removed once its token is used or has expired
// (#3628 F7): a spent token on disk only invites a look.
func (ks *KeyStore) WriteLoginTokenFile(dir string) (string, error) {
	// A file an earlier run left goes first, so a failure below leaves no spent token (#3628 F7).
	RemoveLoginTokenFile(dir)
	token, err := ks.MintLoginToken()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, LoginTokenFile)
	tmp, err := os.CreateTemp(dir, ".login_token-*") // 0600
	if err != nil {
		return "", err
	}
	if err := secfile.Protect(tmp.Name()); err != nil { // Windows ignores the mode (#3905)
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if _, err := tmp.WriteString(token + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	h := hashLoginToken(token)
	ks.mu.Lock()
	ks.loginFile, ks.loginFileHash = path, h
	ks.mu.Unlock()
	time.AfterFunc(loginFileExpiry(), func() {
		ks.mu.Lock()
		defer ks.mu.Unlock()
		if ks.loginFileHash == h {
			ks.removeLoginFileLocked() // expired unused
		}
	})
	return token, nil
}

// RemoveLoginTokenFile removes <dir>/login_token: at a clean shutdown (no token of a stopped
// server is any use) and before a new one is written (#3628 F7, Security's SHOULD). A
// symlink at the path is removed, never followed.
func RemoveLoginTokenFile(dir string) {
	if dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(dir, LoginTokenFile)) // gone already is fine
}

// handleLoginToken trades a ?login_token= for a session cookie (SameSite Strict) and
// redirects to the same URL without it. A used or expired token gets 403. It reports
// whether it answered the request.
func (ks *KeyStore) handleLoginToken(w http.ResponseWriter, r *http.Request) bool {
	token := r.URL.Query().Get(LoginTokenParam)
	if token == "" || r.Method != http.MethodGet {
		return false
	}
	// Another account on this computer may have seen the link (an opener's argv is visible in
	// ps): it can't use it. Checked before the token is used, so the owner's link still works
	// (#3970, Security's #3967 finding 1).
	if ks.peerUser != nil {
		same, supported := ks.peerUser(r)
		switch {
		case supported && !same:
			log.Printf("[auth] a sign-in link was opened by another user of this computer: refused (the link still works for you)")
			http.Error(w, "This sign-in link was already used or has expired. Run 'coral open' on this computer for a new one.", http.StatusForbidden)
			return true
		case !supported:
			ks.unknownPeer.Do(func() {
				log.Printf("[auth] this system can't tell which user opened a sign-in link; links work as before")
			})
		}
	}
	if !ks.consumeLoginToken(token) {
		http.Error(w, "This sign-in link was already used or has expired. Run 'coral open' on this computer for a new one.", http.StatusForbidden)
		return true
	}
	session, err := ks.NewSessionRotating(r, SessionByLink, false)
	if err != nil {
		http.Error(w, "could not start a session", http.StatusInternalServerError)
		return true
	}
	ks.setSessionCookie(w, r, session, false)
	// The same page without the token (a relative URL: never another host), its path exactly as
	// sent: the dashboard's pages carry names in the path, and a decoded one would change what the
	// link means (%40 would read as the @server marker, %2F as a segment; #3532).
	q := r.URL.Query()
	q.Del(LoginTokenParam)
	q.Del(AppClientParam)
	target := (&url.URL{Path: r.URL.Path, RawPath: r.URL.RawPath, RawQuery: q.Encode()}).String()
	// One leading "/": "//host/..." (or "/\host", which browsers read the same) would be
	// another host (Security, #3532).
	target = "/" + strings.TrimLeft(target, "/\\")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusFound)
	return true
}
