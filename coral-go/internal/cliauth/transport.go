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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/subgentic/coral/internal/agentenv"
	"github.com/subgentic/coral/internal/auth"
	"github.com/subgentic/coral/internal/httputil"
	"github.com/subgentic/coral/internal/safefs"
	"github.com/subgentic/coral/internal/secfile"
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
var agentMarkers = []string{"CORAL_SESSION_NAME", "CORAL_SESSION_ID", "CORAL_SESSION_TOKEN", "CORAL_SUBSCRIBER_ID", agentenv.SessionAuthFileEnv}

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
	tok, err := readPrivateFile(dir, TokenFile)
	if err == nil && tok == "" {
		return "", errors.New("empty command-line token")
	}
	return tok, err
}

// ReadPrivateFile reads a secret from the file at path with ReadToken's checks: a regular file
// with one name (not a link), private to this user, at most 256 bytes. It holds an agent
// session's token for the tools of a Codex agent (agentenv.SessionAuthFileEnv, #13); "" with
// no error for an empty file.
func ReadPrivateFile(path string) (string, error) {
	return readPrivateFile(filepath.Dir(path), filepath.Base(path))
}

func readPrivateFile(dir, name string) (string, error) {
	p := filepath.Join(dir, name)
	// Opened once, then checked and read through the same handle (#3601, weak site W22): a
	// regular file, never through a symlink, never a FIFO; the mode, size and owner checks are
	// on what was opened, so nothing can be swapped in between the check and the read.
	root, err := safefs.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	f, fi, err := root.Open(name)
	root.Close()
	if err != nil {
		if errors.Is(err, safefs.ErrNotRegular) {
			return "", fmt.Errorf("%s is not a private regular file of this user", p)
		}
		return "", err
	}
	defer f.Close()
	// One name only: a hard link is another name for a file elsewhere; the count is read
	// from the handle, so on Windows too (#3633).
	if n, ok := safefs.LinkCount(f, fi); !ok || n != 1 {
		return "", fmt.Errorf("%s is not a private regular file of this user", p)
	}
	if !privateMode(fi) || fi.Size() > 256 || !ownedByMe(fi) {
		return "", fmt.Errorf("%s is not a private regular file of this user", p)
	}
	// Windows: the access list instead of the mode and owner (#3905); nothing elsewhere.
	if err := secfile.CheckOpen(f); err != nil {
		return "", fmt.Errorf("%s is not a private regular file of this user: %w", p, err)
	}
	b, err := io.ReadAll(io.LimitReader(f, 257))
	if err != nil {
		return "", err
	}
	if len(b) > 256 {
		return "", fmt.Errorf("%s is not a private regular file of this user", p)
	}
	return strings.TrimSpace(string(b)), nil
}

// Transport signs every request to a loopback Coral with the command-line token: one
// hello (the server proves it knows the token first) and one signed request. A loopback
// server that can't prove it gets nothing else: the request fails with ErrNotCoral.
// Requests to any other host go through Base unchanged.
type Transport struct {
	Base  http.RoundTripper
	Token string
	Dir   string // where the token was read (named in the error when a server can't prove it)
}

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.Token == "" || !auth.IsLoopbackHost(r.URL.Hostname()) || r.Header.Get(CLIAuthHeader) != "" {
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
	out := r.Clone(r.Context())
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	base := r.URL.Scheme + "://" + r.URL.Host
	cn, sn, err := t.hello(r.Context(), base)
	if errors.Is(err, ErrNotCoral) && t.Dir != "" {
		// The server may have a newer token than the one this process read at its start (it
		// restarted, or `coral reset-password` replaced it, #4041): read it again, once.
		if tok, rerr := ReadToken(t.Dir); rerr == nil && tok != t.Token {
			t.Token = tok
			cn, sn, err = t.hello(r.Context(), base)
		}
	}
	if errors.Is(err, ErrNotCoral) {
		// Never sent unsigned (#3421 v2, operator decision): the server rejects unsigned
		// command-line requests, and a program that can't prove it knows the token gets
		// nothing but the hello.
		dir := t.Dir
		if dir == "" {
			dir = "Coral's data folder"
		}
		return nil, fmt.Errorf("%s could not prove it is the Coral whose command-line token is in %s, so nothing was sent; point CORAL_URL (or --url) at that Coral, or CORAL_DATA_DIR at this server's data folder: %w", base, dir, ErrNotCoral)
	}
	if err != nil {
		return nil, err
	}
	sig, err := SignCLIRequest(t.Token, cn, sn, out.Method, out.URL.Path, out.URL.RawQuery, body, out.Header.Get("Authorization"))
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
	if resp.Header.Get(httputil.ActionLimitHeader) != "" {
		return "", "", ErrServerBusy
	}
	s, err1 := b64.DecodeString(h.Nonce)
	proof, err2 := b64.DecodeString(h.Proof)
	if resp.StatusCode != http.StatusOK || err1 != nil || err2 != nil || len(s) != cliNonceBytes ||
		!hmac.Equal(proof, cliMAC(t.Token, cliServerLabel, cn, s)) {
		return "", "", ErrNotCoral
	}
	return b64.EncodeToString(cn), h.Nonce, nil
}

// InstallOperatorSigning makes the default HTTP transport sign requests to a loopback
// Coral with the command-line token, for a person's own command-line tool. It does
// nothing inside an agent session, when the process already has an agent session token
// (CORAL_SESSION_TOKEN), or when the token can't be read (Coral not running as this
// user, or another data folder): the request then goes out unsigned, as before.
//
// When it can't sign, a 401 from this computer's Coral then prints once, on stderr, why the
// request went out without a credential (#3936: with strict local auth the default, a bare
// "401 session_token_required" told a person nothing).
func InstallOperatorSigning() bool {
	if InsideAgentSession() {
		if strings.TrimSpace(os.Getenv("CORAL_SESSION_TOKEN")) == "" && strings.TrimSpace(os.Getenv(agentenv.SessionAuthFileEnv)) == "" {
			installUnsignedHint("this agent session has no session token (it started before Coral gave agents one): restart the agent from Coral")
		}
		return false
	}
	dir := DataDir()
	tok, err := ReadToken(dir)
	if err != nil {
		installUnsignedHint(fmt.Sprintf("this command couldn't read Coral's command-line token in %s (%v), so it could not sign as you: run it as the user Coral runs as, with CORAL_DATA_DIR set if Coral uses another data folder", dir, err))
		return false
	}
	if _, done := http.DefaultTransport.(Transport); done {
		return true
	}
	http.DefaultTransport = Transport{Base: http.DefaultTransport, Token: tok, Dir: dir}
	return true
}

// unsignedHint explains, once, a 401 from this computer's Coral to a request that went out
// without a credential.
type unsignedHint struct {
	base http.RoundTripper
	hint string
	once *sync.Once
	out  io.Writer
}

func installUnsignedHint(hint string) {
	if _, done := http.DefaultTransport.(unsignedHint); done {
		return
	}
	http.DefaultTransport = unsignedHint{base: http.DefaultTransport, hint: hint, once: new(sync.Once), out: os.Stderr}
}

func (u unsignedHint) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := u.base.RoundTrip(r)
	if err == nil && resp.StatusCode == http.StatusUnauthorized && auth.IsLoopbackHost(r.URL.Hostname()) {
		u.once.Do(func() { fmt.Fprintf(u.out, "coral: Coral refused the request (401): %s\n", u.hint) })
	}
	return resp, err
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
