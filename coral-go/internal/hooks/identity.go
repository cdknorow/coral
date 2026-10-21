package hooks

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/subgentic/coral/internal/agentenv"
	"github.com/subgentic/coral/internal/cliauth"
)

// Identity for an agent's own tools (#3387). Coral's launch line puts the agent session's
// token in CORAL_SESSION_TOKEN before the agent CLI starts; coral-board, coral-agent and
// the coral-hook-* binaries inherit it and send it with every request to Coral. The
// agent itself never reads, passes or sees it: nothing in a prompt or a tool call
// touches it.
//
// The token goes only to this agent's own Coral (#3581): a loopback address, or the host
// Coral named in CORAL_URL or CORAL_HOST. A redirect to another host or scheme is refused,
// and a redirect that is followed never carries the token.

// SessionTokenHeader carries the agent session's token to Coral.
const SessionTokenHeader = "X-Coral-Session-Token"

// sessionTokenEnv is set in the agent's shell by Coral's launch line.
const sessionTokenEnv = "CORAL_SESSION_TOKEN"

// Version is reported in the tools' User-Agent.
var Version = "dev"

type identityTransport struct {
	base  http.RoundTripper
	tool  string
	token string
	hosts []string // the configured Coral's hosts, besides loopback
}

// errRedirectRefused: a reply redirected the request to another host or scheme.
var errRedirectRefused = errors.New("coral: a redirect to another host or scheme was refused")

func (t identityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if r.Response != nil {
		// The client is following a redirect: same host and scheme only, and no token.
		if prev := r.Response.Request; prev != nil && prev.URL != nil &&
			(!strings.EqualFold(prev.URL.Scheme, r.URL.Scheme) || !strings.EqualFold(prev.URL.Host, r.URL.Host)) {
			return nil, errRedirectRefused
		}
		r.Header.Del(SessionTokenHeader)
	} else if !t.tokenAllowed(r.URL) {
		r.Header.Del(SessionTokenHeader)
	} else if t.token != "" && r.Header.Get(SessionTokenHeader) == "" {
		r.Header.Set(SessionTokenHeader, t.token)
	}
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", t.tool+"/"+Version)
	}
	return t.base.RoundTrip(r)
}

// InstallIdentity makes every request this process sends through the default HTTP
// transport carry the agent session's token (if Coral gave it one) and name the tool
// in its User-Agent. Call it first thing in main.
func InstallIdentity(tool string) {
	token := sessionToken()
	base := http.DefaultTransport
	if _, done := base.(identityTransport); done {
		return
	}
	http.DefaultTransport = identityTransport{base: base, tool: tool, token: token, hosts: configuredHosts()}
}

// sessionToken is the agent session's token: CORAL_SESSION_TOKEN, or when that is missing (in
// a command Codex ran, which hides it, #13) the file agentenv.SessionAuthFileEnv names, read
// with cliauth's private-file checks. The file never overrides the variable, and a file that
// fails the checks gives no token (the request goes out without one, refused like any other).
func sessionToken() string {
	if tok := strings.TrimSpace(os.Getenv(sessionTokenEnv)); tok != "" {
		return tok
	}
	if p := strings.TrimSpace(os.Getenv(agentenv.SessionAuthFileEnv)); p != "" && inSessionTokens(p) {
		if tok, err := cliauth.ReadPrivateFile(p); err == nil {
			return tok
		}
	}
	return ""
}

// inSessionTokens reports whether p is a file directly in this Coral's session_tokens folder
// (<data dir>/session_tokens, the data dir as cliauth.DataDir finds it), the only place Coral
// writes a session's token file: a variable naming any other file gives no token (#13 review N1).
func inSessionTokens(p string) bool {
	want := filepath.Clean(filepath.Join(cliauth.DataDir(), "session_tokens"))
	got := filepath.Clean(filepath.Dir(p))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(got, want)
	}
	return got == want
}

// tokenAllowed: the request goes to this agent's own Coral.
func (t identityTransport) tokenAllowed(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	if isLoopback(host) {
		return true
	}
	for _, h := range t.hosts {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	return false
}

// configuredHosts: the hosts Coral named in the agent's environment (CORAL_URL, CORAL_HOST).
func configuredHosts() []string {
	var hosts []string
	if u, err := url.Parse(strings.TrimSpace(os.Getenv("CORAL_URL"))); err == nil && u.Hostname() != "" {
		hosts = append(hosts, u.Hostname())
	}
	if h := strings.Trim(strings.TrimSpace(os.Getenv("CORAL_HOST")), "[]"); h != "" && h != "0.0.0.0" && h != "::" {
		hosts = append(hosts, h)
	}
	return hosts
}

func isLoopback(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// TrustedServerURL reports whether a server address read from a file (a board state file's
// server_url) may be used: a loopback http(s) address, or exactly the configured Coral
// address. Coral writes a loopback address there for sandboxes that drop CORAL_URL and
// CORAL_PORT; anything else could send the agent's token elsewhere (#3581).
func TrustedServerURL(candidate, configured string) bool {
	candidate = strings.TrimRight(strings.TrimSpace(candidate), "/")
	u, err := url.Parse(candidate)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return false
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return isLoopback(u.Hostname()) || strings.EqualFold(candidate, strings.TrimRight(strings.TrimSpace(configured), "/"))
}
