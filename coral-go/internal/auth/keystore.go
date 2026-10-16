// Package auth provides API key authentication and session management for Coral.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/secfile"
)

const (
	keyLength = 32 // 32 hex chars = 128 bits
	// legacyCookieName is the session cookie of Coral before #3682: one name for every server on
	// the host, so two Corals on localhost signed each other out. It is still read (a session
	// is only ever valid on the server that made it), never written.
	legacyCookieName = "coral_session"
	rateLimitWindow  = time.Minute
	rateLimitMax     = 10
)

// Session represents an authenticated session.

// rateLimitEntry tracks failed auth attempts per IP.
type rateLimitEntry struct {
	count    int
	windowAt time.Time
}

// KeyStore manages API keys and sessions.
type KeyStore struct {
	cookieBase string // the session cookie's name without a prefix (#3682)
	mu         sync.RWMutex
	key        string
	keyPath    string
	sessions   SessionBackend // browser sign-ins (#3949): memory, or sessions.db once the server sets it
	rateMap    map[string]*rateLimitEntry
	trusted    []*net.IPNet // reverse proxies whose X-Forwarded-Proto is believed (#3356)
	// loginTokens maps a login token's SHA-256 to its expiry (#3387).
	loginTokens map[string]time.Time
	// loginFile and loginFileHash: the login_token file and the hash of the token it holds,
	// so the file is removed once that token is used or expires (#3628 F7).
	loginFile, loginFileHash string
	// setupCode creates the first password from another computer (#3949); "" once used.
	setupCode    string
	setupExpires time.Time
	setupWrong   int
	// peerUser is the peer-user check (#3970): a sign-in link opened by another OS user of this
	// computer is refused. nil, or "unsupported", keeps today's behaviour.
	peerUser    func(*http.Request) (same, supported bool)
	unknownPeer sync.Once
}

// NewKeyStore creates or loads an API key from the given directory.
func NewKeyStore(coralDir string) (*KeyStore, error) {
	ks := &KeyStore{
		cookieBase: sessionCookieBase(coralDir),
		keyPath:    filepath.Join(coralDir, "api_key"),
		sessions:   newMemSessions(),
		rateMap:    make(map[string]*rateLimitEntry),
	}
	if err := ks.loadOrGenerate(); err != nil {
		return nil, err
	}
	return ks, nil
}

func (ks *KeyStore) loadOrGenerate() error {
	data, err := readKeyFile(ks.keyPath)
	if errors.Is(err, secfile.ErrExposed) {
		// Another account could read it (Windows), so the key must be treated as known: a new
		// one replaces it, in a file only this user can read (#3905).
		log.Printf("[auth] %v; made a new API key (clients that kept the old one need the new one)", err)
		data, err = nil, fs.ErrNotExist
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil {
		key := strings.TrimSpace(string(data))
		if len(key) >= keyLength {
			ks.key = key
			return nil
		}
	}
	key, err := generateToken(keyLength)
	if err != nil {
		return fmt.Errorf("generate API key: %w", err)
	}
	ks.key = key
	if err := writeKeyFile(ks.keyPath, ks.key); err != nil {
		log.Printf("[auth] warning: failed to persist API key to disk: %v", err)
	}
	return nil
}

// ReadAPIKey reads the API key Coral keeps in coralDir (`coral api-key`, #3936), with the
// same checks as the server: a regular file, private to this user. It never makes a key: a
// data folder without one gets an fs.ErrNotExist error.
func ReadAPIKey(coralDir string) (string, error) {
	data, err := readKeyFile(filepath.Join(coralDir, "api_key"))
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("the API key file in %s is empty: %w", coralDir, fs.ErrNotExist)
	}
	return key, nil
}

// readKeyFile reads the API key file: a regular file (never through a symlink or another kind
// of file, refused with an error to fix by hand), at most 1 KiB. One readable by group or others
// is tightened to 0600 first, as it should always have been (#3627 F2). Windows has no mode bits:
// there the file's access list is checked instead (secfile.CheckOpen, #3905): one that lets
// another account read it is secfile.ErrExposed.
func readKeyFile(path string) ([]byte, error) {
	li, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !li.Mode().IsRegular() {
		return nil, fmt.Errorf("API key file %s is not a regular file (a link or something else); remove it and Coral makes a new key", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !os.SameFile(li, fi) {
		return nil, fmt.Errorf("API key file %s changed while it was opened", path)
	}
	if err := secfile.CheckOpen(f); err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		if err := f.Chmod(0o600); err != nil {
			return nil, fmt.Errorf("API key file %s is readable by others and can't be made private: %w", path, err)
		}
		log.Printf("[auth] the API key file was readable by others; it is now private (0600)")
	}
	return io.ReadAll(io.LimitReader(f, 1024))
}

// writeKeyFile replaces the key file atomically: a private temp file in the same folder,
// written, synced and renamed over it. A rename replaces a link at path, never follows it, and
// an existing looser mode isn't kept (#3627 F2).
func writeKeyFile(path, key string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".api_key-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	done := false
	defer func() {
		if !done {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if err := secfile.Protect(tmp); err != nil { // Windows ignores the mode (#3905)
		return err
	}
	if _, err := f.WriteString(key + "\n"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	done = true
	// The rename itself is made durable: without syncing the folder, a crash could bring the old
	// key back (Security's NIT on #3627; a folder can't be synced on Windows, where this is a no-op).
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// Key returns the current API key.
func (ks *KeyStore) Key() string {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.key
}

// RegenerateKey generates a new API key and persists it. Every session signed in with the
// old key ends too (#3356): a new key is how a user locks out whoever had the old one. The new
// key is written first: if it can't be persisted, the call fails and nothing changes, since a
// key that only lived in memory would bring the OLD key back at the next start (#3627 F2).
func (ks *KeyStore) RegenerateKey() (string, error) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	key, err := generateToken(keyLength)
	if err != nil {
		return "", fmt.Errorf("regenerate API key: %w", err)
	}
	if err := writeKeyFile(ks.keyPath, key); err != nil {
		return "", fmt.Errorf("regenerate API key: can't save it: %w", err)
	}
	ks.key = key
	if _, err := ks.sessions.DeleteSessionsExcept(context.Background(), ""); err != nil {
		log.Printf("[auth] new API key: couldn't end the browser sessions: %v", err)
	}
	return ks.key, nil
}

// SetTrustedProxies sets the reverse proxies (comma-separated IPs or CIDRs) whose
// X-Forwarded-Proto is believed when deciding the session cookie's Secure flag. A loopback
// peer is always trusted: only something on this machine can connect from there.
func (ks *KeyStore) SetTrustedProxies(list string) error {
	var nets []*net.IPNet
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.Contains(item, "/") {
			ip := net.ParseIP(item)
			if ip == nil {
				return fmt.Errorf("trusted proxy %q: not an IP or CIDR", item)
			}
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(item)
		if err != nil {
			return fmt.Errorf("trusted proxy %q: %w", item, err)
		}
		nets = append(nets, n)
	}
	ks.mu.Lock()
	ks.trusted = nets
	ks.mu.Unlock()
	return nil
}

// fromTrustedProxy reports whether the request's peer is a proxy whose forwarding headers are
// believed: a loopback peer, or one in SetTrustedProxies. This is header BELIEF (X-Forwarded-Proto
// for the Secure cookie, X-Forwarded-Host for the Origin check), not a no-credentials grant, so it
// stays on isLocalhost: a local TLS reverse proxy is exactly a loopback peer that sets these
// headers, and the caller still needs the key or a session. The no-credentials loopback pass is
// the one gated on isDirectLocal (#3585, in Middleware).
func (ks *KeyStore) fromTrustedProxy(r *http.Request) bool {
	if isLocalhost(r) {
		return true
	}
	ip := net.ParseIP(clientIP(r))
	if ip == nil {
		return false
	}
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	for _, n := range ks.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ForwardedHost is the host the browser reached Coral on, as a trusted reverse proxy
// (fromTrustedProxy) reports it in X-Forwarded-Host, when the proxy rewrites Host to
// Coral's own address. It is the LAST value (across repeated headers and comma lists): a
// proxy that appends puts its own value after whatever the client sent (#3524, coral-hub
// Security); a proxy should overwrite the header (nginx: proxy_set_header X-Forwarded-Host
// $host). From any other peer, or without the header, it is "". A page in a browser can't
// add the header to a request without a CORS preflight, which Coral refuses for any origin
// but its own (#3511 follow-up).
func (ks *KeyStore) ForwardedHost(r *http.Request) string {
	if ks == nil || !ks.fromTrustedProxy(r) {
		return ""
	}
	vals := r.Header.Values("X-Forwarded-Host")
	if len(vals) == 0 {
		return ""
	}
	parts := strings.Split(vals[len(vals)-1], ",")
	return strings.TrimSpace(parts[len(parts)-1])
}

// overHTTPS reports whether the browser reached Coral over https: directly over TLS, or
// through a trusted reverse proxy that says so in X-Forwarded-Proto. From any other peer the
// header is ignored, so a client cannot set it to change how the cookie is issued.
func (ks *KeyStore) overHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return ks.fromTrustedProxy(r) && strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

// ValidateKey checks if the given key matches the stored key.
func (ks *KeyStore) ValidateKey(key string) bool {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return subtle.ConstantTimeCompare([]byte(key), []byte(ks.key)) == 1
}

// CheckRateLimit returns true if the IP is within rate limits.
func (ks *KeyStore) CheckRateLimit(ip string) bool {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	// Lazy cleanup: prune expired entries when map grows too large
	if len(ks.rateMap) > 1000 {
		now := time.Now()
		for k, e := range ks.rateMap {
			if now.Sub(e.windowAt) > rateLimitWindow {
				delete(ks.rateMap, k)
			}
		}
	}

	entry, ok := ks.rateMap[ip]
	if !ok {
		ks.rateMap[ip] = &rateLimitEntry{count: 1, windowAt: time.Now()}
		return true
	}
	if time.Since(entry.windowAt) > rateLimitWindow {
		entry.count = 1
		entry.windowAt = time.Now()
		return true
	}
	entry.count++
	return entry.count <= rateLimitMax
}

// RateLimited reports whether ip has already used up its failed-attempt budget
// in the current window. It does not count anything itself.
func (ks *KeyStore) RateLimited(ip string) bool {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	e, ok := ks.rateMap[ip]
	if !ok || time.Since(e.windowAt) > rateLimitWindow {
		return false
	}
	return e.count >= rateLimitMax
}

// RecordFailure counts one failed authentication attempt from ip.
func (ks *KeyStore) RecordFailure(ip string) { ks.CheckRateLimit(ip) }

// IsLocalhost checks if the request originates from localhost.
// Uses the raw RemoteAddr (not X-Forwarded-For) to prevent spoofing.
func isLocalhost(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	return host == "127.0.0.1" || host == "::1"
}

// ExtractAPIKey extracts the API key from the request: the Authorization header (Bearer),
// then X-API-Key (as agent_docs/auth.md documents it, #3936), then the api_key query parameter.
// Coral Hub's tunnel forwards none of these headers.
func extractAPIKey(r *http.Request) string {
	// Authorization: Bearer <key>
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}
	// Query parameter
	if key := r.URL.Query().Get("api_key"); key != "" {
		return key
	}
	return ""
}

// cookieName is this server's session cookie (#3682): coral_session_ and a short hash of its
// data folder, so servers on one host name don't overwrite each other's (cookies ignore ports),
// with the __Host- prefix over https (no Domain, Secure, Path /: no other host can set it).
// Over plain http on loopback nothing a browser offers keeps the cookie from other ports; the
// session's 24 h lifetime stays the bound there.
func (ks *KeyStore) cookieName(r *http.Request) string {
	if ks.overHTTPS(r) {
		return "__Host-" + ks.cookieBase
	}
	return ks.cookieBase
}

// isSessionCookieName reports whether name is a session cookie Coral sets (any server's).
func isSessionCookieName(name string) bool {
	return strings.HasPrefix(name, legacyCookieName+"_") || strings.HasPrefix(name, "__Host-"+legacyCookieName+"_")
}

// SessionCookieName is the name of this server's session cookie for r (tests, tools).
func (ks *KeyStore) SessionCookieName(r *http.Request) string { return ks.cookieName(r) }

// extractSessionCookie returns the session token r carries for this server: this server's
// cookie (https or http name), else the pre-#3682 shared one. Of several, the first that is a
// valid session here wins; when none is, the first present (so a stale cookie is still seen as
// one).
func (ks *KeyStore) extractSessionCookie(r *http.Request) string {
	first := ""
	for _, name := range []string{"__Host-" + ks.cookieBase, ks.cookieBase, legacyCookieName} {
		c, err := r.Cookie(name)
		if err != nil || c.Value == "" {
			continue
		}
		if ks.ValidateSession(c.Value) {
			return c.Value
		}
		if first == "" {
			first = c.Value
		}
	}
	return first
}

// signOut ends every session of this server that r's cookies carry (#3682, Security: a browser
// can hold this server's cookie under each of its names, and each would keep it signed in) and
// clears this server's cookie. The pre-#3682 shared cookie is cleared only when its token was a
// session here: another Coral on the same host may own it.
func (ks *KeyStore) signOut(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{"__Host-" + ks.cookieBase, ks.cookieBase, legacyCookieName} {
		c, err := r.Cookie(name)
		if err != nil || c.Value == "" || !ks.ValidateSession(c.Value) {
			continue
		}
		ks.RevokeSession(c.Value)
		if name == legacyCookieName {
			http.SetCookie(w, &http.Cookie{Name: legacyCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		}
	}
	ks.clearSessionCookie(w, r)
}

// clearSessionCookie tells the browser to drop the session cookie.
func (ks *KeyStore) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     ks.cookieName(r),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   ks.overHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func generateToken(length int) (string, error) {
	b := make([]byte, length/2)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// HasValidSession reports whether the request carries a valid session cookie. Loopback
// requests are never asked for one today; the identity report (#3387) counts the ones
// without it, which strict mode would refuse.
// HasValidKey reports whether the request carries this server's API key (Bearer or
// ?api_key=). Loopback requests skip the API-key check in Middleware; strict local auth
// (#3418) admits a local request that has a valid one.
func (ks *KeyStore) HasValidKey(r *http.Request) bool {
	if ks == nil {
		return false
	}
	key := extractAPIKey(r)
	return key != "" && ks.ValidateKey(key)
}

func (ks *KeyStore) HasValidSession(r *http.Request) bool {
	if ks == nil {
		return false
	}
	token := ks.extractSessionCookie(r)
	return token != "" && ks.ValidateSession(token)
}

// sessionCookieBase is coral_session_ and the first 8 hex digits of the SHA-256 of the data
// folder's absolute path: one name per server, stable across restarts (#3682).
func sessionCookieBase(coralDir string) string {
	abs, err := filepath.Abs(coralDir)
	if err != nil {
		abs = coralDir
	}
	sum := sha256.Sum256([]byte(abs))
	return legacyCookieName + "_" + hex.EncodeToString(sum[:4])
}
