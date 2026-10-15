package cliauth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The command line and the running server authenticate each other with the
// 0600 token from Coral's data folder, without either ever sending it (#3149
// review: whatever answers on the Coral port, a "squatter", used to receive the
// token and then the one-time credential).
//
//  1. POST /api/cloud/cli-hello {"nonce": c}: the server answers {"nonce": s,
//     "proof": HMAC(token, "server" c s)}. The command checks the proof before
//     it sends or reads anything else: a program that is not this Coral cannot
//     produce it, so it learns nothing and the credential file stays unread.
//  2. Each request carries X-Coral-CLI-Auth: s "." HMAC(token, "client" c s
//     method path sha256(body) Authorization). The server accepts each s once,
//     for the one request it was issued for, within a short time. The
//     Authorization header (the one-time credential of a redeem) is signed too,
//     so it cannot be swapped on a signed request.
//
// Distinct labels keep a server proof from ever being usable as a client MAC.
const (
	CLIAuthHeader = "X-Coral-CLI-Auth"
	CLIHelloPath  = "/api/cloud/cli-hello"

	cliNonceBytes  = 32
	cliHelloTTL    = 30 * time.Second
	cliMaxPending  = 256
	cliServerLabel = "coral-cli-server\x00"
	cliClientLabel = "coral-cli-client\x00"
)

// ErrNotCoral means the program answering at the URL could not prove it is the
// Coral that wrote the token: nothing was sent to it.
var ErrNotCoral = errors.New("the program answering there is not this Coral (it could not prove it knows Coral's command-line token)")

func cliMAC(token, label string, parts ...[]byte) []byte {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte(label))
	for _, p := range parts {
		// Length-prefixed, so no two different part lists hash the same.
		fmt.Fprintf(m, "%d:", len(p))
		m.Write(p)
	}
	return m.Sum(nil)
}

func clientMAC(token string, c, s []byte, method, path string, body []byte, authorization string) []byte {
	sum := sha256.Sum256(body)
	return cliMAC(token, cliClientLabel, c, s, []byte(method), []byte(path), sum[:], []byte(authorization))
}

var b64 = base64.RawURLEncoding

// SignCLIRequest is the X-Coral-CLI-Auth value for a request, from the
// command line's nonce and the hello's nonce (both base64url), its body and its
// Authorization header ("" when it has none).
func SignCLIRequest(token, clientNonce, serverNonce, method, path string, body []byte, authorization string) (string, error) {
	c, err1 := b64.DecodeString(clientNonce)
	s, err2 := b64.DecodeString(serverNonce)
	if err1 != nil || err2 != nil {
		return "", errors.New("bad nonce")
	}
	return serverNonce + "." + b64.EncodeToString(clientMAC(token, c, s, method, path, body, authorization)), nil
}

// NewCLINonce returns a fresh nonce for a hello (base64url).
func NewCLINonce() (string, error) {
	b := make([]byte, cliNonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b64.EncodeToString(b), nil
}

// CLIAuth is the server side. A zero token (it could not be written) admits nobody.
type CLIAuth struct {
	token   string
	now     func() time.Time
	mu      sync.Mutex
	pending map[string]cliHello // by server nonce
	seq     uint64              // hellos so far: the order they came in
}

type cliHello struct {
	client  []byte
	expires time.Time
	seq     uint64
}

// NewCLIAuth returns the server side for token.
func NewCLIAuth(token string) *CLIAuth {
	return &CLIAuth{token: token, now: time.Now, pending: map[string]cliHello{}}
}

// Hello answers step 1.
func (a *CLIAuth) Hello(clientNonce string) (serverNonce, proof string, err error) {
	c, err := b64.DecodeString(clientNonce)
	if err != nil || len(c) != cliNonceBytes {
		return "", "", errors.New("nonce must be 32 random bytes, base64url")
	}
	if a.token == "" {
		return "", "", errors.New("the command-line token could not be written; the command line is off")
	}
	s := make([]byte, cliNonceBytes)
	if _, err := rand.Read(s); err != nil {
		return "", "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for k, h := range a.pending {
		if now.After(h.expires) {
			delete(a.pending, k)
		}
	}
	if len(a.pending) >= cliMaxPending {
		// Full (a flood of hellos): drop the oldest instead of refusing, so a burst
		// cannot lock the real command line out; whoever loses its slot just gets
		// a refusal and says hello again (#3285 security review). The oldest by arrival:
		// a coarse clock (Windows) gives many hellos the same time, and a tie must not
		// leave the choice to map order (#3633).
		oldest, first := "", uint64(0)
		for k, h := range a.pending {
			if oldest == "" || h.seq < first {
				oldest, first = k, h.seq
			}
		}
		delete(a.pending, oldest)
	}
	sn := b64.EncodeToString(s)
	a.seq++
	a.pending[sn] = cliHello{client: c, expires: now.Add(cliHelloTTL), seq: a.seq}
	return sn, b64.EncodeToString(cliMAC(a.token, cliServerLabel, c, s)), nil
}

// Verify checks step 2 for a request whose body is body, and uses up its nonce.
func (a *CLIAuth) Verify(r *http.Request, body []byte) bool {
	h := r.Header.Get(CLIAuthHeader)
	sn, macText, ok := strings.Cut(h, ".")
	if !ok || a.token == "" {
		return false
	}
	mac, err := b64.DecodeString(macText)
	if err != nil {
		return false
	}
	s, err := b64.DecodeString(sn)
	if err != nil {
		return false
	}
	a.mu.Lock()
	hello, found := a.pending[sn]
	delete(a.pending, sn) // once, right or wrong
	a.mu.Unlock()
	if !found || a.now().After(hello.expires) {
		return false
	}
	return hmac.Equal(mac, clientMAC(a.token, hello.client, s, r.Method, r.URL.Path, body, r.Header.Get("Authorization")))
}

// CLIClient is the command-line side: it talks only to a Coral that proves it
// knows token.
type CLIClient struct {
	Base  string // http://localhost:8420
	Token string
}

// Do sends one authenticated request. ErrNotCoral when the answer to the hello
// is not this Coral's (nothing else was sent); another error when nothing
// answered.
func (c CLIClient) Do(ctx context.Context, method, path string, body []byte, header http.Header, timeout time.Duration) (*http.Response, error) {
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cn := make([]byte, cliNonceBytes)
	if _, err := rand.Read(cn); err != nil {
		return nil, err
	}
	hb, _ := json.Marshal(map[string]string{"nonce": b64.EncodeToString(cn)})
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+CLIHelloPath, bytes.NewReader(hb))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	// The same client sends both requests, so the second one normally reuses the
	// connection the proof came back on.
	hresp, err := client.Do(hreq)
	if err != nil {
		return nil, err
	}
	var hello struct {
		Nonce string `json:"nonce"`
		Proof string `json:"proof"`
	}
	_ = json.NewDecoder(io.LimitReader(hresp.Body, 4096)).Decode(&hello)
	hresp.Body.Close()
	s, err1 := b64.DecodeString(hello.Nonce)
	proof, err2 := b64.DecodeString(hello.Proof)
	if hresp.StatusCode != http.StatusOK || err1 != nil || err2 != nil || len(s) != cliNonceBytes ||
		!hmac.Equal(proof, cliMAC(c.Token, cliServerLabel, cn, s)) {
		return nil, ErrNotCoral
	}

	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if len(body) > 0 && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	sig, err := SignCLIRequest(c.Token, b64.EncodeToString(cn), hello.Nonce, method, req.URL.Path, body, req.Header.Get("Authorization"))
	if err != nil {
		return nil, ErrNotCoral
	}
	req.Header.Set(CLIAuthHeader, sig)
	return client.Do(req)
}
