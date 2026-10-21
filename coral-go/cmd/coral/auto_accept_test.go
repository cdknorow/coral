package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cliauth"
)

func TestAutoAcceptOffAll(t *testing.T) {
	t.Setenv("CORAL_DATA_DIR", t.TempDir()) // never a real Coral's command-line token
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path + " " + r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stopped":3}`))
	}))
	defer srv.Close()
	var out, errb bytes.Buffer
	assert.Equal(t, 0, runAutoAccept([]string{"off", "--all", "--url", srv.URL}, &out, &errb), errb.String())
	assert.Equal(t, "POST /api/auto-accept/disable-all application/json", got)
	assert.Contains(t, out.String(), "3 turned off")

	t.Setenv("CORAL_URL", srv.URL)
	out.Reset()
	assert.Equal(t, 0, runAutoAccept([]string{"off", "--all"}, &out, &errb), "CORAL_URL is the default server")

	for _, bad := range [][]string{nil, {"on"}, {"off"}, {"off", "--all", "extra"}} {
		assert.Equal(t, 2, runAutoAccept(bad, &out, &errb), "%v", bad)
	}
	srv.Close()
	assert.Equal(t, 1, runAutoAccept([]string{"off", "--all", "--url", srv.URL}, &out, &errb), "server down")

	code, ok := subcommand([]string{"--port", "8450"})
	assert.False(t, ok, "flags still start the server")
	assert.Zero(t, code)
}

// #3421 v2: with a command-line token the kill switch signs; a server that can't prove it
// knows the token gets nothing but the hello, and the error says which folder was used.
func TestAutoAcceptOffNeverSendsUnsigned(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, cliauth.TokenFile), []byte("some-token"), 0o600))
	t.Setenv("CORAL_DATA_DIR", dir)
	for _, k := range []string{"CORAL_SESSION_NAME", "CORAL_SESSION_ID", "CORAL_SESSION_TOKEN", "CORAL_SUBSCRIBER_ID"} {
		t.Setenv(k, "")
	}
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path+" signed="+strconv.FormatBool(r.Header.Get(cliauth.CLIAuthHeader) != ""))
		if r.URL.Path == cliauth.CLIHelloPath {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"stopped":1}`))
	}))
	defer srv.Close()
	var out, errb bytes.Buffer
	assert.Equal(t, 1, runAutoAccept([]string{"off", "--all", "--url", srv.URL}, &out, &errb))
	assert.Equal(t, []string{"POST " + cliauth.CLIHelloPath + " signed=false"}, calls, "only the hello")
	assert.Contains(t, errb.String(), dir)
	assert.Contains(t, errb.String(), "could not prove")
}
