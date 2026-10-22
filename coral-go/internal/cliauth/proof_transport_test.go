package cliauth

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/serverproof"
)

// #6103: the command line proves the program on Coral's port on every new connection, so the hello and the signed request only travel to a server that answered a challenge bound to that connection.

func withProof(t *testing.T) {
	t.Helper()
	prev := proofEnabled
	proofEnabled = true
	t.Cleanup(func() { proofEnabled = prev })
}

// A Coral: the proof route (the command-line token's key), the hello and signed requests; it records the connection of each.
type provingCoral struct {
	mu     sync.Mutex
	proofs []string
	signed []string // RemoteAddr of each signed request
}

func (p *provingCoral) handler(token string) http.Handler {
	a := NewCLIAuth(token)
	mux := http.NewServeMux()
	mux.Handle(serverproof.Path, serverproof.Handler(func(_ context.Context, kind, _ string) ([]byte, bool) { return a.ProofKey() }, nil, nil))
	mux.HandleFunc(CLIHelloPath, func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Nonce string }
		_ = json.NewDecoder(r.Body).Decode(&b)
		sn, proof, err := a.Hello(b.Nonce)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"nonce": sn, "proof": proof})
	})
	mux.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !a.Verify(r, body) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		p.mu.Lock()
		p.signed = append(p.signed, r.RemoteAddr)
		p.mu.Unlock()
		io.WriteString(w, "ok")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == serverproof.Path {
			p.mu.Lock()
			p.proofs = append(p.proofs, r.RemoteAddr)
			p.mu.Unlock()
		}
		mux.ServeHTTP(w, r)
	})
}

func TestCommandLineTransportProvesTheServerOnTheConnectionItSendsOn(t *testing.T) {
	withProof(t)
	p := &provingCoral{}
	srv := httptest.NewServer(p.handler("the-token"))
	t.Cleanup(srv.Close)
	base := &http.Transport{}
	c := &http.Client{Transport: Transport{Base: base, Token: "the-token", Proof: newProofTransports(base)}}
	for i := 0; i < 2; i++ {
		resp, err := c.Post(srv.URL+"/api/echo", "application/json", strings.NewReader(`{"a":1}`))
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, 200, resp.StatusCode)
	}
	require.Len(t, p.signed, 2)
	assert.LessOrEqual(t, len(p.proofs), 2, "the hello and the signed requests reuse the proved connection")
	for _, conn := range p.signed {
		assert.Contains(t, p.proofs, conn, "every signed request travelled on a connection that was proved")
	}
}

// the exposure the proof closes: a program that took the port relays everything to the real Coral on another address of the same port, and returns the real hello's proof. Without the connection proof
// the relay works (it sees, and passes on, the person's request); with it nothing is sent past the first challenge.
func TestARelayThatForwardsTheHelloToTheRealCoralGetsNothing(t *testing.T) {
	realLn, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback here")
	}
	_, port, _ := net.SplitHostPort(realLn.Addr().String())
	impLn, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		realLn.Close()
		t.Skip("the port is not free on 127.0.0.1")
	}
	real := &provingCoral{}
	realSrv := &http.Server{Handler: real.handler("the-token")}
	go realSrv.Serve(realLn)
	t.Cleanup(func() { realSrv.Close() })
	var mu sync.Mutex
	var sawBody []string
	imp := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sawBody = append(sawBody, r.URL.Path+" "+string(b))
		mu.Unlock()
		req, _ := http.NewRequest(r.Method, "http://"+realLn.Addr().String()+r.URL.RequestURI(), strings.NewReader(string(b)))
		req.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})}
	go imp.Serve(impLn)
	t.Cleanup(func() { imp.Close() })
	url := "http://" + impLn.Addr().String() + "/api/echo"

	t.Run("without the proof the relay passes the request on", func(t *testing.T) {
		withProofOff := proofEnabled
		proofEnabled = false
		defer func() { proofEnabled = withProofOff }()
		c := &http.Client{Transport: Transport{Base: &http.Transport{}, Token: "the-token"}}
		resp, err := c.Post(url, "application/json", strings.NewReader(`{"secret":"the person's message"}`))
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, 200, resp.StatusCode, "the exposure: the relay is invisible to the hello alone")
		mu.Lock()
		assert.Contains(t, strings.Join(sawBody, "|"), "the person's message", "and it read the request")
		mu.Unlock()
	})
	t.Run("with the proof it is refused and sees no request", func(t *testing.T) {
		withProof(t)
		mu.Lock()
		sawBody = nil
		mu.Unlock()
		real.mu.Lock()
		real.signed = nil
		real.mu.Unlock()
		base := &http.Transport{}
		c := &http.Client{Transport: Transport{Base: base, Token: "the-token", Proof: newProofTransports(base)}}
		_, err := c.Post(url, "application/json", strings.NewReader(`{"secret":"the person's message"}`))
		var re *serverproof.RefusedError
		require.ErrorAs(t, err, &re)
		assert.True(t, re.TokenWithheld())
		mu.Lock()
		defer mu.Unlock()
		assert.NotContains(t, strings.Join(sawBody, "|"), "the person's message")
		assert.NotContains(t, strings.Join(sawBody, "|"), CLIHelloPath, "not even the hello went on")
		real.mu.Lock()
		defer real.mu.Unlock()
		assert.Empty(t, real.signed)
	})
}

// Coral restarted with a new command-line token while a tool was running: the proof fails with the old key, the token is read again from the data folder, once, and the request goes through.
func TestANewTokenAfterARestartIsReadAgainWhenTheProofFails(t *testing.T) {
	withProof(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, TokenFile), []byte("the-new-token\n"), 0o600))
	p := &provingCoral{}
	srv := httptest.NewServer(p.handler("the-new-token"))
	t.Cleanup(srv.Close)
	base := &http.Transport{}
	c := &http.Client{Transport: Transport{Base: base, Token: "the-old-token", Dir: dir, Proof: newProofTransports(base)}, Timeout: 5 * time.Second}
	resp, err := c.Post(srv.URL+"/api/echo", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)

	// and a server that cannot prove with either token is still refused
	other := httptest.NewServer((&provingCoral{}).handler("some-other-token"))
	t.Cleanup(other.Close)
	_, err = c.Post(other.URL+"/api/echo", "application/json", strings.NewReader(`{}`))
	var re *serverproof.RefusedError
	require.ErrorAs(t, err, &re)
}

func TestCLIClientProvesTheServerToo(t *testing.T) {
	withProof(t)
	p := &provingCoral{}
	srv := httptest.NewServer(p.handler("the-token"))
	t.Cleanup(srv.Close)
	resp, err := CLIClient{Base: srv.URL, Token: "the-token"}.Do(context.Background(), "POST", "/api/echo", []byte(`{}`), nil, 5*time.Second)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.Len(t, p.proofs, 1)

	imposter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	t.Cleanup(imposter.Close)
	_, err = CLIClient{Base: imposter.URL, Token: "the-token"}.Do(context.Background(), "POST", "/api/echo", []byte(`{}`), nil, 5*time.Second)
	var re *serverproof.RefusedError
	require.ErrorAs(t, err, &re)
}
