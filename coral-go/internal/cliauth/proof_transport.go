package cliauth

import (
	"net/http"
	"runtime"
	"sync"

	"github.com/subgentic/coral/internal/serverproof"
)

// The command line proves the program on Coral's port on every new connection before anything is sent on it (#6103, serverproof): the hello and the signed request then travel on a connection whose
// server has answered a challenge bound to it, so a program that took the port while Coral was stopped, and relays the hello to the real Coral, gets neither. On Windows the check of who owns the
// port (#224 B) is used instead.
var proofEnabled = runtime.GOOS != "windows"

// notCoral is a refusal to send (the program on the port did not prove it is Coral) that is also ErrNotCoral: callers that classify or retry on ErrNotCoral (a program that is not this Coral answered)
// keep doing so, and errors.As still finds the RefusedError with its wording and its "withheld" mark.
type notCoral struct{ err error }

func (e notCoral) Error() string   { return e.err.Error() }
func (e notCoral) Unwrap() []error { return []error{ErrNotCoral, e.err} }

// DisableProofForTests turns the proof off for the rest of the process: a test whose stand-in server does not serve /api/system/proof says so in its TestMain. Nothing else calls it.
func DisableProofForTests() { proofEnabled = false }

// proofTransports builds, per command-line token, the transport that proves each new connection (the token can change while a tool runs: Coral restarted with a new one).
type proofTransports struct {
	base http.RoundTripper
	mu   sync.Mutex
	by   map[string]http.RoundTripper
}

func newProofTransports(base http.RoundTripper) *proofTransports {
	if !proofEnabled {
		return nil
	}
	return &proofTransports{base: base, by: map[string]http.RoundTripper{}}
}

// get is the proving transport for token, or nil when base cannot be hooked (then nothing is proved, as before).
func (p *proofTransports) get(token string) http.RoundTripper {
	if p == nil || token == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if rt, ok := p.by[token]; ok {
		return rt
	}
	rt, ok := serverproof.NewTransport(p.base, serverproof.Credential{Kind: serverproof.KindOperator, Secret: token})
	if !ok {
		rt = nil
	}
	p.by[token] = rt
	return rt
}

// baseHTTP is the transport a command-line client dials with: the default transport when it is a plain *http.Transport, else a fresh one with the environment's proxy rules.
func baseHTTP() http.RoundTripper {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		return t
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}
