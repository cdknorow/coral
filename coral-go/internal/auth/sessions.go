package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/subgentic/coral/internal/appmarker"
)

// Browser sessions (#3949): every sign-in (a sign-in link, the API-key form, the password)
// gets a random 256-bit cookie value; only its SHA-256 is kept, with a label, the times and
// whether the person asked to stay signed in. The rows live in sessions.db when the server sets
// a backend (they then survive a restart), else in memory.

// Session methods.
const (
	SessionByLink     = "link"
	SessionByKey      = "key"
	SessionByPassword = "password"
)

const (
	// sessionMaxAge is a session's life without "keep me signed in" (absolute).
	sessionMaxAge = 24 * time.Hour
	// sessionKeepAge is a "keep me signed in" session's life (absolute, never extended).
	sessionKeepAge = 30 * 24 * time.Hour
	// maxSessions bounds the kept sessions; the least recently used go first.
	maxSessions = 200
	// touchEvery limits last_seen writes.
	touchEvery = time.Minute
	// maxLabel is the longest session label (the client's User-Agent decides it).
	maxLabel = 64
)

// SessionRecord is one browser session as stored (never the cookie value).
type SessionRecord struct {
	IDHash     string
	Label      string
	Method     string
	Persistent bool
	CreatedAt  time.Time
	LastSeen   time.Time
	ExpiresAt  time.Time
	// A Hub session's grant (#3966): the grant id, the owner's account, the cleaned display
	// name and the last check Coral Hub answered yes to. Empty for every other method.
	HubGrant, HubAccount, HubDisplay string
	HubLastOK                        time.Time
}

// SessionBackend keeps browser sessions (sessions.db: store.BrowserSessions).
type SessionBackend interface {
	PutSession(ctx context.Context, s SessionRecord) error
	GetSession(ctx context.Context, idHash string) (SessionRecord, bool, error)
	TouchSession(ctx context.Context, idHash string, at time.Time) error
	DeleteSession(ctx context.Context, idHash string) error
	DeleteSessionsExcept(ctx context.Context, keep string) (int, error)
	ListSessions(ctx context.Context) ([]SessionRecord, error)
	SweepSessions(ctx context.Context, now time.Time, max int) error
}

// memSessions is the in-memory SessionBackend (no database: tests, tools).
type memSessions struct {
	mu   sync.Mutex
	m    map[string]SessionRecord
	seq  map[string]uint64 // insertion order: breaks ties between equal LastSeen times (Windows' clock is coarse)
	next uint64
}

// newerFirst orders sessions most recently seen first, the later sign-in first on a tie.
func (s *memSessions) newerFirst(all []SessionRecord) {
	sort.Slice(all, func(i, j int) bool {
		if !all[i].LastSeen.Equal(all[j].LastSeen) {
			return all[i].LastSeen.After(all[j].LastSeen)
		}
		return s.seq[all[i].IDHash] > s.seq[all[j].IDHash]
	})
}

func newMemSessions() *memSessions {
	return &memSessions{m: map[string]SessionRecord{}, seq: map[string]uint64{}}
}

func (s *memSessions) PutSession(_ context.Context, r SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[r.IDHash]; !ok {
		s.next++
		s.seq[r.IDHash] = s.next
	}
	s.m[r.IDHash] = r
	return nil
}

func (s *memSessions) GetSession(_ context.Context, id string) (SessionRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.m[id]
	return r, ok, nil
}

func (s *memSessions) TouchSession(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.m[id]; ok {
		r.LastSeen = at
		s.m[id] = r
	}
	return nil
}

func (s *memSessions) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	delete(s.seq, id)
	return nil
}

func (s *memSessions) DeleteSessionsExcept(_ context.Context, keep string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id := range s.m {
		if id != keep {
			delete(s.m, id)
			delete(s.seq, id)
			n++
		}
	}
	return n, nil
}

func (s *memSessions) ListSessions(context.Context) ([]SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SessionRecord, 0, len(s.m))
	for _, r := range s.m {
		out = append(out, r)
	}
	s.newerFirst(out)
	return out, nil
}

func (s *memSessions) SweepSessions(_ context.Context, now time.Time, max int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.m {
		if !now.Before(r.ExpiresAt) {
			delete(s.m, id)
			delete(s.seq, id)
		}
	}
	if len(s.m) > max {
		all := make([]SessionRecord, 0, len(s.m))
		for _, r := range s.m {
			all = append(all, r)
		}
		s.newerFirst(all)
		for _, r := range all[max:] {
			delete(s.m, r.IDHash)
			delete(s.seq, r.IDHash)
		}
	}
	return nil
}

// SetSessionBackend keeps sessions in b from now on (the server: sessions.db). Sessions made
// before (in memory) are dropped.
func (ks *KeyStore) SetSessionBackend(b SessionBackend) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.sessions = b
}

func (ks *KeyStore) backend() SessionBackend {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	return ks.sessions
}

func sessionHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// sessionLabel is a short, plain-text name for the browser from its User-Agent: the client
// chooses it, so control characters are dropped and it is cut at maxLabel runes.
func sessionLabel(ua string) string {
	browser := ""
	for _, b := range []struct{ token, name string }{
		{appmarker.Token + "/", "Coral app"}, // the Coral app's own window (its web view adds the marker); before the browser it is built on
		{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"},
	} {
		if strings.Contains(ua, b.token) {
			browser = b.name
			break
		}
	}
	osName := ""
	for _, o := range []struct{ token, name string }{
		{"iPhone", "iOS"}, {"iPad", "iPadOS"}, {"Android", "Android"}, {"Mac OS X", "macOS"}, {"Macintosh", "macOS"},
		{"Windows", "Windows"}, {"CrOS", "ChromeOS"}, {"Linux", "Linux"},
	} {
		if strings.Contains(ua, o.token) {
			osName = o.name
			break
		}
	}
	label := ua
	switch {
	case browser != "" && osName != "":
		label = browser + " on " + osName
	case browser != "":
		label = browser
	}
	var b strings.Builder
	n := 0
	for _, r := range label {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if n == maxLabel {
			break
		}
		b.WriteRune(r)
		n++
	}
	if s := strings.TrimSpace(b.String()); s != "" {
		return s
	}
	return "Unknown browser"
}

// NewSession signs a browser in: a fresh 256-bit cookie value, its hash kept with the label
// and an absolute expiry (30 days with keep, else sessionMaxAge).
func (ks *KeyStore) NewSession(r *http.Request, method string, keep bool) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("create session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	now := time.Now().UTC()
	life := sessionMaxAge
	if keep {
		life = sessionKeepAge
	}
	ctx := context.Background()
	be := ks.backend()
	if err := be.PutSession(ctx, SessionRecord{
		IDHash: sessionHash(token), Label: sessionLabel(r.UserAgent()), Method: method, Persistent: keep,
		CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(life),
	}); err != nil {
		return "", fmt.Errorf("save session: %w", err)
	}
	if err := be.SweepSessions(ctx, now, maxSessions); err != nil {
		log.Printf("[auth] couldn't sweep old sessions: %v", err)
	}
	return token, nil
}

// NewSessionRotating is NewSession for a sign-in from a browser that may already hold a session: that one ends here, so a cookie a sign-in
// replaced (a link, the key form) is not left valid behind it, as the password sign-in does.
func (ks *KeyStore) NewSessionRotating(r *http.Request, method string, keep bool) (string, error) {
	old := ks.extractSessionCookie(r)
	token, err := ks.NewSession(r, method, keep)
	if err != nil {
		return "", err
	}
	if old != "" {
		ks.RevokeSession(old)
	}
	return token, nil
}

// CreateSession signs a browser in with the API key (kept for the key paths and tests).
func (ks *KeyStore) CreateSession(clientIP, userAgent string) (string, error) {
	r := &http.Request{Header: http.Header{"User-Agent": {userAgent}}}
	return ks.NewSession(r, SessionByKey, false)
}

// lookupSession returns the live session for a cookie value; an expired one is deleted. A
// backend error is "no session" (fail closed).
func (ks *KeyStore) lookupSession(token string) (SessionRecord, bool) {
	if token == "" {
		return SessionRecord{}, false
	}
	ctx := context.Background()
	be := ks.backend()
	id := sessionHash(token)
	s, ok, err := be.GetSession(ctx, id)
	if err != nil {
		log.Printf("[auth] couldn't read a session: %v", err)
		return SessionRecord{}, false
	}
	if !ok {
		return SessionRecord{}, false
	}
	now := time.Now()
	if !now.Before(s.ExpiresAt) {
		_ = be.DeleteSession(ctx, id)
		return SessionRecord{}, false
	}
	if now.Sub(s.LastSeen) >= touchEvery {
		if err := be.TouchSession(ctx, id, now.UTC()); err == nil {
			s.LastSeen = now.UTC()
		}
	}
	return s, true
}

// ValidateSession reports whether a cookie value is a live session.
func (ks *KeyStore) ValidateSession(token string) bool {
	_, ok := ks.lookupSession(token)
	return ok
}

// RevokeSession ends one session (sign out). An unknown token is a no-op.
func (ks *KeyStore) RevokeSession(token string) {
	if token == "" {
		return
	}
	if err := ks.endSession(context.Background(), sessionHash(token)); err != nil {
		log.Printf("[auth] couldn't end a session: %v", err)
	}
}

// RevokeOtherSessions ends every session but the one r is signed in with (all of them when r
// has none); it returns how many ended.
func (ks *KeyStore) RevokeOtherSessions(r *http.Request) (int, error) {
	keep := ""
	if t := ks.extractSessionCookie(r); t != "" && ks.ValidateSession(t) {
		keep = sessionHash(t)
	}
	n, err := ks.backend().DeleteSessionsExcept(context.Background(), keep)
	ks.live.close("", keep) // the others' open WebSockets close too
	return n, err
}

// RevokeAllSessions ends every browser session.
func (ks *KeyStore) RevokeAllSessions() (int, error) {
	n, err := ks.backend().DeleteSessionsExcept(context.Background(), "")
	ks.live.close("", "")
	return n, err
}

// currentSessionHash is the hash of the live session r carries, or "".
func (ks *KeyStore) currentSessionHash(r *http.Request) string {
	if t := ks.extractSessionCookie(r); t != "" && ks.ValidateSession(t) {
		return sessionHash(t)
	}
	return ""
}

// setSessionCookie sets the session cookie: HttpOnly, SameSite=Strict, Secure when the browser
// reached Coral over https (TLS here, or a trusted proxy's X-Forwarded-Proto). With keep it
// lasts sessionKeepAge; without, it is a browser-session cookie (the server still ends it after
// sessionMaxAge).
func (ks *KeyStore) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, keep bool) {
	c := &http.Cookie{
		Name:     ks.cookieName(r),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   ks.overHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	}
	if keep {
		c.MaxAge = int(sessionKeepAge.Seconds())
	}
	http.SetCookie(w, c)
}

// SetPeerUser gives the store the peer-user check for sign-in links (#3970).
func (ks *KeyStore) SetPeerUser(f func(*http.Request) (same, supported bool)) {
	ks.mu.Lock()
	ks.peerUser = f
	ks.mu.Unlock()
}
