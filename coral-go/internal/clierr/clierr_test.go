package clierr

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
