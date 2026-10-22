package hooks

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/serverproof"
)

// #6103: an agent's tools send a "v2." session token only on a connection whose server answered the proof challenge; an older token's tools do not ask, so an agent started before proofs existed
// keeps working.

const v2Token = serverproof.SessionTokenPrefix + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abcde"

func enableProof(t *testing.T) {
	t.Helper()
	prev := proofEnabled
	proofEnabled = true
	t.Cleanup(func() { proofEnabled = prev })
}

// a Coral that proves itself with the session key of token, and records what it was sent.
type coralSeen struct {
	mu     sync.Mutex
	tokens []string
	proofs int
	conns  [][2]string // [proof RemoteAddr, token request RemoteAddr] per token request
	last   string
}

func provingServer(t *testing.T, token string) (*httptest.Server, *coralSeen) {
	t.Helper()
	seen := &coralSeen{}
	key := serverproof.DeriveKey(serverproof.KindSession, token)
	proof := serverproof.Handler(func(_ context.Context, kind, hash string) ([]byte, bool) {
		return key, kind == serverproof.KindSession && hash == serverproof.TokenHash(token)
	}, nil, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.mu.Lock()
		defer seen.mu.Unlock()
		if r.URL.Path == serverproof.Path {
			seen.proofs++
			seen.last = r.RemoteAddr
			seen.mu.Unlock()
			proof.ServeHTTP(w, r)
			seen.mu.Lock()
			return
		}
		seen.tokens = append(seen.tokens, r.Header.Get(SessionTokenHeader))
		seen.conns = append(seen.conns, [2]string{seen.last, r.RemoteAddr})
		io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func agentClient(token string, base http.RoundTripper) *http.Client {
	return &http.Client{Transport: identityTransport{base: base, tool: "coral-board", token: token, hosts: configuredHosts(), proof: sessionProof(base, token)}}
}

func TestASessionTokenGoesOnlyAfterTheServerProvedItself(t *testing.T) {
	enableProof(t)
	srv, seen := provingServer(t, v2Token)
	c := agentClient(v2Token, &http.Transport{})
	for i := 0; i < 2; i++ {
		resp, err := c.Get(srv.URL + "/api/board/x/messages")
		require.NoError(t, err)
		io.Copy(io.Discard, resp.Body) // read to the end, as a client that reuses its connection does
		resp.Body.Close()
	}
	assert.Equal(t, []string{v2Token, v2Token}, seen.tokens)
	assert.Equal(t, 1, seen.proofs, "one proof for the connection, which both requests use")
	for _, c := range seen.conns {
		assert.Equal(t, c[0], c[1], "the token travelled on the connection that was proved")
	}
}

func TestAProgramThatCannotProveItselfNeverGetsTheSessionToken(t *testing.T) {
	enableProof(t)
	serve := func(h http.HandlerFunc) (string, *[]string) {
		var mu sync.Mutex
		var seen []string
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.URL.Path+" "+r.Header.Get(SessionTokenHeader))
			mu.Unlock()
			h(w, r)
		}))
		t.Cleanup(s.Close)
		return s.URL, &seen
	}
	for name, h := range map[string]http.HandlerFunc{
		"answers 404":    func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"answers 200 {}": func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "{}") },
		"answers nothing": func(w http.ResponseWriter, r *http.Request) {
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			c.Close()
		},
	} {
		t.Run(name, func(t *testing.T) {
			url, seen := serve(h)
			_, err := agentClient(v2Token, &http.Transport{}).Get(url + "/api/board/x/messages")
			var re *serverproof.RefusedError
			require.ErrorAs(t, err, &re)
			assert.True(t, re.TokenWithheld())
			for _, s := range *seen {
				assert.NotContains(t, s, v2Token, "the token was never sent")
			}
		})
	}
}

// the relay: a program that took 127.0.0.1:P forwards everything to the real Coral on [::1]:P, which answers the challenge it receives, with its own connection's endpoints.
func TestARelayToTheRealCoralNeverSeesTheSessionToken(t *testing.T) {
	enableProof(t)
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
	key := serverproof.DeriveKey(serverproof.KindSession, v2Token)
	realSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == serverproof.Path {
			serverproof.Handler(func(context.Context, string, string) ([]byte, bool) { return key, true }, nil, nil).ServeHTTP(w, r)
			return
		}
		io.WriteString(w, "ok")
	})}
	go realSrv.Serve(realLn)
	t.Cleanup(func() { realSrv.Close() })
	var mu sync.Mutex
	var seen strings.Builder
	imp := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen.WriteString(r.URL.RequestURI() + " " + r.Header.Get(SessionTokenHeader) + "\n")
		mu.Unlock()
		req, _ := http.NewRequest(r.Method, "http://"+realLn.Addr().String()+r.URL.RequestURI(), nil)
		req.Header = r.Header.Clone()
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})}
	go imp.Serve(impLn)
	t.Cleanup(func() { imp.Close() })

	_, err = agentClient(v2Token, &http.Transport{}).Get("http://" + impLn.Addr().String() + "/api/board/x/messages")
	var re *serverproof.RefusedError
	require.ErrorAs(t, err, &re)
	mu.Lock()
	defer mu.Unlock()
	assert.NotContains(t, seen.String(), v2Token, "the relay never saw the token")
}

// An agent started before proofs existed holds an unmarked token: its tools do not ask, and the token goes as it always did.
func TestAnOlderTokenIsSentWithoutAProof(t *testing.T) {
	enableProof(t)
	var asked bool
	var got string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == serverproof.Path {
			asked = true
		}
		got = r.Header.Get(SessionTokenHeader)
	}))
	t.Cleanup(s.Close)
	resp, err := agentClient("an-older-unmarked-token", &http.Transport{}).Get(s.URL + "/api/x")
	require.NoError(t, err)
	resp.Body.Close()
	assert.False(t, asked)
	assert.Equal(t, "an-older-unmarked-token", got)
}

func TestNoProofWithoutAHookableTransportOrOnWindows(t *testing.T) {
	enableProof(t)
	assert.Nil(t, sessionProof(&recorder{}, v2Token), "a base whose dialing cannot be hooked is not wrapped")
	assert.Nil(t, sessionProof(&http.Transport{}, ""), "no token: nothing to prove with")
	proofEnabled = false
	assert.Nil(t, sessionProof(&http.Transport{}, v2Token), "Windows asks the system who owns the port instead")
}
