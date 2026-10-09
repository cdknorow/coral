// Package proxy implements the hub's streaming HTTP and WebSocket proxy to
// registered remote Coral servers, plus helpers (authenticated GET, websocket
// dial) that background services can reuse with the same SSRF safeguards.
package proxy

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

	"github.com/cdknorow/coral/internal/httputil"
)

// Target is a resolved remote server. APIKey is plaintext and must only live
// in a local variable for the duration of a request.
type Target struct {
	URL          string
	APIKey       string
	AllowPrivate bool
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
	ErrInvalidPath    = errors.New("invalid path")
	ErrPathNotAllowed = errors.New("path not allowed")
)

const (
	connectTimeout = 5 * time.Second
	headerTimeout  = 30 * time.Second
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
func wsPathAllowed(p string) bool   { return hasPrefixDir(p, "/ws/") || hasPrefixDir(p, "/api/") }

// ── Target validation and guarded transport ─────────────────────────

// ValidateTarget parses the base URL and, unless AllowPrivate, resolves it and
// rejects private/reserved addresses. The dial-time guard re-checks the IP that
// is actually connected to.
func ValidateTarget(t Target) (*url.URL, error) {
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
		IdleConnTimeout:     60 * time.Second,
		DisableCompression:  true,
	}
	transes[allowPrivate] = t
	return t
}

// ── Helpers reusable by background services ─────────────────────────

func endpoint(t Target, u *url.URL, rawPath, rawQuery string) string {
	out := url.URL{Scheme: u.Scheme, Host: u.Host, RawQuery: stripAPIKey(rawQuery)}
	dec, _ := url.PathUnescape(rawPath)
	out.Path, out.RawPath = dec, rawPath
	return out.String()
}

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

// Get performs an authenticated GET against {t.URL}{path} with the same SSRF
// safeguards and path rules as the proxy. path is an escaped path under /api/.
// A 401/403 from the remote yields ErrRemoteRejected. The 30s header timeout
// applies; callers control the body lifetime via ctx.
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
	u, err := ValidateTarget(t)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint(t, u, path, rawQuery), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.APIKey)
	cl := &http.Client{
		Transport:     &headerTimeoutTransport{rt: transport(t.AllowPrivate), d: headerTimeout},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, ErrRemoteRejected
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
