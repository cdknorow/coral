package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AllowedHosts are where a release's files may come from: GitHub, the hosts it redirects a release download to, and its API for the list of releases a promotion is found in (#6393).
// Anything else is refused, at the first request and at every redirect.
var AllowedHosts = []string{"github.com", "api.github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"}

const maxRedirects = 3

// ErrTooLarge is a body longer than the limit the caller set: the answer is refused, never truncated. The client tells it from a failure to reach GitHub, so a release with an oversize
// file is a bad candidate and not an outage (#6393).
var ErrTooLarge = errors.New("the body is larger than the limit")

// StatusError is an answer other than 200: the caller tells a 404 (the file is not there) from a failure to reach GitHub.
type StatusError struct {
	URL    string
	Status int
	Text   string
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s: %s", e.URL, e.Text) }

// Fetcher downloads with a host allow-list, https only, a limit on redirects and a size cap. The zero value uses AllowedHosts and a 60 s timeout.
type Fetcher struct {
	// Allow decides a host (a test allows its own server); nil is AllowedHosts. Scheme is "https" unless a test says otherwise.
	Allow  func(host string) bool
	Scheme string
	Client *http.Client
}

func (f *Fetcher) allowed(u *url.URL) error {
	scheme := f.Scheme
	if scheme == "" {
		scheme = "https"
	}
	if u.Scheme != scheme {
		return fmt.Errorf("%s is not %s", u.Redacted(), scheme)
	}
	if u.User != nil {
		return errors.New("a URL with credentials")
	}
	host := strings.ToLower(u.Hostname())
	if f.Allow != nil {
		if !f.Allow(host) {
			return fmt.Errorf("host %q is not allowed", host)
		}
		return nil
	}
	for _, h := range AllowedHosts {
		if host == h {
			return nil
		}
	}
	return fmt.Errorf("host %q is not allowed", host)
}

func (f *Fetcher) client() *http.Client {
	c := f.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Second}
	}
	cp := *c
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return errors.New("too many redirects")
		}
		return f.allowed(req.URL)
	}
	return &cp
}

// Get fetches rawURL and returns at most max bytes; a longer body is an error, not a truncation.
func (f *Fetcher) Get(ctx context.Context, rawURL string, max int64) ([]byte, error) {
	var buf limitBuffer
	buf.max = max
	if err := f.Copy(ctx, rawURL, &buf, max); err != nil {
		return nil, err
	}
	return buf.b, nil
}

// Copy streams rawURL into w, failing when the body is longer than max (what was written is then not to be used).
func (f *Fetcher) Copy(ctx context.Context, rawURL string, w io.Writer, max int64) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if err := f.allowed(u); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &StatusError{URL: u.Redacted(), Status: resp.StatusCode, Text: resp.Status}
	}
	if resp.ContentLength > max {
		return fmt.Errorf("%w: %d bytes, more than %d", ErrTooLarge, resp.ContentLength, max)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, max+1))
	if err != nil {
		return err
	}
	if n > max {
		return fmt.Errorf("%w: longer than %d bytes", ErrTooLarge, max)
	}
	return nil
}

type limitBuffer struct {
	b   []byte
	max int64
}

func (l *limitBuffer) Write(p []byte) (int, error) {
	if int64(len(l.b)+len(p)) > l.max+1 {
		return 0, ErrTooLarge
	}
	l.b = append(l.b, p...)
	return len(p), nil
}
