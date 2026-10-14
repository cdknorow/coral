package hooks

import (
	"net/http"
	"os"
	"strings"
)

// Identity for an agent's own tools (#3387). Coral's launch line puts the agent session's
// token in CORAL_SESSION_TOKEN before the agent CLI starts; coral-board, coral-agent and
// the coral-hook-* binaries inherit it and send it with every request to Coral. The
// agent itself never reads, passes or sees it: nothing in a prompt or a tool call
// touches it.

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
}

func (t identityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if t.token != "" && r.Header.Get(SessionTokenHeader) == "" {
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
	token := strings.TrimSpace(os.Getenv(sessionTokenEnv))
	base := http.DefaultTransport
	if _, done := base.(identityTransport); done {
		return
	}
	http.DefaultTransport = identityTransport{base: base, tool: tool, token: token}
}
