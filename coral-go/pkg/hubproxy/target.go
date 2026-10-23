// Package hubproxy implements the hub's streaming HTTP and WebSocket proxy to
// registered remote Coral servers, plus helpers (authenticated GET, websocket
// dial) that background services can reuse with the same SSRF safeguards.
//
// The handler is independent of how a request reaches the remote: it talks to
// a Target. HTTPTarget (self-hosted --hub) dials the remote's URL directly
// with SSRF protection; the Cloud Hub supplies a tunnel-backed Target.
package hubproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"github.com/subgentic/coral/internal/httputil"
)

// Target is a resolved remote server the proxy can send requests to.
type Target interface {
	// RoundTrip sends req to the remote. req.URL carries only the upstream
	// path (Path/RawPath) and RawQuery; the Target supplies scheme, host and
	// credentials. The response body streams (SSE must not be buffered) and
	// the caller closes it.
	RoundTrip(req *http.Request) (*http.Response, error)
	// DialWS opens a websocket to the upstream path. path is an escaped path
	// under /ws/ or /api/. A rejected credential yields ErrRemoteRejected.
	DialWS(ctx context.Context, path, rawQuery string, protocols []string) (WSConn, error)
}

// WSConn is the websocket the proxy pumps frames through. *websocket.Conn
// satisfies it, as does the tunnel's stream-backed connection.
type WSConn interface {
	Read(ctx context.Context) (websocket.MessageType, []byte, error)
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
	Close(code websocket.StatusCode, reason string) error
	CloseNow() error
	Subprotocol() string
	SetReadLimit(n int64)
}

// Validator is optionally implemented by a Target to reject it before any
// request is made (for HTTPTarget: URL syntax and SSRF checks). It should
// return ErrBlocked for disallowed addresses.
type Validator interface {
	Validate() error
}

// HTTPTarget is a remote Coral server reached directly over HTTP(S), as used
// by the self-hosted --hub. APIKey is plaintext and must only live in a local
// variable for the duration of a request. SSRF checks live here.
type HTTPTarget struct {
	URL          string
	APIKey       string
	AllowPrivate bool
}

// Validate implements Validator.
func (t HTTPTarget) Validate() error {
	_, err := ValidateTarget(t)
	return err
}

// RoundTrip implements Target.
func (t HTTPTarget) RoundTrip(req *http.Request) (*http.Response, error) {
	base, err := ValidateTarget(t)
	if err != nil {
		return nil, err
	}
	out := req.Clone(req.Context())
	out.URL.Scheme = base.Scheme
	out.URL.Host = base.Host
	out.Host = base.Host
	out.Header.Set("Authorization", "Bearer "+t.APIKey)
	return transport(t.AllowPrivate).RoundTrip(out)
}

// DialWS implements Target.
func (t HTTPTarget) DialWS(ctx context.Context, path, rawQuery string, protocols []string) (WSConn, error) {
	base, err := ValidateTarget(t)
	if err != nil {
		return nil, err
	}
	c, err := dialWS(ctx, t, base, path, rawQuery, protocols)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Resolver looks up a registered remote by id. It must return ErrUnknownServer
// for unregistered ids and ErrKeyUnreadable when the stored key cannot be
// decrypted.
type Resolver interface {
	Resolve(ctx context.Context, id string) (Target, error)
}

// StatusReporter is optionally implemented by a Resolver so the proxy can
// record server health (unauthorized, unreachable, key_unreadable).
type StatusReporter interface {
	MarkStatus(ctx context.Context, id, status, errMsg string)
}

// Status values passed to MarkStatus.
const (
	StatusUnauthorized  = "unauthorized"
	StatusUnreachable   = "unreachable"
	StatusKeyUnreadable = "key_unreadable"
)

var (
	ErrUnknownServer  = errors.New("unknown server")
	ErrKeyUnreadable  = errors.New("remote key unreadable")
	ErrRemoteRejected = errors.New("remote rejected API key")
	ErrBlocked        = errors.New("remote address not allowed")
	// ErrOffline is returned by a Resolver for a server the caller owns that has
	// no live connection (a tunnel that is down). It answers 502 "remote unreachable".
	ErrOffline        = errors.New("remote offline")
	ErrInvalidPath    = errors.New("invalid path")
	ErrPathNotAllowed = errors.New("path not allowed")
)

// headerTimeout is how long a machine has to start its reply; a variable so a test can shorten it.
var headerTimeout = 30 * time.Second

const (
	connectTimeout = 5 * time.Second
	// idleConnTimeout keeps a pooled hub->remote connection open between uses.
	// The remote poller's /api/health check (every PollInterval) shares this
	// pool, so with a timeout well above that interval the next chat switch
	// reuses a warm connection instead of paying a new TCP (and TLS) handshake.
	idleConnTimeout = 5 * time.Minute
)

// ── Path safety ─────────────────────────────────────────────────────

// SanitizePath validates an escaped upstream path (leading "/"). It rejects
// empty segments, "." and ".." segments (also when percent-encoded), encoded
// separators, backslashes, NUL bytes and double encoding. It returns the
// decoded path and the escaped path to send upstream.
func SanitizePath(escaped string) (decoded, raw string, err error) {
	if !strings.HasPrefix(escaped, "/") {
		return "", "", ErrInvalidPath
	}
	segs := strings.Split(escaped[1:], "/")
	out := make([]string, 0, len(segs))
	for i, s := range segs {
		d, derr := url.PathUnescape(s)
		if derr != nil {
			return "", "", ErrInvalidPath
		}
		if d == "." || d == ".." || strings.ContainsAny(d, "/\\\x00") || (d == "" && i != len(segs)-1) {
			return "", "", ErrInvalidPath
		}
		// A decoded segment that still looks percent-encoded is double encoding.
		if ld := strings.ToLower(d); strings.Contains(ld, "%2e") || strings.Contains(ld, "%2f") || strings.Contains(ld, "%5c") {
			return "", "", ErrInvalidPath
		}
		out = append(out, d)
	}
	decoded = "/" + strings.Join(out, "/")
	return decoded, escaped, nil
}

func hasPrefixDir(p, dir string) bool { return strings.HasPrefix(p, dir) }

func httpPathAllowed(p string) bool { return hasPrefixDir(p, "/api/") }

// wsPathAllowed: Coral's WebSocket routes are all under /ws/ (SEC-TUN-04).
func wsPathAllowed(p string) bool { return hasPrefixDir(p, "/ws/") }

// ── Target validation and guarded transport ─────────────────────────

// ValidateTarget parses the base URL and, unless AllowPrivate, resolves it and
// rejects private/reserved addresses. The dial-time guard re-checks the IP that
// is actually connected to.
func ValidateTarget(t HTTPTarget) (*url.URL, error) {
	u, err := url.Parse(t.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid remote url")
	}
	if !t.AllowPrivate {
		if _, err := httputil.ResolveAndValidateURL(t.URL); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBlocked, err)
		}
	}
	return u, nil
}

var (
	transMu sync.Mutex
	transes = map[bool]*http.Transport{}
)

// transport returns a shared transport whose dialer re-checks the resolved IP
// (defeating DNS rebinding) unless allowPrivate. Environment proxies are never
// used, so the dial check always sees the real destination.
func transport(allowPrivate bool) *http.Transport {
	transMu.Lock()
	defer transMu.Unlock()
	if t, ok := transes[allowPrivate]; ok {
		return t
	}
	d := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
	if !allowPrivate {
		d.Control = func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrBlocked
			}
			ip := net.ParseIP(host)
			if ip == nil || httputil.IsIPBlocked(ip) {
				return ErrBlocked
			}
			return nil
		}
	}
	t := &http.Transport{
		Proxy:               nil,
		DialContext:         d.DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     idleConnTimeout,
		DisableCompression:  true,
	}
	transes[allowPrivate] = t
	return t
}

// CloseIdleConnections closes the idle keep-alive connections of the shared
// remote transports, for a hub that stops talking to its remotes (#3142).
// Requests in flight are not affected; a later request dials again.
func CloseIdleConnections() {
	transMu.Lock()
	defer transMu.Unlock()
	for _, t := range transes {
		t.CloseIdleConnections()
	}
}

// ── Helpers reusable by background services ─────────────────────────

func stripAPIKey(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	vals, err := url.ParseQuery(rawQuery)
	if err != nil {
		return ""
	}
	if _, ok := vals["api_key"]; !ok {
		return rawQuery
	}
	vals.Del("api_key")
	return vals.Encode()
}

// Get performs an authenticated GET against the remote through t with the
// same path rules as the proxy (SSRF safeguards come from the Target). path is
// an escaped path under /api/. A 401/403 from the remote yields
// ErrRemoteRejected. The 30s header timeout applies; callers control the body
// lifetime via ctx.
func Get(ctx context.Context, t Target, path, rawQuery string) (*http.Response, error) {
	if _, raw, err := SanitizePath(path); err != nil {
		return nil, err
	} else {
		path = raw
	}
	dec, _ := url.PathUnescape(path)
	if !httpPathAllowed(dec) {
		return nil, ErrPathNotAllowed
	}
	if v, ok := t.(Validator); ok {
		if err := v.Validate(); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://remote.invalid/", nil)
	if err != nil {
		return nil, err
	}
	req.URL.Path, req.URL.RawPath, req.URL.RawQuery = dec, path, stripAPIKey(rawQuery)
	cl := &http.Client{
		Transport:     &headerTimeoutTransport{rt: targetTransport{t}, d: headerTimeout},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	if err := Refusal(resp); err != nil { // a key or Host refusal; any other answer is the caller's (#3649)
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

// headerTimeoutTransport cancels a request if response headers do not arrive
// within d, without limiting body streaming afterwards.
type headerTimeoutTransport struct {
	rt http.RoundTripper
	d  time.Duration
}

func (h *headerTimeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	timer := time.AfterFunc(h.d, cancel)
	resp, err := h.rt.RoundTrip(req.WithContext(ctx))
	timer.Stop()
	if err != nil {
		cancel()
		return nil, err
	}
	// cancel is released when the request context ends; tie it to body close.
	resp.Body = &cancelBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelBody) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
