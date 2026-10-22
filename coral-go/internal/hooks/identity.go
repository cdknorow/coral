package hooks

import (
	"errors"
	"github.com/subgentic/coral/internal/serverproof"
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

	// proof is base with every new connection proved before the token goes on it (#6103): set for a "v2." session token on macOS and Linux (the Windows check of who owns the port is used there, and an
	// older token's tools do not ask). nil: the token is sent as before.
	proof http.RoundTripper
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
	} else if t.plainHTTPOffThisComputer(r.URL) && (t.token != "" || r.Header.Get(SessionTokenHeader) != "") {
		// The agent's own Coral, but over plain http to an address that is not this computer: the token would cross the
		// network in clear text. Refused with a reason rather than sent without it, which the server would only refuse (#224).
		return nil, &PlainHTTPTokenError{Host: r.URL.Host}
	} else if !t.tokenAllowed(r.URL) {
		r.Header.Del(SessionTokenHeader)
	} else if (t.token != "" || r.Header.Get(SessionTokenHeader) != "") && t.localCoral(r.URL) && verifyLocalListener(r.URL) != nil {
		// A Coral on this computer, but the program on its port is not this user's (#224 B): fail closed, say why.
		return nil, verifyLocalListener(r.URL)
	} else if t.token != "" && r.Header.Get(SessionTokenHeader) == "" {
		r.Header.Set(SessionTokenHeader, t.token)
	}
	if r.Header.Get("User-Agent") == "" {
		r.Header.Set("User-Agent", t.tool+"/"+Version)
	}
	if t.proof != nil && r.Response == nil && r.URL.Scheme == "http" && r.Header.Get(SessionTokenHeader) != "" && t.localCoral(r.URL) {
		return t.proof.RoundTrip(r) // the token goes only on a connection whose server answered the challenge (#6103)
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
	http.DefaultTransport = identityTransport{base: base, tool: tool, token: token, hosts: configuredHosts(), proof: sessionProof(base, token)}
}

// proofEnabled: macOS and Linux prove the program on Coral's port with a challenge; Windows checks who owns it (listener_owner_windows.go).
var proofEnabled = runtime.GOOS != "windows"

// DisableProofForTests turns the proof off for the rest of the process: a test whose stand-in server does not serve /api/system/proof says so. Nothing else calls it.
func DisableProofForTests() { proofEnabled = false }

// sessionProof is the transport that proves each new connection to a Coral on this computer before the session token goes on it, or nil when there is nothing to prove with: no token, a token without
// the "v2." mark (an agent started before proof keys existed), or a base whose dialing cannot be hooked.
func sessionProof(base http.RoundTripper, token string) http.RoundTripper {
	if !proofEnabled || !strings.HasPrefix(token, serverproof.SessionTokenPrefix) {
		return nil
	}
	rt, ok := serverproof.NewTransport(base, serverproof.Credential{Kind: serverproof.KindSession, Secret: token})
	if !ok {
		return nil
	}
	return rt
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

// PlainHTTPTokenError: the agent's session token was not sent to its Coral because the address is plain http and not this
// computer (#224).
type PlainHTTPTokenError struct{ Host string }

func (e *PlainHTTPTokenError) Error() string {
	return "Refused: the agent's token was not sent over plain http to " + e.Host + ", which is not this computer, because it would cross " +
		"the network unencrypted. Use an https:// CORAL_URL, or 127.0.0.1 for the Coral on this computer."
}

// TokenWithheld marks the error as Coral's own refusal to send the token (see ForeignListenerError.TokenWithheld).
func (e *PlainHTTPTokenError) TokenWithheld() bool { return true }

// tokenAllowed: the request goes to this agent's own Coral, over a channel that keeps the token on this computer or encrypted:
// loopback, https to a configured host, or plain http to a configured host that is an address of this computer itself (a
// Coral started with --host on one LAN address gives its agents that address, and the traffic never leaves the computer).
func (t identityTransport) tokenAllowed(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	if isLoopback(host) {
		return true
	}
	if !t.configured(host) {
		return false
	}
	return u.Scheme == "https" || localInterfaceIP(host)
}

// plainHTTPOffThisComputer: an http request to a configured Coral host that is neither loopback nor an address of this
// computer, which tokenAllowed refuses and RoundTrip reports.
func (t identityTransport) plainHTTPOffThisComputer(u *url.URL) bool {
	if u == nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return !isLoopback(host) && t.configured(host) && !localInterfaceIP(host)
}

// localCoral: the request goes to a Coral on this computer (loopback, or a configured address of this computer itself), where
// the program on the port can be checked before the token goes (#224 B).
func (t identityTransport) localCoral(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	return isLoopback(host) || (t.configured(host) && localInterfaceIP(host))
}

// configured: host is one Coral named in the agent's environment.
func (t identityTransport) configured(host string) bool {
	for _, h := range t.hosts {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	return false
}

// interfaceAddrs is net.InterfaceAddrs; a test replaces it.
var interfaceAddrs = net.InterfaceAddrs

// localInterfaceIP reports whether host is an IP literal assigned to one of this computer's network interfaces. A name is
// not looked up (it could resolve elsewhere by the time the request dials): a Coral on this computer reached by a name needs
// https, or its IP address.
func localInterfaceIP(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	addrs, err := interfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		var local net.IP
		switch v := a.(type) {
		case *net.IPNet:
			local = v.IP
		case *net.IPAddr:
			local = v.IP
		}
		if local != nil && local.Equal(ip) {
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
