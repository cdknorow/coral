package serverproof

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/httputil"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Credential is what a tool is about to send to a Coral on this computer: the proof key is derived from it.
type Credential struct {
	Kind   string // KindOperator or KindSession
	Secret string // the command-line token, or the agent's session token
}

func (c Credential) noun() string {
	if c.Kind == KindOperator {
		return "the command-line token"
	}
	return "the agent's token"
}

// RefusedError: the credential was not sent, because the program on the port did not prove it is this user's Coral (#6103). The tools exit "refused" (5) for it, never "cannot reach" (3), so a script
// does not take a foreign program for a Coral that is not running (see clierr).
type RefusedError struct {
	Port string
	Why  string // whole sentences: what happened and what to do
	Noun string // what was withheld
	Err  error
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("Refused: %s was not sent to port %s on this computer. %s", e.Noun, e.Port, e.Why)
}
func (e *RefusedError) Unwrap() error       { return e.Err }
func (e *RefusedError) TokenWithheld() bool { return true }

// proofTimeout bounds one challenge: a program that does not answer in time is not Coral.
var proofTimeout = 2 * time.Second

// NewTransport is base with every new connection proved before anything else is sent on it. The challenge travels on the connection the credential then uses, which http keeps and reuses, so there
// is no gap between the check and the send, and a replaced listener means a new connection and a new check. base must be an *http.Transport (it is, for the default transport); ok is false otherwise.
func NewTransport(base http.RoundTripper, c Credential) (http.RoundTripper, bool) {
	t, isHTTP := base.(*http.Transport)
	if !isHTTP {
		return nil, false
	}
	clone := t.Clone()
	dial := t.DialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	clone.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if err := Prove(ctx, conn, c); err != nil {
			conn.Close()
			return nil, err
		}
		return conn, nil
	}
	return clone, true
}

// Prove runs the challenge on conn, which must be a fresh connection to Coral: nothing has been sent on it. It returns nil only for a proof computed with this credential's key over this connection.
func Prove(ctx context.Context, conn net.Conn, c Credential) error {
	port := "?"
	fail := func(why string, err error) error {
		return &RefusedError{Port: port, Why: why, Noun: c.noun(), Err: err}
	}
	serverEP, err1 := Endpoint(conn.RemoteAddr().String())
	clientEP, err2 := Endpoint(conn.LocalAddr().String())
	if err1 != nil || err2 != nil {
		return fail("Coral could not check who answers there (the connection's address could not be read). Start Coral, or stop the program that holds its port.", errors.Join(err1, err2))
	}
	_, port, _ = net.SplitHostPort(serverEP)
	nb := make([]byte, NonceHexLen/2)
	if _, err := rand.Read(nb); err != nil {
		return err
	}
	nonce := hex.EncodeToString(nb)
	q := url.Values{"n": {nonce}, "k": {c.Kind}}
	if c.Kind == KindSession {
		q.Set("h", TokenHash(c.Secret))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+serverEP+Path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "coral-proof")
	deadline := time.Now().Add(proofTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	defer conn.SetDeadline(time.Time{})
	if err := req.Write(conn); err != nil {
		return fail("The program on that port did not answer. Start Coral, or stop the program that holds its port.", err)
	}
	br := bufio.NewReaderSize(conn, 2048)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return fail("The program on that port did not answer Coral's proof in time, or answered something else. Start Coral, or stop the program that holds its port.", err)
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1024))
	resp.Body.Close()
	switch {
	case resp.Header.Get(httputil.ActionLimitHeader) != "": // Coral's own answer to a flood of challenges
		return fail("Coral is answering too many proof requests from this computer right now. Try again in a few seconds.", nil)
	case resp.StatusCode == http.StatusNotFound:
		return fail("The program on that port cannot prove who it is: it may be a Coral older than this tool, or one that does not know this "+c.scope()+". Restart Coral"+c.restartHint()+", or stop the program that holds the port.", nil)
	case resp.StatusCode != http.StatusOK || rerr != nil || resp.Close || br.Buffered() != 0:
		return fail("The program on that port did not prove it is your Coral. Start Coral, or stop the program that holds its port.", rerr)
	}
	var got struct {
		Proof string `json:"proof"`
	}
	if err := json.Unmarshal(body, &got); err != nil || !Equal(got.Proof, Proof(DeriveKey(c.Kind, c.Secret), port, nonce, clientEP, serverEP)) {
		return fail("The program on that port did not prove it is your Coral. Start Coral, or stop the program that holds its port.", err)
	}
	return nil
}

func (c Credential) scope() string {
	if c.Kind == KindOperator {
		return "command-line token"
	}
	return "agent's session"
}

func (c Credential) restartHint() string {
	if c.Kind == KindOperator {
		return ""
	}
	return ", or restart the agent"
}
