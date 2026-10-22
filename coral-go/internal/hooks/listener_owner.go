package hooks

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"
)

// ForeignListenerError: the agent's session token was not sent to a Coral on this computer, because the process listening on
// that port belongs to another account (#224 B). On a shared Windows computer (a terminal server) ports are machine-wide, so
// another account can listen on Coral's port while Coral is down and collect the tokens agents send it.
type ForeignListenerError struct {
	Port int
	PID  uint32
	Why  string
}

func (e *ForeignListenerError) Error() string {
	return fmt.Sprintf("coral: not sending this agent's session token to port %d on this computer: the process listening there (PID %d) %s. "+
		"Is another account's program on Coral's port? Start Coral, or check what is listening there", e.Port, e.PID, e.Why)
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

// listenerVerified remembers, for this process, each local Coral address already checked ("host:port" -> its error, nil when the
// listener is this user's), so the check (the platform's checkListener) runs once per address, not once per request.
var listenerVerified sync.Map

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
	if v, ok := listenerVerified.Load(key); ok {
		if v == nil {
			return nil
		}
		return v.(error)
	}
	err = checkListener(host, port)
	if err == nil {
		listenerVerified.Store(key, nil)
	} else {
		listenerVerified.Store(key, err)
	}
	return err
}
