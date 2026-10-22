package serverproof

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/httputil"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	secret = "the-credential-only-this-users-tools-hold"
	other  = "another-credential-entirely"
)

// coral is a stand-in Coral: the proof route and an echo route that records the connection each request arrived on.
type coral struct {
	srv    *httptest.Server
	mu     sync.Mutex
	proofs []string // RemoteAddr of each proof request
	echoes []string // RemoteAddr of each credentialed request
	tokens []string
}

func newCoral(t *testing.T, key []byte) *coral {
	t.Helper()
	c := &coral{}
	mux := http.NewServeMux()
	lookup := func(_ context.Context, kind, hash string) ([]byte, bool) {
		if key == nil {
			return nil, false
		}
		if kind == KindSession && hash != TokenHash(secret) {
			return nil, false
		}
		return key, true
	}
	mux.Handle(Path, Handler(lookup, nil, nil))
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.echoes = append(c.echoes, r.RemoteAddr)
		c.tokens = append(c.tokens, r.Header.Get("X-Test-Credential"))
		c.mu.Unlock()
		io.WriteString(w, "ok")
	})
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == Path {
			c.mu.Lock()
			c.proofs = append(c.proofs, r.RemoteAddr)
			c.mu.Unlock()
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *coral) counts() (proofs, echoes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.proofs), len(c.echoes)
}

func client(t *testing.T, cred Credential) *http.Client {
	t.Helper()
	rt, ok := NewTransport(&http.Transport{}, cred)
	require.True(t, ok)
	t.Cleanup(func() { rt.(*http.Transport).CloseIdleConnections() })
	return &http.Client{Transport: rt, Timeout: 5 * time.Second}
}

func get(c *http.Client, url string) error {
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Test-Credential", secret)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

func isRefused(t *testing.T, err error) *RefusedError {
	t.Helper()
	var re *RefusedError
	require.True(t, errors.As(err, &re), "a refusal, not another error: %v", err)
	assert.True(t, re.TokenWithheld(), "marked as a withheld credential, so the tools exit 'refused' (5)")
	return re
}

func opCred() Credential  { return Credential{Kind: KindOperator, Secret: secret} }
func sesCred() Credential { return Credential{Kind: KindSession, Secret: secret} }

func TestTheProofIsOnTheConnectionTheCredentialUses(t *testing.T) {
	for name, cred := range map[string]Credential{"operator": opCred(), "session": sesCred()} {
		t.Run(name, func(t *testing.T) {
			c := newCoral(t, DeriveKey(cred.Kind, cred.Secret))
			cl := client(t, cred)
			require.NoError(t, get(cl, c.srv.URL+"/echo"))
			require.NoError(t, get(cl, c.srv.URL+"/echo"))
			proofs, echoes := c.counts()
			assert.Equal(t, 1, proofs, "one challenge for the connection, which http keeps")
			assert.Equal(t, 2, echoes)
			assert.Equal(t, c.proofs[0], c.echoes[0], "the credential travelled on the very connection that was proved")
			assert.Equal(t, c.proofs[0], c.echoes[1])
		})
	}
}

// The headline: a program that took the port (127.0.0.1:P) relays the challenge to the real Coral, which listens on another address of the same port ([::1]:P: the stand-in for "the real Coral on
// 0.0.0.0:P, the impostor on the more specific 127.0.0.1:P"), and hands the genuine answer back. The answer is bound to the relay's connection to the real Coral, not to the tool's connection to the
// relay: both ends see the same port, so only the connection's endpoints tell them apart.
func TestARelayThatForwardsTheChallengeToTheRealCoralOnAnotherAddressFails(t *testing.T) {
	// a pair of listeners on one port, on two address families: the real Coral on [::1]:P, the program that took the port on 127.0.0.1:P
	pair := func(t *testing.T) (realLn, impLn net.Listener) {
		realLn, err := net.Listen("tcp", "[::1]:0")
		if err != nil {
			t.Skip("no IPv6 loopback here")
		}
		_, port, _ := net.SplitHostPort(realLn.Addr().String())
		impLn, err = net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			realLn.Close()
			t.Skip("the port is not free on 127.0.0.1")
		}
		t.Cleanup(func() { realLn.Close(); impLn.Close() })
		return realLn, impLn
	}
	startReal := func(t *testing.T, ln net.Listener) *coral {
		real := &coral{}
		mux := http.NewServeMux()
		mux.Handle(Path, Handler(func(context.Context, string, string) ([]byte, bool) { return DeriveKey(KindOperator, secret), true }, nil, nil))
		mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
			real.mu.Lock()
			real.echoes = append(real.echoes, r.RemoteAddr)
			real.mu.Unlock()
			io.WriteString(w, "ok")
		})
		srv := &http.Server{Handler: mux}
		go srv.Serve(ln)
		t.Cleanup(func() { srv.Close() })
		return real
	}
	var mu sync.Mutex
	var seen bytes.Buffer
	note := func(b []byte) { mu.Lock(); seen.Write(b); mu.Unlock() }
	check := func(t *testing.T, real *coral, impURL string) {
		err := get(client(t, opCred()), impURL+"/echo")
		re := isRefused(t, err)
		assert.Contains(t, re.Error(), "did not prove it is your Coral")
		real.mu.Lock()
		assert.Empty(t, real.echoes, "the credentialed request never reached the real Coral through the relay")
		real.mu.Unlock()
		mu.Lock()
		defer mu.Unlock()
		assert.NotContains(t, seen.String(), secret, "the credential is never sent")
		assert.NotContains(t, seen.String(), "X-Test-Credential")
	}

	t.Run("a TCP relay", func(t *testing.T) {
		realLn, impLn := pair(t)
		realAddr := realLn.Addr().String()
		real := startReal(t, realLn)
		go func() {
			for {
				cc, err := impLn.Accept()
				if err != nil {
					return
				}
				go func() {
					defer cc.Close()
					up, err := net.Dial("tcp", realAddr)
					if err != nil {
						return
					}
					defer up.Close()
					go io.Copy(up, io.TeeReader(cc, writerFunc(note)))
					io.Copy(cc, up)
				}()
			}
		}()
		check(t, real, "http://"+impLn.Addr().String())
	})
	t.Run("an HTTP relay: the answer is fetched from the real Coral on a connection of the relay's own", func(t *testing.T) {
		realLn, impLn := pair(t)
		realAddr := realLn.Addr().String()
		real := startReal(t, realLn)
		imp := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			note([]byte(r.URL.String() + fmt.Sprint(r.Header)))
			resp, err := http.Get("http://" + realAddr + r.URL.RequestURI())
			if err != nil {
				return
			}
			defer resp.Body.Close()
			io.Copy(w, resp.Body)
		})}
		go imp.Serve(impLn)
		t.Cleanup(func() { imp.Close() })
		check(t, real, "http://"+impLn.Addr().String())
	})
}

// The same relay on a different port is a weaker case (the port differs on its own); it stays as the plain statement that a program that is not Coral gets nothing.
func TestARelayOnAnotherPortFails(t *testing.T) {
	real := newCoral(t, DeriveKey(KindOperator, secret))
	imp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := http.Get(real.srv.URL + r.URL.RequestURI())
		if err != nil {
			return
		}
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(imp.Close)
	isRefused(t, get(client(t, opCred()), imp.URL+"/echo"))
	_, echoes := real.counts()
	assert.Zero(t, echoes)
}

type writerFunc func([]byte)

func (f writerFunc) Write(b []byte) (int, error) { f(b); return len(b), nil }

func TestAnImpostorThatCannotProveGetsNothing(t *testing.T) {
	serve := func(h http.HandlerFunc) string {
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)
		return s.URL
	}
	var credentialed atomic.Int32
	counted := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != Path {
				credentialed.Add(1)
			}
			h(w, r)
		}
	}
	good := func(nonce string) string { return nonce }
	_ = good
	cases := map[string]struct {
		h    http.HandlerFunc
		want string
	}{
		"404 (an older Coral, or a program that answers everything with 404)": {func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, "cannot prove who it is"},
		"200 with no proof": {func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "{}") }, "did not prove"},
		"200 with garbage":  {func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>hello</html>") }, "did not prove"},
		"a wrong proof": {func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]string{"proof": strings.Repeat("ab", 32)})
		}, "did not prove"},
		"a proof made with another credential's key": {func(w http.ResponseWriter, r *http.Request) {
			c, _ := Endpoint(r.RemoteAddr)
			s, _ := Endpoint(r.Context().Value(http.LocalAddrContextKey).(net.Addr).String())
			_, port, _ := net.SplitHostPort(s)
			json.NewEncoder(w).Encode(map[string]string{"proof": Proof(DeriveKey(KindOperator, other), port, r.URL.Query().Get("n"), c, s)})
		}, "did not prove"},
		"a redirect": {func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) }, "did not prove"},
		"Coral's own limit answer for a flood": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set(httputil.ActionLimitHeader, "action")
			w.WriteHeader(429)
		}, "too many proof requests"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			credentialed.Store(0)
			cl := client(t, opCred())
			err := get(cl, serve(counted(tc.h))+"/echo")
			re := isRefused(t, err)
			assert.Contains(t, re.Error(), tc.want)
			assert.Zero(t, credentialed.Load(), "no credentialed request reached it")
		})
	}
}

func TestAnImpostorThatDoesNotAnswerOrSendsMoreIsRefused(t *testing.T) {
	prev := proofTimeout
	proofTimeout = 300 * time.Millisecond
	t.Cleanup(func() { proofTimeout = prev })

	t.Run("a slow answer", func(t *testing.T) {
		stop := make(chan struct{})
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-stop }))
		t.Cleanup(func() { close(stop); s.Close() })
		start := time.Now()
		err := get(client(t, opCred()), s.URL+"/echo")
		isRefused(t, err)
		assert.Less(t, time.Since(start), 2*time.Second, "refused at the proof's deadline, not left hanging")
	})
	// A program that has the key (it is the real Coral, or holds its credential) but answers in a way no Coral does: more on the connection than one answer, or an answer that closes it.
	valid := func(cc net.Conn, req *http.Request) string {
		c, _ := Endpoint(cc.RemoteAddr().String())
		sv, _ := Endpoint(cc.LocalAddr().String())
		_, port, _ := net.SplitHostPort(sv)
		return Proof(DeriveKey(KindOperator, secret), port, req.URL.Query().Get("n"), c, sv)
	}
	rawServer := func(t *testing.T, answer func(cc net.Conn, req *http.Request)) string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { ln.Close() })
		go func() {
			cc, err := ln.Accept()
			if err != nil {
				return
			}
			defer cc.Close()
			req, err := http.ReadRequest(bufio.NewReader(cc))
			if err != nil {
				return
			}
			answer(cc, req)
			time.Sleep(time.Second)
		}()
		return "http://" + ln.Addr().String()
	}
	t.Run("a valid proof with a second response queued on the connection", func(t *testing.T) {
		url := rawServer(t, func(cc net.Conn, req *http.Request) {
			body := `{"proof":"` + valid(cc, req) + `"}`
			fmt.Fprintf(cc, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%sHTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nxx", len(body), body)
		})
		isRefused(t, get(client(t, opCred()), url+"/echo"))
	})
	t.Run("a valid proof in an answer that closes the connection", func(t *testing.T) {
		url := rawServer(t, func(cc net.Conn, req *http.Request) {
			body := `{"proof":"` + valid(cc, req) + `"}`
			fmt.Fprintf(cc, "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
		})
		isRefused(t, get(client(t, opCred()), url+"/echo"))
	})
	t.Run("control: a valid proof alone passes", func(t *testing.T) {
		url := rawServer(t, func(cc net.Conn, req *http.Request) {
			body := `{"proof":"` + valid(cc, req) + `"}`
			fmt.Fprintf(cc, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
			// then the credentialed request: answer it
			r2, err := http.ReadRequest(bufio.NewReader(cc))
			if err == nil {
				_ = r2
				io.WriteString(cc, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
			}
		})
		// the raw server's second read uses a fresh bufio reader: a request already buffered by the first would be lost, so only the proof leg is asserted
		_ = url
		cc, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
		require.NoError(t, err)
		defer cc.Close()
		require.NoError(t, Prove(context.Background(), cc, opCred()))
	})
}

// An answer a program recorded earlier, or computed with the key but for another port or another connection, is not an answer for this challenge.
func TestAReplayedOrMisboundProofIsRefused(t *testing.T) {
	key := DeriveKey(KindOperator, secret)
	type bind func(port, nonce, client, server string) string
	cases := map[string]bind{
		"a recorded proof for another nonce": func(port, nonce, client, server string) string {
			return Proof(key, port, strings.Repeat("0", 32), client, server)
		},
		"a proof for another port":           func(port, nonce, client, server string) string { return Proof(key, "1", nonce, client, server) },
		"a proof for another client address": func(port, nonce, client, server string) string { return Proof(key, port, nonce, "127.0.0.1:1", server) },
		"a proof for another server address": func(port, nonce, client, server string) string {
			return Proof(key, port, nonce, client, "127.0.0.1:"+port+"0")
		},
		"a proof with the right key for nothing": func(port, nonce, client, server string) string { return Proof(key, "", "", "", "") },
	}
	for name, make := range cases {
		t.Run(name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, _ := Endpoint(r.RemoteAddr)
				sv, _ := Endpoint(r.Context().Value(http.LocalAddrContextKey).(net.Addr).String())
				_, port, _ := net.SplitHostPort(sv)
				json.NewEncoder(w).Encode(map[string]string{"proof": make(port, r.URL.Query().Get("n"), c, sv)})
			}))
			t.Cleanup(s.Close)
			isRefused(t, get(client(t, opCred()), s.URL+"/echo"))
		})
	}
	t.Run("control: the right proof for this connection passes", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != Path {
				io.WriteString(w, "ok")
				return
			}
			c, _ := Endpoint(r.RemoteAddr)
			sv, _ := Endpoint(r.Context().Value(http.LocalAddrContextKey).(net.Addr).String())
			_, port, _ := net.SplitHostPort(sv)
			json.NewEncoder(w).Encode(map[string]string{"proof": Proof(key, port, r.URL.Query().Get("n"), c, sv)})
		}))
		t.Cleanup(s.Close)
		require.NoError(t, get(client(t, opCred()), s.URL+"/echo"))
	})
}

func TestAListenerThatIsReplacedIsCheckedAgain(t *testing.T) {
	real := newCoral(t, DeriveKey(KindOperator, secret))
	cl := client(t, opCred())
	require.NoError(t, get(cl, real.srv.URL+"/echo"))
	real.srv.CloseClientConnections() // Coral stopped; a program takes its port and answers whatever is asked
	ln, err := net.Listen("tcp", strings.TrimPrefix(real.srv.URL, "http://"))
	if err != nil {
		real.srv.Close()
		ln, err = net.Listen("tcp", strings.TrimPrefix(real.srv.URL, "http://"))
	}
	require.NoError(t, err)
	imp := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "{}") })}
	go imp.Serve(ln)
	t.Cleanup(func() { imp.Close() })
	time.Sleep(50 * time.Millisecond)
	isRefused(t, get(cl, real.srv.URL+"/echo"))
}

func TestTheSessionProofNeedsTheSessionAndItsHash(t *testing.T) {
	c := newCoral(t, DeriveKey(KindSession, secret))
	require.NoError(t, get(client(t, sesCred()), c.srv.URL+"/echo"))
	// a tool holding another session's token: Coral has no key for its hash
	wrong := Credential{Kind: KindSession, Secret: other}
	re := isRefused(t, get(client(t, wrong), c.srv.URL+"/echo"))
	assert.Contains(t, re.Error(), "restart the agent")
	assert.NotContains(t, re.Error(), other)
}

func TestHandlerAnswersOnlyAWellFormedChallenge(t *testing.T) {
	key := DeriveKey(KindOperator, secret)
	srv := httptest.NewServer(Handler(func(_ context.Context, kind, hash string) ([]byte, bool) { return key, kind == KindOperator }, nil, nil))
	t.Cleanup(srv.Close)
	nonce := strings.Repeat("ab", 16)
	status := func(method, query string) int {
		req, _ := http.NewRequest(method, srv.URL+Path+query, nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, 200, status("GET", "?k=operator&n="+nonce))
	assert.Equal(t, 405, status("POST", "?k=operator&n="+nonce))
	for _, q := range []string{"", "?k=operator", "?n=" + nonce, "?k=operator&n=short", "?k=operator&n=" + strings.ToUpper(nonce), "?k=operator&n=" + nonce + "&x=1",
		"?k=operator&n=" + nonce + "&h=" + strings.Repeat("a", 64), "?k=session&n=" + nonce, "?k=session&n=" + nonce + "&h=nothex", "?k=root&n=" + nonce, "?k=operator&n=" + nonce + "&n=" + nonce} {
		assert.Equal(t, 400, status("GET", q), q)
	}
	assert.Equal(t, 404, status("GET", "?k=session&n="+nonce+"&h="+strings.Repeat("a", 64)), "a session Coral has no key for")
}

func TestHandlerIsRateBounded(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(Handler(func(context.Context, string, string) ([]byte, bool) { return []byte("k"), true }, func() bool { return n.Add(1) <= 2 },
		func(w http.ResponseWriter) { w.Header().Set(httputil.ActionLimitHeader, "action"); w.WriteHeader(429) }))
	t.Cleanup(srv.Close)
	q := "?k=operator&n=" + strings.Repeat("cd", 16)
	var codes []int
	for i := 0; i < 4; i++ {
		resp, err := http.Get(srv.URL + Path + q)
		require.NoError(t, err)
		resp.Body.Close()
		codes = append(codes, resp.StatusCode)
	}
	assert.Equal(t, []int{200, 200, 429, 429}, codes)
}

func TestEndpointsAreWrittenTheSameFromBothEnds(t *testing.T) {
	for in, want := range map[string]string{"127.0.0.1:8420": "127.0.0.1:8420", "[::ffff:127.0.0.1]:8420": "127.0.0.1:8420", "[::1]:8420": "[::1]:8420", "[fe80::1]:9": "[fe80::1]:9"} {
		got, err := Endpoint(in)
		require.NoError(t, err)
		assert.Equal(t, want, got, in)
	}
	_, err := Endpoint("not an address")
	assert.Error(t, err)
}

func TestDerivedKeysDoNotGiveTheCredentialBackOrCrossKinds(t *testing.T) {
	assert.NotEqual(t, DeriveKey(KindOperator, secret), DeriveKey(KindSession, secret), "an operator key is not a session key")
	assert.NotContains(t, string(DeriveKey(KindSession, secret)), secret)
	assert.NotEqual(t, DeriveKey(KindSession, secret), DeriveKey(KindSession, other))
	a := Proof([]byte("k"), "1", "2", "3", "4")
	assert.NotEqual(t, a, Proof([]byte("k"), "12", "", "3", "4"), "fields are length-prefixed: shifting a boundary changes the proof")
}

func TestNewTransportNeedsAnHTTPTransport(t *testing.T) {
	_, ok := NewTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil }), opCred())
	assert.False(t, ok, "a transport whose dialing cannot be hooked is not wrapped: the caller decides")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
