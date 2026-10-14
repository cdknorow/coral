package httputil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Outbound webhooks (#3363). Coral POSTs to URLs that users, agents and remote boards
// supply: notification webhooks, job-run callbacks and board subscriber webhooks. Each of
// them must not become a way to reach this machine or its network (cloud metadata at
// 169.254.169.254, a router admin page, another local service), so every outbound webhook
// goes through WebhookClient: its dialer resolves the host and refuses a blocked address
// at connect time (a DNS answer that changes between the check and the connect cannot
// slip through), redirects go through the same dialer, and no proxy from the environment
// is used. URLs are also checked when they are saved (ValidateWebhookURL), so a bad one is
// refused with a clear message instead of failing later.
//
// People who send webhooks to their own LAN (a home-automation box, a local chat bridge)
// turn on the webhook_allow_private setting, or CORAL_WEBHOOK_ALLOW_PRIVATE=1; then only
// the scheme and host are checked.

// ErrWebhookTargetBlocked is returned for a webhook URL that resolves to a loopback,
// private, link-local or otherwise reserved address while private targets are not allowed.
var ErrWebhookTargetBlocked = errors.New("webhook URL points to this computer or a private network (turn on 'Allow webhooks to private networks' to permit it)")

var webhookAllowPrivate atomic.Pointer[func() bool]

// SetWebhookAllowPrivate sets how outbound webhooks learn whether private targets are
// allowed (the server reads the setting on each call). nil means never.
func SetWebhookAllowPrivate(f func() bool) {
	if f == nil {
		webhookAllowPrivate.Store(nil)
		return
	}
	webhookAllowPrivate.Store(&f)
}

// WebhookPrivateAllowed reports whether webhooks may target private addresses now.
func WebhookPrivateAllowed() bool {
	if f := webhookAllowPrivate.Load(); f != nil {
		return (*f)()
	}
	return false
}

// lookupIP is the resolver; tests replace it to make a hostname resolve to a blocked address.
var lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// blockedIP decides which addresses are refused; tests replace it so an httptest server on
// 127.0.0.1 can stand in for a public host.
var blockedIP = IsIPBlocked

// ValidateWebhookURL checks a webhook URL when it is saved: http or https, a host, and
// (unless private targets are allowed) every address the host resolves to is public.
func ValidateWebhookURL(ctx context.Context, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("webhook URL is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("webhook URL must start with http:// or https://")
	}
	if u.User != nil {
		return fmt.Errorf("webhook URL must not contain a user name or password")
	}
	if WebhookPrivateAllowed() {
		return nil
	}
	_, err = publicIPs(ctx, u.Hostname())
	return err
}

// publicIPs resolves host and returns its addresses, or ErrWebhookTargetBlocked if any of
// them is blocked (one bad answer is enough: a resolver could hand it out next time).
func publicIPs(ctx context.Context, host string) ([]net.IP, error) {
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		var err error
		if ips, err = lookupIP(ctx, host); err != nil {
			return nil, fmt.Errorf("webhook host %q could not be resolved", host)
		}
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("webhook host %q could not be resolved", host)
	}
	for _, ip := range ips {
		if blockedIP(ip) {
			return nil, ErrWebhookTargetBlocked
		}
	}
	return ips, nil
}

// WebhookClient returns an HTTP client for outbound webhooks with the guards above.
func WebhookClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		Proxy: nil, // an HTTP(S)_PROXY would connect for us, past the dial-time check
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if WebhookPrivateAllowed() {
				return dialer.DialContext(ctx, network, addr)
			}
			ips, err := publicIPs(ctx, host)
			if err != nil {
				return nil, err
			}
			// Dial the address that was checked, never a fresh lookup.
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("webhook: too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("webhook: redirect to a non-http URL")
			}
			return nil // the target is dialled through the same guarded dialer
		},
	}
}
