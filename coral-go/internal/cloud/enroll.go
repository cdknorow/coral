package cloud

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/subgentic/coral/pkg/tunnel"
	"unicode"

	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/config"
)

// Enrollment errors. They carry no hub-supplied text.
var (
	ErrAuthorizationPending = errors.New("cloud: authorization pending")
	ErrSlowDown             = errors.New("cloud: polling too fast")
	ErrEnrollmentExpired    = errors.New("cloud: enrollment code expired")
	ErrAccessDenied         = errors.New("cloud: the request was denied in Coral Hub")
	ErrHubResponse          = errors.New("cloud: unexpected response from the hub")
	// ErrHubUnavailable: the hub could not be reached or answered with a server error
	// (5xx). Transient: trying again later may work.
	ErrHubUnavailable = errors.New("cloud: hub unreachable")
)

// RateLimitedError is the hub refusing to start an enrollment for now (429
// rate_limited). RetryAfter is from its Retry-After header, capped; zero if absent.
type RateLimitedError struct {
	RetryAfter time.Duration
	// Local: this machine's own limit on asking for new codes, not the hub's answer.
	Local bool
}

func (e *RateLimitedError) Error() string {
	if e.Local {
		return fmt.Sprintf("a new Coral Hub code can be asked for again in %s", e.RetryAfter.Round(time.Second))
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("Coral Hub is busy: try again in %s", e.RetryAfter.Round(time.Second))
	}
	return "Coral Hub is busy: try again in a minute"
}

const maxRetryAfter = 15 * time.Minute

// enrollRetryAfter reads a Retry-After header in seconds or as an HTTP date. Unlike
// redeem's (retried automatically, capped at a minute), it is only shown to the user.
func enrollRetryAfter(h http.Header, now time.Time) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	var d time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, 0), maxRetryAfter)
}

const (
	maxHubResponse = 16 << 10
	tokenPrefix    = "coralhub_srv_"
)

var (
	userCodeRe = regexp.MustCompile(`^[A-Z0-9]{4}-?[A-Z0-9]{4}$`)
	serverIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// hubAccountRe is a Coral Hub account id: a lowercase UUID (#3966).
	hubAccountRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// NormalizeHubURL validates the configured hub address and returns it as a bare
// origin (scheme://host[:port], no path, query, fragment or credentials). Release
// builds accept https only; -tags dev builds also accept http for loopback hosts.
func NormalizeHubURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil {
		return "", errors.New("cloud url must be an origin like https://hub.example.com")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("cloud url must not contain a path, query or fragment")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !config.AllowInsecureCloud || !auth.IsLoopbackHost(u.Hostname()) {
			return "", errors.New("cloud url must use https")
		}
	default:
		return "", errors.New("cloud url must use https")
	}
	return u.Scheme + "://" + u.Host, nil
}

// NewHubHTTPClient returns the client used for enrollment calls: certificate
// verification on (TLS 1.2+), never follows redirects, short timeouts, and no way to
// switch verification off.
func NewHubHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// The tunnel's socket keeps a short unsent queue, so the connector's uplink scheduler
	// decides what goes first, not the kernel (#3440). Harmless for the other hub calls.
	tr.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: tunnel.LowLatencyControl}).DialContext
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	tr.ResponseHeaderTimeout = 15 * time.Second
	return &http.Client{
		Transport:     userAgentTransport{tr},
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// userAgentTransport names Coral to the hub on every call: Coral/<version> (<os>/<arch>).
type userAgentTransport struct{ base http.RoundTripper }

func (t userAgentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", hubUserAgent())
	return t.base.RoundTrip(r)
}

func hubUserAgent() string {
	return fmt.Sprintf("Coral/%s (%s/%s)", cleanInfo(config.ReportedVersion(), 40), runtime.GOOS, runtime.GOARCH)
}

// DeviceCode is what the user needs to complete enrollment in the browser.
type DeviceCode struct {
	DeviceCode      string // secret: only ever sent back to the hub's poll endpoint
	UserCode        string
	VerificationURL string
	Interval        time.Duration
	ExpiresAt       time.Time
}

// Enrolled is the outcome of a completed enrollment.
type Enrolled struct {
	Token    string // secret
	ServerID string
	Label    string
	Account  string // the Coral Hub account that enrolled it (#3966); "" from a hub that doesn't say
	// OwnerUser is that account's hub-user-<8 hex> label as the hub stamps it (#5280, the update routes' owner check); "" when
	// the hub doesn't say or says something that is not one.
	OwnerUser string
	// TeamOrg is the Coral Hub organisation's id (a canonical lower-case UUID) when the hub enrolled this machine as a team's (#7061), and "" for a machine enrolled to a person. A team's machine has no
	// Account and no OwnerUser: the person who pressed Confirm is not its owner.
	TeamOrg string
}

func hubPost(ctx context.Context, hc *http.Client, hubURL, path string, in, out any) (int, error) {
	status, _, err := hubPostHeader(ctx, hc, hubURL, path, in, out)
	return status, err
}

// hubPostHeader is hubPost with the response headers.
func hubPostHeader(ctx context.Context, hc *http.Client, hubURL, path string, in, out any) (int, http.Header, error) {
	var body io.Reader = http.NoBody
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hubURL+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, ErrHubUnavailable // do not echo URL or error text
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxHubResponse))
	if err != nil {
		return resp.StatusCode, resp.Header, ErrHubResponse
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, resp.Header, ErrHubResponse
		}
	}
	return resp.StatusCode, resp.Header, nil
}

// EnrollInfo is what this machine tells the hub about itself, so the person approving the
// code sees who asked. The hub labels it "reported by the machine (not verified)".
type EnrollInfo struct {
	Hostname     string `json:"hostname,omitempty"`
	CoralVersion string `json:"coral_version,omitempty"`
}

// cleanInfo keeps printable text only: no control, format (bidi, zero-width) or line
// characters, at most max runes.
func cleanInfo(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return -1
		}
		return r
	}, s)
	if r := []rune(strings.TrimSpace(s)); len(r) > max {
		return string(r[:max])
	}
	return strings.TrimSpace(s)
}

// StartEnrollment asks the hub for a device code (POST /api/enroll/start).
func StartEnrollment(ctx context.Context, hc *http.Client, hubURL string, now time.Time, info EnrollInfo) (*DeviceCode, error) {
	var r struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expires_in"`
	}
	info = EnrollInfo{Hostname: cleanInfo(info.Hostname, 64), CoralVersion: cleanInfo(info.CoralVersion, 40)}
	status, hdr, err := hubPostHeader(ctx, hc, hubURL, "/api/enroll/start", info, &r)
	if err != nil && status == 0 && ctx.Err() == nil {
		// One quick retry for a transient connection error (a fresh code is harmless).
		time.Sleep(200 * time.Millisecond)
		status, hdr, err = hubPostHeader(ctx, hc, hubURL, "/api/enroll/start", info, &r)
	}
	if status == http.StatusTooManyRequests {
		// rate_limited: whatever the body says, back off for as long as the hub asks.
		return nil, &RateLimitedError{RetryAfter: enrollRetryAfter(hdr, now)}
	}
	if status >= 500 {
		return nil, ErrHubUnavailable
	}
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return nil, ErrHubResponse
	}
	if len(r.DeviceCode) < 16 || len(r.DeviceCode) > 256 || !userCodeRe.MatchString(r.UserCode) {
		return nil, ErrHubResponse
	}
	interval := time.Duration(clamp(r.Interval, 2, 30)) * time.Second
	expires := time.Duration(clamp(r.ExpiresIn, 30, 900)) * time.Second
	return &DeviceCode{
		DeviceCode: r.DeviceCode, UserCode: r.UserCode, Interval: interval, ExpiresAt: now.Add(expires),
		VerificationURL: safeVerificationURL(hubURL, r.VerificationURL),
	}, nil
}

// safeVerificationURL only trusts a hub-supplied link that stays on the configured
// hub origin; otherwise it falls back to the hub's own /enroll page. The user is
// told to open this link and type a code, so it must not point elsewhere.
func safeVerificationURL(hubURL, supplied string) string {
	fallback := hubURL + "/enroll"
	u, err := url.Parse(supplied)
	if err != nil || u.User != nil || u.Fragment != "" || u.Scheme+"://"+u.Host != hubURL {
		return fallback
	}
	return u.String()
}

// PollEnrollment checks whether the user confirmed (POST /api/enroll/poll). It returns
// ErrAuthorizationPending, ErrSlowDown or ErrEnrollmentExpired for the RFC 8628
// answers, and the token exactly once on success.
func PollEnrollment(ctx context.Context, hc *http.Client, hubURL, deviceCode string) (*Enrolled, error) {
	var r struct {
		Token    string `json:"server_token"`
		ServerID string `json:"server_id"`
		Label    string `json:"label"`
		Account  string `json:"account"`
		Owner    string `json:"owner_user"`
		Kind     string `json:"owner_kind"`
		Org      string `json:"owner_org"`
		Error    string `json:"error"`
	}
	status, err := hubPost(ctx, hc, hubURL, "/api/enroll/poll", map[string]string{"device_code": deviceCode}, &r)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
		if !strings.HasPrefix(r.Token, tokenPrefix) || len(r.Token) > 256 || !serverIDRe.MatchString(r.ServerID) {
			return nil, ErrHubResponse
		}
		// What the machine is enrolled as (#7061): nothing or "personal" is a person's (and an organisation then is a refusal, not ignored), "team" needs the organisation's id, and anything else is
		// refused. A reply this code does not understand is never "treated as personal": a stray personal owner marker would then be valid on a machine the hub meant as a team's.
		switch r.Kind {
		case "", tunnel.OwnerKindPersonal:
			if r.Org != "" {
				return nil, ErrHubResponse
			}
		case tunnel.OwnerKindTeam:
			if !tunnel.ValidHubOrg(r.Org) {
				return nil, ErrHubResponse
			}
			// A team's machine has no person owner: neither the account nor the label is kept (the sign-in is refused there, and the owner check is the organisation's).
			return &Enrolled{Token: r.Token, ServerID: r.ServerID, Label: cleanLabel(r.Label), TeamOrg: r.Org}, nil
		default:
			return nil, ErrHubResponse
		}
		// A malformed account is no account: Hub sign-in then stays off until one is pinned.
		acct := r.Account
		if !ValidHubAccount(acct) {
			acct = ""
		}
		owner := r.Owner
		if !tunnel.ValidHubUser(owner) {
			owner = ""
		}
		return &Enrolled{Token: r.Token, ServerID: r.ServerID, Label: cleanLabel(r.Label), Account: acct, OwnerUser: owner}, nil
	case http.StatusBadRequest, http.StatusForbidden:
		switch r.Error {
		case "authorization_pending":
			return nil, ErrAuthorizationPending
		case "slow_down": // RFC 8628 sends it as a 400
			return nil, ErrSlowDown
		case "expired_token":
			return nil, ErrEnrollmentExpired
		case "access_denied":
			return nil, ErrAccessDenied
		}
	case http.StatusTooManyRequests:
		return nil, ErrSlowDown
	}
	return nil, ErrHubResponse
}

// ValidHubAccount reports whether s is a Coral Hub account id.
func ValidHubAccount(s string) bool { return hubAccountRe.MatchString(s) }

func cleanLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return strings.TrimSpace(s)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// TunnelURL turns the hub origin into its wss:// tunnel endpoint.
func TunnelURL(hubURL string) string {
	u, _ := url.Parse(hubURL)
	scheme := "wss"
	if u.Scheme == "http" {
		scheme = "ws" // dev builds, loopback only
	}
	return scheme + "://" + u.Host + "/tunnel"
}
