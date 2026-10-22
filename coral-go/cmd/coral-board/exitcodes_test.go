package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Each way a request can fail has its own exit code, and the message says what to do (#228). Run as subprocesses, so the exit code is the real one.
func TestExitCodesByFailure(t *testing.T) {
	release := make(chan struct{})
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == 0 {
			<-release // never answers
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(`{"error":"nope"}`))
	}))
	defer srv.Close()
	defer close(release)
	dir := subscribedDataDir(t, srv.URL)

	// 3: nothing listens
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + l.Addr().String()
	l.Close()
	res := runBoard(t, dead, subscribedDataDir(t, dead), "check")
	assert.Equal(t, 3, res.code, "refused: %q", res.stderr)
	assert.Contains(t, res.stderr, "cannot reach Coral server at "+dead)
	assert.Contains(t, res.stderr, "Is Coral running?")

	// 5 and 6: the server answers 401/403 and 404
	for st, want := range map[int]int{401: 5, 403: 5, 404: 6, 500: 1} {
		status = st
		res = runBoard(t, srv.URL, dir, "read")
		assert.Equal(t, want, res.code, "HTTP %d: %q", st, res.stderr)
	}

	// 4: the server accepts and never answers, and CORAL_TIMEOUT (1s is the shortest) ends the wait
	status = 0
	res = runBoardEnv(t, srv.URL, dir, []string{"CORAL_TIMEOUT=1s"}, "read")
	assert.Equal(t, 4, res.code, "timeout: %q", res.stderr)
	assert.Contains(t, res.stderr, "did not answer within 1s")
	assert.Contains(t, res.stderr, "CORAL_TIMEOUT")

	// 2: usage
	res = runBoard(t, srv.URL, dir, "read", "--nope")
	assert.Equal(t, 2, res.code)
}

func TestHelpHasTheExitCodeTable(t *testing.T) {
	srv := newRecordingServer(t)
	res := runBoard(t, srv.URL, subscribedDataDir(t, srv.URL), "--help")
	assert.Equal(t, 0, res.code)
	assert.True(t, strings.Contains(res.stdout, "Exit codes:") && strings.Contains(res.stdout, "CORAL_TIMEOUT"), res.stdout)
}

// The review verbs end with the code of what failed (QA's live check of #101): a 404 is 6, a missing --to or --note is a usage error (2), and the message
// carries the server's words, not "<nil>".
func TestReviewVerbsExitCodes(t *testing.T) {
	status := 404
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(`{"error":"Task not found"}`))
	}))
	defer srv.Close()
	dir := subscribedDataDir(t, srv.URL)
	for _, verb := range []string{"approve", "reject"} {
		res := runBoard(t, srv.URL, dir, "task", verb, "999", "--note", "x")
		assert.Equal(t, 6, res.code, "%s: %q", verb, res.stderr)
		assert.Contains(t, res.stderr, "Coral answered HTTP 404: Task not found", verb)
		assert.NotContains(t, res.stderr, "<nil>", verb)
	}
	status = 403
	res := runBoard(t, srv.URL, dir, "task", "review", "999", "--to", "QA Engineer")
	assert.Equal(t, 5, res.code, res.stderr)
	res = runBoard(t, srv.URL, dir, "task", "review", "999")
	assert.Equal(t, 2, res.code, "a missing --to is a usage error: %q", res.stderr)
	res = runBoard(t, srv.URL, dir, "task", "reject", "999")
	assert.Equal(t, 2, res.code, "a missing --note is a usage error: %q", res.stderr)
	res = runBoard(t, srv.URL, dir, "task", "send-back", "999")
	assert.Equal(t, 2, res.code, res.stderr)
}
