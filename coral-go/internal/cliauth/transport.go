package cliauth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Strict local auth (#3418): with --strict-local-auth, a request from this computer needs
// a credential. A person's own command-line tools (coral-board or coral-agent in their
// terminal, coral auto-accept, the tray) have no agent session token; they prove they run
// as Coral's user by signing each request with the 0600 command-line token, exactly like
// `coral cloud connect`, and act as the local Operator. Never inside an agent session: an
// agent whose token is missing is refused, not promoted (see InsideAgentSession).

// TokenFile is the command-line token's name in Coral's data folder (written at startup).
const TokenFile = "cli-token"

// maxSignedBody bounds a body the transport buffers to sign (an artifact upload is 25 MiB).
const maxSignedBody = 32 << 20

// agentMarkers are set by Coral for an agent session (its launch line and environment).
var agentMarkers = []string{"CORAL_SESSION_NAME", "CORAL_SESSION_ID", "CORAL_SESSION_TOKEN", "CORAL_SUBSCRIBER_ID"}

// InsideAgentSession reports whether this process runs inside a Coral agent session: any
// of the variables Coral sets for one is present. Such a process never signs with the
// command-line token, even if it could read it: its identity is its session token, and
// without one it is refused, never promoted to the Operator.
func InsideAgentSession() bool {
	for _, k := range agentMarkers {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			return true
		}
	}
	return false
}

// DataDir is Coral's data folder as the server resolves it without flags:
// CORAL_DATA_DIR, then CORAL_DIR, then ~/.coral.
func DataDir() string {
	for _, k := range []string{"CORAL_DATA_DIR", "CORAL_DIR"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".coral")
}

// ReadToken reads the command-line token from dir: a regular file (not a link), not
// readable by group or others, owned by this user where the system says, at most 256 bytes.
func ReadToken(dir string) (string, error) {
	p := filepath.Join(dir, TokenFile)
	fi, err := os.Lstat(p)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 || fi.Size() > 256 || !ownedByMe(fi) {
		return "", fmt.Errorf("%s is not a private regular file of this user", p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", errors.New("empty command-line token")
	}
	return tok, nil
}

// Transport signs every request to a loopback Coral with the command-line token: one
// hello (the server proves it knows the token first) and one signed request. Requests to
// any other host go through Base unchanged.
type Transport struct {
	Base  http.RoundTripper
	Token string
}

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.Token == "" || !loopbackHost(r.URL.Hostname()) || r.Header.Get(CLIAuthHeader) != "" {
		return t.Base.RoundTrip(r)
	}
	var body []byte
	if r.Body != nil && r.Body != http.NoBody {
		b, err := io.ReadAll(io.LimitReader(r.Body, maxSignedBody+1))
		r.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(b) > maxSignedBody {
			return nil, errors.New("request body too large to sign")
		}
		body = b
	}
	cn, sn, err := t.hello(r.Context(), r.URL.Scheme+"://"+r.URL.Host)
	if err != nil {
		return nil, err
	}
	out := r.Clone(r.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	sig, err := SignCLIRequest(t.Token, cn, sn, out.Method, out.URL.Path, body, out.Header.Get("Authorization"))
	if err != nil {
		return nil, ErrNotCoral
	}
	out.Header.Set(CLIAuthHeader, sig)
	return t.Base.RoundTrip(out)
}

// hello is step 1: it returns the client and server nonces once the server has proved it
// knows the token (otherwise ErrNotCoral, and nothing else was sent).
func (t Transport) hello(ctx context.Context, base string) (clientNonce, serverNonce string, err error) {
	cn := make([]byte, cliNonceBytes)
	if _, err := rand.Read(cn); err != nil {
		return "", "", err
	}
	hb, _ := json.Marshal(map[string]string{"nonce": b64.EncodeToString(cn)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+CLIHelloPath, bytes.NewReader(hb))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.Base.RoundTrip(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var h struct {
		Nonce string `json:"nonce"`
		Proof string `json:"proof"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&h)
	s, err1 := b64.DecodeString(h.Nonce)
	proof, err2 := b64.DecodeString(h.Proof)
	if resp.StatusCode != http.StatusOK || err1 != nil || err2 != nil || len(s) != cliNonceBytes ||
		!hmac.Equal(proof, cliMAC(t.Token, cliServerLabel, cn, s)) {
		return "", "", ErrNotCoral
	}
	return b64.EncodeToString(cn), h.Nonce, nil
}

func loopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// InstallOperatorSigning makes the default HTTP transport sign requests to a loopback
// Coral with the command-line token, for a person's own command-line tool. It does
// nothing inside an agent session, when the process already has an agent session token
// (CORAL_SESSION_TOKEN), or when the token can't be read (Coral not running as this
// user, or another data folder): the request then goes out unsigned, as before.
func InstallOperatorSigning() bool {
	if InsideAgentSession() {
		return false
	}
	tok, err := ReadToken(DataDir())
	if err != nil {
		return false
	}
	if _, done := http.DefaultTransport.(Transport); done {
		return true
	}
	http.DefaultTransport = Transport{Base: http.DefaultTransport, Token: tok}
	return true
}

type verifiedKey struct{}

// WithVerified marks a request whose signature the server has checked (and whose nonce is
// therefore used up), so a handler further on accepts it without checking it again.
func WithVerified(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), verifiedKey{}, true))
}

// Verified reports whether WithVerified marked the request.
func Verified(r *http.Request) bool {
	v, _ := r.Context().Value(verifiedKey{}).(bool)
	return v
}
