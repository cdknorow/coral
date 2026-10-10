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
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/config"
)

// Enrollment errors. They carry no hub-supplied text.
var (
	ErrAuthorizationPending = errors.New("cloud: authorization pending")
	ErrSlowDown             = errors.New("cloud: polling too fast")
	ErrEnrollmentExpired    = errors.New("cloud: enrollment code expired")
	ErrHubResponse          = errors.New("cloud: unexpected response from the hub")
)

const (
	maxHubResponse = 16 << 10
	tokenPrefix    = "coralhub_srv_"
)

var (
	userCodeRe = regexp.MustCompile(`^[A-Z0-9]{4}-?[A-Z0-9]{4}$`)
	serverIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
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
		if !config.AllowInsecureCloud || !isLoopback(u.Hostname()) {
			return "", errors.New("cloud url must use https")
		}
	default:
		return "", errors.New("cloud url must use https")
	}
	return u.Scheme + "://" + u.Host, nil
}

func isLoopback(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// NewHubHTTPClient returns the client used for enrollment calls: certificate
// verification on (TLS 1.2+), never follows redirects, short timeouts, and no way to
// switch verification off.
func NewHubHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	tr.ResponseHeaderTimeout = 15 * time.Second
	return &http.Client{
		Transport:     tr,
		Timeout:       20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
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
}

func hubPost(ctx context.Context, hc *http.Client, hubURL, path string, in, out any) (int, error) {
	var body io.Reader = http.NoBody
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hubURL+path, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("cloud: hub unreachable") // do not echo URL or error text
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxHubResponse))
	if err != nil {
		return resp.StatusCode, ErrHubResponse
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, ErrHubResponse
		}
	}
	return resp.StatusCode, nil
}

// StartEnrollment asks the hub for a device code (POST /api/enroll/start).
func StartEnrollment(ctx context.Context, hc *http.Client, hubURL string, now time.Time) (*DeviceCode, error) {
	var r struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expires_in"`
	}
	status, err := hubPost(ctx, hc, hubURL, "/api/enroll/start", struct{}{}, &r)
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
		return &Enrolled{Token: r.Token, ServerID: r.ServerID, Label: cleanLabel(r.Label)}, nil
	case http.StatusBadRequest:
		switch r.Error {
		case "authorization_pending":
			return nil, ErrAuthorizationPending
		case "expired_token":
			return nil, ErrEnrollmentExpired
		}
	case http.StatusTooManyRequests:
		return nil, ErrSlowDown
	}
	return nil, ErrHubResponse
}

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
