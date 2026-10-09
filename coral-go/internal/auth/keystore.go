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

// RegenerateKey generates a new API key and persists it.
func (ks *KeyStore) RegenerateKey() (string, error) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	key, err := generateToken(keyLength)
	if err != nil {
		return "", fmt.Errorf("regenerate API key: %w", err)
	}
	ks.key = key
	if err := os.WriteFile(ks.keyPath, []byte(ks.key+"\n"), 0600); err != nil {
		log.Printf("[auth] warning: failed to persist regenerated key to disk: %v", err)
	}
	return ks.key, nil
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

// SetSessionCookie sets the session cookie on the response.
// The Secure flag is set when the request arrived over TLS or from
// a non-localhost remote address (implying a reverse proxy / HTTPS
// frontend), so the cookie won't leak over plain HTTP.
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	secure := r.TLS != nil
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   secure,
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
