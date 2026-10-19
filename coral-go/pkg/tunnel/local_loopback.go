// The loopback HTTP client the connector dials the local server with (moved out of local.go, #5091; no behaviour change).

package tunnel

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// loopbackDials caps the connector's dials to the local server in progress at once, and a dial
// the local server refused is tried loopbackDialRetries more times (10, 20 and 40 ms apart).
const (
	loopbackDials       = 32
	loopbackDialRetries = 3
)

// NewLoopbackClient returns an http.Client and base URL for the local Coral
// API. base must be http(s) on a loopback host. The host is resolved once and
// every connection goes to that pinned loopback IP, whatever the request
// says, and the dial is re-checked at connect time. Redirects are never
// followed and proxies are never used.
func NewLoopbackClient(base string) (*http.Client, *url.URL, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, nil, errors.New("tunnel: invalid local url")
	}
	var pinned net.IP
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		pinned = ip
	} else {
		ips, err := net.LookupIP(u.Hostname())
		if err != nil || len(ips) == 0 {
			return nil, nil, errors.New("tunnel: cannot resolve local host")
		}
		pinned = ips[0]
		for _, ip := range ips {
			if !ip.IsLoopback() {
				return nil, nil, errors.New("tunnel: local host is not loopback")
			}
		}
	}
	if !pinned.IsLoopback() {
		return nil, nil, errors.New("tunnel: local host is not loopback")
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	addr := net.JoinHostPort(pinned.String(), port)
	d := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			h, _, err := net.SplitHostPort(address)
			if err != nil {
				return errors.New("tunnel: non-loopback dial refused")
			}
			if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
				return errors.New("tunnel: non-loopback dial refused")
			}
			return nil
		},
	}
	// A burst through the hub (up to the hub limits, 1024 when raised) dialed the local server
	// all at once, and on Windows the listener's accept queue overflowed: some dials were
	// refused and those requests failed ("local request failed", #4011). Dials now wait for one
	// of loopbackDials slots, and a refused dial (nothing was sent yet) is tried again shortly.
	slots := make(chan struct{}, loopbackDials)
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			defer func() { <-slots }()
			for attempt := 0; ; attempt++ {
				c, err := d.DialContext(ctx, network, addr)
				if err == nil || attempt == loopbackDialRetries || !connRefused(err) {
					return c, err
				}
				select {
				case <-time.After(time.Duration(10<<attempt) * time.Millisecond):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
		},
		DisableCompression:  true,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     60 * time.Second,
		// Headers must arrive promptly; bodies (SSE, downloads) stream freely.
		ResponseHeaderTimeout: 60 * time.Second,
	}
	cl := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return cl, u, nil
}
