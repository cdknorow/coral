// Package loginlink gives Coral's own apps (the desktop window coral-app, the tray and
// launch-coral) a dashboard URL that signs them in (#3394), so they keep working once the
// dashboard on this computer needs a session cookie (identity phase 2).
//
// A link carries a single-use login token (#3387, 10 minutes): opening it trades the token
// for the session cookie and redirects to the same page without it. So each open mints a new
// one. An app that runs the server in its own process mints it there (RunningServer.
// NewLoginLink); coral-app, a separate process, asks the Coral on this computer through the
// command-line handshake (FromCLI), as 'coral open' does. Neither needs the API key.
//
// A link is a credential for 10 minutes: never log it; log Redact(link).
package loginlink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/cloud"
)

// Param is the query parameter that carries the login token (auth.LoginTokenParam).
const Param = "login_token"

// Or returns a fresh sign-in link from mint, or base if there is none: a Coral without
// login links (before #3387) or a failure. Until strict mode, base still opens the dashboard
// on this computer; with it, the dashboard asks to sign in.
func Or(base string, mint func() (string, error)) string {
	if mint == nil {
		return base
	}
	link, err := mint()
	if err != nil || link == "" {
		if err != nil {
			log.Printf("[login] no sign-in link (%v); opening %s", err, base)
		}
		return base
	}
	return link
}

// ErrNotLocal is returned for a server that is not on this computer.
var ErrNotLocal = errors.New("login links are only asked from the Coral on this computer")

// FromCLI asks the Coral at base (on this computer) for a fresh sign-in link, proving it is
// the same user through the command-line token in coralDir (<data>/cli-token, 0600). It
// retries until ctx ends while Coral is still starting.
func FromCLI(ctx context.Context, base, coralDir string) (string, error) {
	base = strings.TrimRight(base, "/")
	if !local(base) {
		return "", ErrNotLocal
	}
	var last error
	for {
		link, retry, err := fromCLIOnce(ctx, base, coralDir)
		if err == nil || !retry {
			return link, err
		}
		last = err
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("coral did not answer: %w", last)
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func fromCLIOnce(ctx context.Context, base, coralDir string) (link string, retry bool, err error) {
	token, err := cloud.ReadCLIToken(coralDir)
	if err != nil {
		return "", true, err // written at startup: Coral may not be up yet
	}
	resp, err := cloud.CLIClient{Base: base, Token: token}.Do(ctx, http.MethodPost, "/api/system/login-token", []byte("{}"), nil, 5*time.Second)
	if errors.Is(err, cloud.ErrNotCoral) {
		return "", false, err // something else answers there: never ask it again
	}
	if err != nil {
		return "", true, err // not listening yet
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", false, errors.New("this Coral has no sign-in links")
	}
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode >= 500, fmt.Errorf("sign-in link: %s", resp.Status)
	}
	var reply struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&reply); err != nil || !strings.HasPrefix(reply.Path, "/") || strings.HasPrefix(reply.Path, "//") {
		return "", false, errors.New("unexpected sign-in link answer")
	}
	return base + reply.Path, false, nil
}

// local reports whether base is an http(s) URL on this computer.
func local(base string) bool {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Redact returns link without its login token, for logs.
func Redact(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return "(unparseable URL)"
	}
	q := u.Query()
	if q.Has(Param) {
		q.Set(Param, "REDACTED")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// AppParam and AppValue mark a sign-in link as the Coral app's (auth.AppClientParam, auth.AppClientApp): the session it makes is named "Coral app
// on ..." on the Sessions page.
const (
	AppParam = "coral_client"
	AppValue = "app"
)

// MarkApp adds the Coral app's mark to a link that carries a login token, leaving its path exactly as it is; any other URL is returned as it was.
func MarkApp(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Query().Get(Param) == "" || u.Query().Get(AppParam) == AppValue {
		return link
	}
	if u.RawQuery != "" {
		u.RawQuery += "&"
	}
	u.RawQuery += AppParam + "=" + AppValue
	return u.String()
}

// HasToken reports whether link already carries a login token.
func HasToken(link string) bool {
	u, err := url.Parse(link)
	return err == nil && u.Query().Get(Param) != ""
}

// ValidPage reports whether page is a page of the dashboard: a path from its root ("/team/qa",
// "/chats?q=x"), never another host ("//evil.example", "https://…"), a fragment or a token.
// Coral serves the page or sends it home, so only the shape is checked (#3532).
func ValidPage(page string) bool {
	if !strings.HasPrefix(page, "/") || strings.HasPrefix(page, "//") || strings.ContainsAny(page, "\\#\x00\r\n") {
		return false
	}
	u, err := url.Parse(page)
	return err == nil && u.Scheme == "" && u.Host == "" && !u.Query().Has(Param)
}

// OnPage returns link with its token moved onto page of the same dashboard: opening it signs
// in and lands on page (the server strips the token and keeps the path exactly as written).
// link is returned unchanged when page is not a valid page or link has no token.
func OnPage(link, page string) string {
	if page == "" || page == "/" || !ValidPage(page) {
		return link
	}
	u, err := url.Parse(link)
	if err != nil || u.Query().Get(Param) == "" {
		return link
	}
	sep := "?"
	if strings.Contains(page, "?") {
		sep = "&"
	}
	return u.Scheme + "://" + u.Host + page + sep + Param + "=" + url.QueryEscape(u.Query().Get(Param))
}
