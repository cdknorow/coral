package main

import (
	"net/http"

	"github.com/subgentic/coral/internal/clierr"
)

// withheldNoter notes Refused for any request whose token the agent tools withheld (a foreign program on Coral's port, #99), so
// the command that then fails exits refused (5), as coral-agent does, not 1 (Security's #260 pass). PID resolution prints the
// refusal itself (withheldMessage); the noter covers any other request whose error a command swallows.
type withheldNoter struct{ base http.RoundTripper }

func (t withheldNoter) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil && clierr.KindOf(err) == clierr.Refused {
		clierr.Note(clierr.Refused)
	}
	return resp, err
}

// withheldMessage is the refusal to print when err is a request whose token the agent tools withheld (ok false otherwise). It
// notes Refused, so clierr.Exit1 exits 5.
func withheldMessage(err error) (string, bool) {
	if err == nil || clierr.KindOf(err) != clierr.Refused {
		return "", false
	}
	return clierr.CannotReach(redactedServerURL(), err, 0).Error(), true
}

// installWithheldNoter wraps the default transport (after the identity and signing transports are installed).
func installWithheldNoter() { http.DefaultTransport = withheldNoter{base: http.DefaultTransport} }
