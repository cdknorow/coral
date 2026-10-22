package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// coral-agent exits with a code for each way a request fails (#228).
func TestAgentExitCodesByFailure(t *testing.T) {
	release := make(chan struct{})
	status := 404
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == 0 {
			<-release
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(`{"error":"no"}`))
	}))
	defer srv.Close()
	defer close(release)

	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + l.Addr().String()
	l.Close()
	code, _, stderr := runAgent(t, dead, "task", "list")
	assert.Equal(t, 3, code, stderr)
	assert.Contains(t, stderr, "Is Coral running?")

	for st, want := range map[int]int{401: 5, 403: 5, 404: 6, 500: 1} {
		status = st
		code, _, stderr = runAgent(t, srv.URL, "task", "list")
		assert.Equal(t, want, code, "HTTP %d: %q", st, stderr)
	}

	status = 0
	code, _, stderr = runAgentEnv(t, srv.URL, []string{"CORAL_TIMEOUT=1s"}, "task", "list")
	assert.Equal(t, 4, code, stderr)
	assert.Contains(t, stderr, "did not answer within 1s")

	code, _, _ = runAgent(t, srv.URL, "task", "list", "--nope")
	assert.Equal(t, 2, code)
}

// A value the command line does not take is a usage error: exit 2 and the usage line, nothing on stdout, and Coral is never asked (#282 rows 1, 2).
func TestAgentInputValidationIsAUsageError(t *testing.T) {
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked = true }))
	defer srv.Close()
	for _, args := range [][]string{
		{"task", "detail", "not-a-number"},
		{"ui", "events"},
		{"ui", "publish", "--file", "x.html"},
		{"artifact", "download", "not-a-uri"},
		{"history", "search"},
		{"not-a-command"},
	} {
		code, stdout, stderr := runAgent(t, srv.URL, args...)
		assert.Equal(t, 2, code, "%v: %q", args, stderr)
		assert.Empty(t, stdout, "%v prints nothing on stdout", args)
		assert.NotEmpty(t, stderr, "%v", args)
	}
	_, _, stderr := runAgent(t, srv.URL, "task", "detail", "not-a-number")
	assert.Contains(t, stderr, "Task ID must be a positive integer")
	assert.Contains(t, stderr, "Usage:")
	_, _, stderr = runAgent(t, srv.URL, "not-a-command")
	assert.Contains(t, stderr, "Unknown command: not-a-command")
	assert.NotContains(t, stderr, "Usage: coral-agent <command>", "a short message, not the whole help")
	_, _, stderr = runAgent(t, srv.URL, "ui", "events")
	assert.Contains(t, stderr, "events --id ID")
	assert.NotContains(t, stderr, "subagent", "the usage on error is the short list, not the handoff paragraph")
	assert.False(t, asked, "none of these reached Coral")
}
