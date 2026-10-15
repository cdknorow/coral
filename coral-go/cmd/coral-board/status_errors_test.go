package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serverError turns a non-2xx reply into a non-nil error that names the status and the server's
// code, and adds the loopback hint only for a 401 from a non-loopback CORAL_URL (#3589).
func TestServerError(t *testing.T) {
	saved := serverURL
	t.Cleanup(func() { serverURL = saved })

	serverURL = "http://127.0.0.1:8420"
	assert.NoError(t, serverError(200, map[string]any{}))
	assert.NoError(t, serverError(201, nil))
	for _, status := range []int{400, 401, 403, 500} {
		err := serverError(status, map[string]any{"error": "unauthorized", "code": "unauthorized"})
		require.Error(t, err, "status %d", status)
		assert.Contains(t, err.Error(), strconv.Itoa(status))
		assert.Contains(t, err.Error(), "unauthorized")
	}
	// A 401 from loopback: no hint (an agent on its own machine should just work).
	assert.NotContains(t, serverError(401, map[string]any{"error": "x"}).Error(), "loopback address")

	// A 401 from a non-loopback CORAL_URL: the hint is added.
	serverURL = "http://coral.example.test:8420"
	assert.Contains(t, serverError(401, map[string]any{"error": "x"}).Error(), "loopback address")
	// A non-401 from non-loopback: no hint.
	assert.NotContains(t, serverError(500, map[string]any{"error": "x"}).Error(), "loopback address")

	// The message is preferred over the code, and neither echoes a token.
	assert.Contains(t, serverError(403, map[string]any{"message": "forbidden here", "code": "forbidden"}).Error(), "forbidden here")

	// Userinfo embedded in CORAL_URL is never echoed in an error message.
	serverURL = "http://agent:s3cret-token@coral.example.test:8420"
	e := serverError(401, map[string]any{"error": "x"}).Error()
	assert.NotContains(t, e, "s3cret-token")
	assert.NotContains(t, e, "agent:")
	assert.Contains(t, e, "coral.example.test")
}

func TestIsLoopbackServer(t *testing.T) {
	saved := serverURL
	t.Cleanup(func() { serverURL = saved })
	for _, u := range []string{"http://127.0.0.1:8420", "http://localhost:8420", "http://[::1]:8420", "http://127.5.5.5:1"} {
		serverURL = u
		assert.True(t, isLoopbackServer(), u)
	}
	for _, u := range []string{
		"http://coral.example.test", "http://100.64.1.2:8420", "http://10.0.0.1",
		"http://127.0.0.1.example.com:8420", // a DNS name that merely starts with 127.0.0.1
		"http://localhost.example.com",
	} {
		serverURL = u
		assert.False(t, isLoopbackServer(), u)
	}
}

// End to end: a non-2xx reply makes join, post, read and check exit non-zero with the status on
// stderr and no success line on stdout; a 200 still works (#3589).
func TestCLICommandsFailLoudlyOnNon2xx(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"error":"denied","code":"denied"}`)
		}))
		dir := subscribedDataDir(t, srv.URL)
		for _, args := range [][]string{
			{"join", "team", "--as", "QA"},
			{"post", "hello world"},
			{"read"},
			{"read", "--last", "5"},
			{"check"},
			{"export", "--output", t.TempDir() + "/export.json"},
		} {
			run := runBoard(t, srv.URL, dir, args...)
			label := fmt.Sprintf("status %d, args %v", status, args)
			assert.NotEqual(t, 0, run.code, "%s must exit non-zero; stdout=%q", label, run.stdout)
			assert.Contains(t, run.stderr, strconv.Itoa(status), "%s stderr must name the status: %q", label, run.stderr)
			assert.NotContains(t, run.stdout, "posted", label)
			assert.NotContains(t, run.stdout, "No new messages", label)
		}
		srv.Close()
	}
}

func TestCLICommandsSucceedOn2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/subscribe"):
			fmt.Fprint(w, `{"project":"team"}`)
		case strings.HasSuffix(r.URL.Path, "/messages") && r.Method == "POST":
			fmt.Fprint(w, `{"id":7}`)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(r.URL.Path, "/messages/check"):
			fmt.Fprint(w, `{"unread":0}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	dir := subscribedDataDir(t, srv.URL)

	run := runBoard(t, srv.URL, dir, "post", "hi")
	assert.Equal(t, 0, run.code, "post stderr=%q", run.stderr)
	assert.Contains(t, run.stdout, "posted")
	run = runBoard(t, srv.URL, dir, "read")
	assert.Equal(t, 0, run.code, "read stderr=%q", run.stderr)
	assert.Contains(t, run.stdout, "No new messages")
	run = runBoard(t, srv.URL, dir, "check")
	assert.Equal(t, 0, run.code, "check stderr=%q", run.stderr)
}
