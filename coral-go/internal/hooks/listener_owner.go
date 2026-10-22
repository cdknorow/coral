package hooks

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// ForeignListenerError: the agent's session token was not sent to a Coral on this computer, because the process listening on
// that port belongs to another account (#224 B). On a shared Windows computer (a terminal server) ports are machine-wide, so
// another account can listen on Coral's port while Coral is down and collect the tokens agents send it.
type ForeignListenerError struct {
	Port int
	PID  uint32
	Why  string // what the program on the port is, and what to do: whole sentences (#279)
}

func (e *ForeignListenerError) Error() string {
	return fmt.Sprintf("Refused: the agent's token was not sent to port %d on this computer. %s", e.Port, e.Why)
}

// TokenWithheld marks the error as Coral's own refusal to send the token, not a server that couldn't be reached: the CLIs exit
// "refused" (5) for it, not "cannot reach" (3), so a script never takes a foreign listener for a Coral that isn't running and
// starts or retries one (Security's #260 pass). A method, not a type the CLIs' error classes import.
func (e *ForeignListenerError) TokenWithheld() bool { return true }

// AllowTestServerListener lets a test's own server pass the Windows check: the test process itself, or a process running this
// tool's own executable (a test that runs the CLI as a child of the test binary, which serves on the port). Tests set it (the
// hooks tests' TestMain, coral-agent's helper process); a shipped tool never does, so the rule there is exactly Coral's image and
// owner. It does nothing on platforms without the check.
var AllowTestServerListener bool

// Cache only briefly: another process may take Coral's port after it stops, and a
// transient lookup failure must not refuse a long-running tool forever.
const listenerPassTTL = 30 * time.Second
const listenerErrorTTL = 2 * time.Second

type listenerVerdict struct {
	err   error
	until time.Time
}

var listenerVerified sync.Map
var listenerNow = time.Now

// verifyLocalListener: u is a Coral on this computer (loopback or one of its own addresses); its listener must be this user's.
func verifyLocalListener(u *url.URL) error {
	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		if u.Scheme == "https" {
			port = 443
		} else {
			port = 80
		}
	}
	key := net.JoinHostPort(host, strconv.Itoa(port))
	now := listenerNow()
	if v, ok := listenerVerified.Load(key); ok {
		verdict := v.(listenerVerdict)
		if now.Before(verdict.until) {
			return verdict.err
		}
	}
	err = checkListener(host, port)
	ttl := listenerPassTTL
	if err != nil {
		ttl = listenerErrorTTL
	}
	listenerVerified.Store(key, listenerVerdict{err: err, until: now.Add(ttl)})
	return err
}
