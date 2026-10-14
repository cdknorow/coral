// Package auth provides API key authentication and session management for Coral.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	keyLength       = 32 // 32 hex chars = 128 bits
	sessionLength   = 32 // 32 hex chars
	cookieName      = "coral_session"
	sessionMaxAge   = 24 * time.Hour
	maxSessions     = 100
	rateLimitWindow = time.Minute
	rateLimitMax    = 10
)

// Session represents an authenticated session.
type Session struct {
	Token     string
	CreatedAt time.Time
	ClientIP  string
	UserAgent string
}

// rateLimitEntry tracks failed auth attempts per IP.
type rateLimitEntry struct {
	count    int
	windowAt time.Time
}

// KeyStore manages API keys and sessions.
type KeyStore struct {
	mu       sync.RWMutex
	key      string
	keyPath  string
	sessions map[string]*Session
	order    []string // LRU order (oldest first)
	rateMap  map[string]*rateLimitEntry
	trusted  []*net.IPNet // reverse proxies whose X-Forwarded-Proto is believed (#3356)
	// loginTokens maps a login token's SHA-256 to its expiry (#3387).
	loginTokens map[string]time.Time
}

// NewKeyStore creates or loads an API key from the given directory.
func NewKeyStore(coralDir string) (*KeyStore, error) {
	ks := &KeyStore{
		keyPath:  filepath.Join(coralDir, "api_key"),
		sessions: make(map[string]*Session),
		rateMap:  make(map[string]*rateLimitEntry),
	}
	if err := ks.loadOrGenerate(); err != nil {
		return nil, err
	}
	return ks, nil
}

func (ks *KeyStore) loadOrGenerate() error {
	data, err := os.ReadFile(ks.keyPath)
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
	if err := os.MkdirAll(filepath.Dir(ks.keyPath), 0755); err != nil {
		log.Printf("[auth] warning: failed to create key directory: %v", err)
	}
	if err := os.WriteFile(ks.keyPath, []byte(ks.key+"\n"), 0600); err != nil {
		log.Printf("[auth] warning: failed to persist API key to disk: %v", err)
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
// old key ends too (#3356): a new key is how a user locks out whoever had the old one.
func (ks *KeyStore) RegenerateKey() (string, error) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	key, err := generateToken(keyLength)
	if err != nil {
		return "", fmt.Errorf("regenerate API key: %w", err)
	}
	ks.key = key
	ks.sessions = make(map[string]*Session)
	ks.order = nil
	if err := os.WriteFile(ks.keyPath, []byte(ks.key+"\n"), 0600); err != nil {
		log.Printf("[auth] warning: failed to persist regenerated key to disk: %v", err)
	}
	return ks.key, nil
}

// RevokeSession ends one session (sign out). An unknown token is a no-op.
func (ks *KeyStore) RevokeSession(token string) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	if _, ok := ks.sessions[token]; !ok {
		return
	}
	delete(ks.sessions, token)
	for i, t := range ks.order {
		if t == token {
			ks.order = append(ks.order[:i], ks.order[i+1:]...)
			break
		}
	}
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

// fromTrustedProxy reports whether the request's peer is a proxy whose forwarding headers
// are believed: a loopback peer, or one in SetTrustedProxies.
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

// CreateSession creates a new authenticated session and returns the token.
func (ks *KeyStore) CreateSession(clientIP, userAgent string) (string, error) {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	token, err := generateToken(sessionLength)
	if err != nil {
		return "", fmt.Errorf("create session token: %w", err)
	}
	ks.sessions[token] = &Session{
		Token:     token,
		CreatedAt: time.Now(),
		ClientIP:  clientIP,
		UserAgent: userAgent,
	}
	ks.order = append(ks.order, token)

	// LRU eviction
	for len(ks.sessions) > maxSessions {
		oldest := ks.order[0]
		ks.order = ks.order[1:]
		delete(ks.sessions, oldest)
	}

	return token, nil
}

// ValidateSession checks if a session token is valid and not expired.
// Expired sessions are removed lazily on access.
func (ks *KeyStore) ValidateSession(token string) bool {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	s, ok := ks.sessions[token]
	if !ok {
		return false
	}
	if time.Since(s.CreatedAt) > sessionMaxAge {
		delete(ks.sessions, token)
		return false
	}
	return true
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

// ExtractAPIKey extracts the API key from the request.
// Checks Authorization header first, then query parameter.
func extractAPIKey(r *http.Request) string {
	// Authorization: Bearer <key>
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Query parameter
	if key := r.URL.Query().Get("api_key"); key != "" {
		return key
	}
	return ""
}

// ExtractSessionCookie extracts the session token from the cookie.
func extractSessionCookie(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// setSessionCookie sets the session cookie on the response. It is Secure when the browser
// reached Coral over https (overHTTPS: TLS here, or a trusted proxy's X-Forwarded-Proto), so
// the cookie is never sent back over plain http.
func (ks *KeyStore) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   ks.overHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie tells the browser to drop the session cookie.
func (ks *KeyStore) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
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
func (ks *KeyStore) HasValidSession(r *http.Request) bool {
	if ks == nil {
		return false
	}
	token := extractSessionCookie(r)
	return token != "" && ks.ValidateSession(token)
}
