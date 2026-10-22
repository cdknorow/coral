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
