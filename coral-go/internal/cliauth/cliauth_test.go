package cliauth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nonce(t *testing.T) []byte {
	b := make([]byte, cliNonceBytes)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

// signed builds the request the command line sends after a hello.
func signed(t *testing.T, a *CLIAuth, token, method, path string, body []byte) *http.Request {
	t.Helper()
	cn := nonce(t)
	sn, proof, err := a.Hello(b64.EncodeToString(cn))
	require.NoError(t, err)
	s, _ := b64.DecodeString(sn)
	p, _ := b64.DecodeString(proof)
	require.Equal(t, cliMAC(a.token, cliServerLabel, cn, s), p, "the server proves its token")
	r := httptest.NewRequest(method, path, strings.NewReader(string(body)))
	r.Header.Set(CLIAuthHeader, sn+"."+b64.EncodeToString(clientMAC(token, cn, s, method, r.URL.Path, r.URL.RawQuery, body, r.Header.Get("Authorization"))))
	return r
}

// signedFrom signs req (with its current Authorization header) after a hello.
func signedFrom(t *testing.T, a *CLIAuth, token string, req *http.Request, body []byte) *http.Request {
	t.Helper()
	cn := nonce(t)
	sn, _, err := a.Hello(b64.EncodeToString(cn))
	require.NoError(t, err)
	s, _ := b64.DecodeString(sn)
	req.Header.Set(CLIAuthHeader, sn+"."+b64.EncodeToString(clientMAC(token, cn, s, req.Method, req.URL.Path, req.URL.RawQuery, body, req.Header.Get("Authorization"))))
	return req
}

// A request is admitted once, only as signed (method, path, body), only with the
// token, and only soon after its hello.
func TestCLIAuthVerify(t *testing.T) {
	a := NewCLIAuth("the-token")
	body := []byte(`{"check":true}`)
	r := signed(t, a, "the-token", "POST", "/api/cloud/redeem", body)
	assert.True(t, a.Verify(r, body))
	assert.False(t, a.Verify(r, body), "each nonce once")

	r = signed(t, a, "the-token", "POST", "/api/cloud/redeem", body)
	assert.False(t, a.Verify(r, []byte(`{"check":false}`)), "another body")
	r = signed(t, a, "the-token", "POST", "/api/cloud/redeem", body)
	r.URL.Path = "/api/cloud/disconnect"
	assert.False(t, a.Verify(r, body), "another path")
	r = signed(t, a, "the-token", "POST", "/api/cloud/redeem", body)
	r.Method = "PUT"
	assert.False(t, a.Verify(r, body), "another method")
	r = signed(t, a, "a-guess", "POST", "/api/cloud/redeem", body)
	assert.False(t, a.Verify(r, body), "another token")

	// The Authorization header (a redeem's credential) is signed: swapped, added or
	// removed after signing, the request is refused.
	for _, swap := range []struct{ signedWith, sent string }{
		{"Bearer chb_original", "Bearer chb_swapped"},
		{"", "Bearer chb_added"},
		{"Bearer chb_original", ""},
	} {
		req := httptest.NewRequest("POST", "/api/cloud/redeem", strings.NewReader(string(body)))
		if swap.signedWith != "" {
			req.Header.Set("Authorization", swap.signedWith)
		}
		signedReq := signedFrom(t, a, "the-token", req, body)
		if swap.sent == "" {
			signedReq.Header.Del("Authorization")
		} else {
			signedReq.Header.Set("Authorization", swap.sent)
		}
		assert.False(t, a.Verify(signedReq, body), "%+v", swap)
	}
	req := httptest.NewRequest("POST", "/api/cloud/redeem", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer chb_original")
	assert.True(t, a.Verify(signedFrom(t, a, "the-token", req, body), body), "unchanged: admitted")

	for _, h := range []string{"", "x", "x.y", "..", "AAAA.AAAA"} {
		r = httptest.NewRequest("POST", "/api/cloud/redeem", nil)
		r.Header.Set(CLIAuthHeader, h)
		assert.False(t, a.Verify(r, nil), h)
	}

	now := time.Now()
	a.now = func() time.Time { return now }
	r = signed(t, a, "the-token", "GET", "/api/cloud/status", nil)
	now = now.Add(cliHelloTTL + time.Second)
	assert.False(t, a.Verify(r, nil), "too long after the hello")
}

// A proof the server gives (anyone on loopback can ask for one) is never a
// valid request signature: the labels differ.
func TestCLIAuthServerProofIsNotAClientMAC(t *testing.T) {
	a := NewCLIAuth("the-token")
	cn := nonce(t)
	sn, proof, err := a.Hello(b64.EncodeToString(cn))
	require.NoError(t, err)
	r := httptest.NewRequest("GET", "/api/cloud/status", nil)
	r.Header.Set(CLIAuthHeader, sn+"."+proof)
	assert.False(t, a.Verify(r, nil))
}

func TestCLIAuthHelloRefusals(t *testing.T) {
	a := NewCLIAuth("the-token")
	for _, n := range []string{"", "short", b64.EncodeToString(make([]byte, 16)), "!!!"} {
		_, _, err := a.Hello(n)
		assert.Error(t, err, n)
	}
	_, _, err := NewCLIAuth("").Hello(b64.EncodeToString(nonce(t)))
	assert.Error(t, err, "no token: no command line")
	r := httptest.NewRequest("GET", "/api/cloud/status", nil)
	r.Header.Set(CLIAuthHeader, "a.b")
	assert.False(t, NewCLIAuth("").Verify(r, nil))

	// A flood never locks the real command line out: at the cap the oldest
	// pending hello is dropped, and the table stays bounded. The clock is frozen, as a
	// coarse one (Windows) is between hellos: the oldest is still the first to arrive (#3633).
	frozen := time.Now()
	a.now = func() time.Time { return frozen }
	first := signed(t, a, "the-token", "GET", "/api/cloud/status", nil)
	for i := 0; i < cliMaxPending+44; i++ {
		_, _, err := a.Hello(b64.EncodeToString(nonce(t)))
		require.NoError(t, err)
	}
	a.mu.Lock()
	assert.LessOrEqual(t, len(a.pending), cliMaxPending, "bounded")
	firstSN, _, _ := strings.Cut(first.Header.Get(CLIAuthHeader), ".")
	_, kept := a.pending[firstSN]
	a.mu.Unlock()
	assert.False(t, kept, "the oldest was dropped")
	assert.False(t, a.Verify(first, nil), "the oldest was dropped")
	fresh := signed(t, a, "the-token", "GET", "/api/cloud/status", nil)
	assert.True(t, a.Verify(fresh, nil), "a new hello after the flood works")
}

// The client against a server that runs CLIAuth, and against one that does not.
func TestCLIClient(t *testing.T) {
	a := NewCLIAuth("the-token")
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path == CLIHelloPath {
			var h struct{ Nonce string }
			_ = json.Unmarshal(b, &h)
			sn, proof, _ := a.Hello(h.Nonce)
			_ = json.NewEncoder(w).Encode(map[string]string{"nonce": sn, "proof": proof})
			return
		}
		if !a.Verify(r, b) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		got = append(got, r.Method+" "+r.URL.Path+" "+string(b))
	}))
	defer srv.Close()

	c := CLIClient{Base: srv.URL, Token: "the-token"}
	resp, err := c.Do(context.Background(), "PUT", "/api/cloud/max-mode", []byte(`{"mode":"view"}`), nil, 5*time.Second)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{`PUT /api/cloud/max-mode {"mode":"view"}`}, got)

	// 300 hellos (a local flood) first: the real command line still gets through.
	for i := 0; i < cliMaxPending+44; i++ {
		_, _, err := a.Hello(b64.EncodeToString(nonce(t)))
		require.NoError(t, err)
	}
	resp, err = c.Do(context.Background(), "GET", "/api/cloud/status", nil, nil, 5*time.Second)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode, "a connect after the flood")
	got = got[:1]

	_, err = CLIClient{Base: srv.URL, Token: "another"}.Do(context.Background(), "GET", "/api/cloud/status", nil, nil, 5*time.Second)
	assert.ErrorIs(t, err, ErrNotCoral, "a server that cannot prove this token")
	assert.Len(t, got, 1, "and nothing was sent to it")
}

// #4041 (Security's #4039 finding 2): the MAC covers the raw query, so a signed request's
// query (a confirm flag) can't be changed or added, and the same query still verifies.
func TestCLIAuthSignsTheQuery(t *testing.T) {
	a := NewCLIAuth("the-token")
	r := signed(t, a, "the-token", "POST", "/api/system/thing?confirm=false", nil)
	assert.True(t, a.Verify(r, nil), "as signed")
	for _, q := range []string{"confirm=true", "", "confirm=false&confirm=true"} {
		r := signed(t, a, "the-token", "POST", "/api/system/thing?confirm=false", nil)
		r.URL.RawQuery = q
		assert.False(t, a.Verify(r, nil), "query changed to %q", q)
	}
	r = signed(t, a, "the-token", "POST", "/api/system/thing", nil)
	r.URL.RawQuery = "confirm=true"
	assert.False(t, a.Verify(r, nil), "a query added to an unsigned-query request")
	sig, err := SignCLIRequest("the-token", b64.EncodeToString(nonce(t)), b64.EncodeToString(nonce(t)), "GET", "/a", "b=c", nil, "")
	require.NoError(t, err)
	assert.NotEmpty(t, sig)
}

// #4041 (Security's #4039 finding 3): a caller flooding hellos (1000 a second for a whole
// hello lifetime, as the one local caller) is limited, never fills the pending
// table, and so never evicts another command line's pending nonce.
func TestCLIAuthHelloFloodKeepsPendingNonces(t *testing.T) {
	a := NewCLIAuth("the-token")
	clock := time.Now()
	a.now = func() time.Time { return clock }
	// The real command line says hello first, from another caller.
	cn := nonce(t)
	sn, _, err := a.HelloFrom("addr:10.0.0.2", b64.EncodeToString(cn))
	require.NoError(t, err)
	s, _ := b64.DecodeString(sn)
	refused := 0
	for i := 0; i < 1000*29; i++ {
		clock = clock.Add(time.Second / 1000)
		if _, _, err := a.HelloFrom("local", b64.EncodeToString(nonce(t))); err != nil {
			require.ErrorIs(t, err, ErrHelloLimit)
			refused++
		}
	}
	a.mu.Lock()
	pending := len(a.pending)
	a.mu.Unlock()
	assert.Less(t, pending, cliMaxPending, "the flood never fills the table")
	assert.Greater(t, refused, 1000*29-cliHelloBurst-cliHelloRate*30, "the flood is limited")
	r := httptest.NewRequest("GET", "/api/cloud/status", nil)
	r.Header.Set(CLIAuthHeader, sn+"."+b64.EncodeToString(clientMAC("the-token", cn, s, "GET", "/api/cloud/status", "", nil, "")))
	assert.True(t, a.Verify(r, nil), "the real command line's nonce survived the flood")
	// A limited caller gets hellos again once its bucket refills; an unlimited caller ("") always does.
	clock = clock.Add(time.Second)
	_, _, err = a.HelloFrom("local", b64.EncodeToString(nonce(t)))
	assert.NoError(t, err)
	_, _, err = a.Hello(b64.EncodeToString(nonce(t)))
	assert.NoError(t, err)
}

// A hello refused for its rate (the marked 429) is reported as the server being busy, not as
// another program on the port.
func TestCLIClientHelloLimitIsNotNotCoral(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Coral-Limit", "action")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	_, err := CLIClient{Base: srv.URL, Token: "the-token"}.Do(context.Background(), "GET", "/api/cloud/status", nil, nil, 5*time.Second)
	assert.ErrorIs(t, err, ErrServerBusy)
	assert.NotErrorIs(t, err, ErrNotCoral)
	_, _, err = Transport{Token: "the-token", Base: http.DefaultTransport}.hello(context.Background(), srv.URL)
	assert.ErrorIs(t, err, ErrServerBusy)
}
