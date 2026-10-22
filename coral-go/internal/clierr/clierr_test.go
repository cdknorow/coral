package clierr

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/serverproof"
)

func TestExitCodes(t *testing.T) {
	want := map[Kind]int{Other: 1, Usage: 2, Unreachable: 3, TimedOut: 4, Refused: 5, NotFound: 6}
	for k, code := range want {
		assert.Equal(t, code, k.Code())
	}
	assert.Equal(t, Refused, StatusKind(401))
	assert.Equal(t, Refused, StatusKind(403))
	assert.Equal(t, NotFound, StatusKind(404))
	assert.Equal(t, Other, StatusKind(500))
	assert.Equal(t, Other, StatusKind(200))
}

// A real connection to a port nothing listens on: on Windows this is WSAECONNREFUSED ("connectex"), which is not Go's syscall.ECONNREFUSED.
func TestRefusedConnectionIsClassifiedAndExplained(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	l.Close()
	_, err = (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/")
	require.Error(t, err)

	e := CannotReach("http://"+addr, err, 5*time.Second)
	assert.Equal(t, Unreachable, KindOf(e))
	assert.Contains(t, e.Error(), "cannot reach Coral server at http://"+addr)
	assert.Contains(t, e.Error(), "Is Coral running?")
	assert.Contains(t, e.Error(), "CORAL_URL")
	assert.NotContains(t, e.Error(), "connectex", "Go's raw message is not what the person reads")
	assert.NotContains(t, e.Error(), "dial tcp")
}

func TestATimeoutIsClassifiedAndExplained(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	_, err := (&http.Client{Timeout: 150 * time.Millisecond}).Get(srv.URL)
	require.Error(t, err)

	e := CannotReach(srv.URL, err, 150*time.Millisecond)
	assert.Equal(t, TimedOut, KindOf(e))
	assert.Contains(t, e.Error(), "cannot reach Coral server at "+srv.URL)
	assert.Contains(t, e.Error(), "did not answer within 150ms")
	assert.Contains(t, e.Error(), "CORAL_TIMEOUT")

	ctxErr := context.DeadlineExceeded
	assert.Equal(t, TimedOut, KindOf(CannotReach("http://x", ctxErr, time.Second)))
}

func TestAHostThatCannotBeFoundIsClassified(t *testing.T) {
	err := &net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}
	e := CannotReach("http://nope.invalid:8420", err, time.Second)
	assert.Equal(t, Unreachable, KindOf(e))
	assert.Contains(t, e.Error(), "host name cannot be found")
	assert.Contains(t, e.Error(), "http://nope.invalid:8420")
}

// The errno of a refused connection as each OS reports it, wrapped as net/http wraps it.
func TestRefusedErrnoWrappedLikeNetHTTP(t *testing.T) {
	for _, errno := range []syscall.Errno{refusedErrnoForTest} {
		err := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)}
		e := CannotReach("http://127.0.0.1:1", err, time.Second)
		assert.Equal(t, Unreachable, KindOf(e), "errno %d", errno)
		assert.Contains(t, e.Error(), "Is Coral running?", "errno %d", errno)
	}
	other := errors.New("tls: handshake failure")
	e := CannotReach("https://x", other, time.Second)
	assert.Equal(t, Unreachable, KindOf(e), "an error with no answer is still 'cannot reach'")
	assert.Contains(t, e.Error(), "tls: handshake failure", "an unclassified error keeps Go's words")
}

func TestKindOfPlainErrorsAndWrapped(t *testing.T) {
	assert.Equal(t, Other, KindOf(errors.New("x")))
	assert.Equal(t, TimedOut, KindOf(&Error{Kind: TimedOut, Msg: "m"}))
	wrapped := errors.Join(errors.New("ctx"), &Error{Kind: Refused, Msg: "m"})
	assert.Equal(t, Refused, KindOf(wrapped))
}

func TestTimeout(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", 10 * time.Second}, {"30s", 30 * time.Second}, {"2m", 2 * time.Minute}, {" 45s ", 45 * time.Second},
		{"100ms", MinTimeout}, {"2h", MaxTimeout}, {"abc", 10 * time.Second}, {"-5s", 10 * time.Second}, {"0", 10 * time.Second}, {"30", 10 * time.Second},
	}
	for _, c := range cases {
		t.Setenv("CORAL_TIMEOUT", c.env)
		assert.Equal(t, c.want, Timeout(10*time.Second), "CORAL_TIMEOUT=%q", c.env)
	}
}

func TestDescribe(t *testing.T) {
	assert.Equal(t, "Coral says: task not found (HTTP 404)", Describe(nil, 404, []byte(`{"error":"task not found"}`)))
	assert.Equal(t, "Coral says: not yours (HTTP 403)", Describe(nil, 403, []byte(`{"message":"not yours","error":"x"}`)))
	assert.Equal(t, "Coral says: bad gateway (HTTP 502)", Describe(nil, 502, []byte("  bad gateway  ")))
	assert.Equal(t, "Coral returned an error (HTTP 500)", Describe(nil, 500, nil))
	assert.Equal(t, "boom", Describe(errors.New("boom"), 0, nil))
	assert.NotContains(t, Describe(nil, 404, []byte(`{}`)), "nil")
}

// #6103: a program on Coral's port that could not prove it is Coral is "refused" (5), never "cannot reach" (3), whether the tool is the agent's (the session token) or the person's (the
// command-line token): a script that restarts Coral on 3 would start a second one beside the foreign program.
func TestAProofRefusalIsRefusedNotCannotReach(t *testing.T) {
	imposter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	t.Cleanup(imposter.Close)
	for name, cred := range map[string]serverproof.Credential{
		"the agent's token":      {Kind: serverproof.KindSession, Secret: "v2.abc"},
		"the command-line token": {Kind: serverproof.KindOperator, Secret: "tok"},
	} {
		t.Run(name, func(t *testing.T) {
			rt, ok := serverproof.NewTransport(&http.Transport{}, cred)
			require.True(t, ok)
			_, err := (&http.Client{Transport: rt, Timeout: 5 * time.Second}).Get(imposter.URL + "/api/x")
			require.Error(t, err)
			e := CannotReach(imposter.URL, err, 5*time.Second)
			assert.Equal(t, Refused, KindOf(e))
			assert.Equal(t, ExitRefused, KindOf(e).Code())
			assert.True(t, strings.HasPrefix(e.Error(), "Refused: "+name+" was not sent to port"), e.Error())
			assert.NotContains(t, e.Error(), "cannot reach")
		})
	}
}

// A reply is printed in a terminal, so what answered at CORAL_URL cannot drive it: control sequences go, newlines become spaces, tabs stay (Security's note on #101).
func TestDescribeStripsTerminalControl(t *testing.T) {
	esc := string([]byte{27})
	body := []byte(`{"error":"bad` + `\u001b]0;pwned\u0007` + `\u001b[31m red\u001b[0m` + `\nline two` + `\ttabbed"}`)
	got := Describe(nil, 400, body)
	assert.NotContains(t, got, esc)
	assert.NotContains(t, got, "pwned\u0007")
	assert.NotContains(t, got, "\n")
	assert.Contains(t, got, "line two")
	assert.Contains(t, got, "\ttabbed")
	assert.True(t, strings.HasSuffix(got, "(HTTP 400)"), got)
}

// A long reply is cut at a character, not in the middle of one.
func TestDescribeCutsAtACharacterBoundary(t *testing.T) {
	for pad := 0; pad < 4; pad++ {
		got := Describe(nil, 500, []byte(strings.Repeat("a", 298+pad)+strings.Repeat("é", 20)))
		assert.True(t, utf8.ValidString(got), "pad %d: %q", pad, got)
		assert.Contains(t, got, "...")
	}
}
