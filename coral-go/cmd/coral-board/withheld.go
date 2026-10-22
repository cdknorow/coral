package main

import (
	"net/http"

	"github.com/subgentic/coral/internal/clierr"
)

// withheldNoter notes Refused for any request whose token the agent tools withheld (a foreign program on Coral's port, #99), so
// the command that then fails exits refused (5), as coral-agent does, not 1 (Security's #260 pass). PID resolution swallows its
// request's error and the command reports "Not subscribed to any board" instead: the transport is the one place that sees it.
type withheldNoter struct{ base http.RoundTripper }

func (t withheldNoter) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil && clierr.KindOf(err) == clierr.Refused {
		clierr.Note(clierr.Refused)
	}
	return resp, err
}

// installWithheldNoter wraps the default transport (after the identity and signing transports are installed).
func installWithheldNoter() { http.DefaultTransport = withheldNoter{base: http.DefaultTransport} }
