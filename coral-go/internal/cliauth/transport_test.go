package cliauth

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// a Coral that answers the hello and accepts only correctly signed requests.
func signedServer(t *testing.T, token string) (*httptest.Server, *[]string) {
	t.Helper()
	a := NewCLIAuth(token)
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == CLIHelloPath {
			var b struct{ Nonce string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			sn, proof, err := a.Hello(b.Nonce)
			require.NoError(t, err)
			_ = json.NewEncoder(w).Encode(map[string]string{"nonce": sn, "proof": proof})
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !a.Verify(r, body) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+string(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// #3418: a person's command-line tool signs each request to a loopback Coral with the
// command-line token: the body and the method are covered, and each signature works once.
func TestTransportSignsRequests(t *testing.T) {
	srv, seen := signedServer(t, "the-token")
	c := &http.Client{Transport: Transport{Base: http.DefaultTransport, Token: "the-token"}}
	resp, err := c.Post(srv.URL+"/api/board/p/messages?x=1", "application/json", strings.NewReader(`{"content":"hi"}`))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp, err = c.Get(srv.URL + "/api/sessions/live")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{`POST /api/board/p/messages?x=1 {"content":"hi"}`, "GET /api/sessions/live "}, *seen)

	// A server that doesn't know the token can't prove it: the request is never sent, signed
	// or not (#3421 v2), and the error names the target and the folder the token came from.
	other, otherSeen := signedServer(t, "another-token")
	c2 := &http.Client{Transport: Transport{Base: http.DefaultTransport, Token: "the-token", Dir: "/data/coral"}}
	_, err = c2.Post(other.URL+"/api/board/p/messages", "application/json", strings.NewReader(`{"content":"x"}`))
	require.ErrorIs(t, err, ErrNotCoral)
	assert.Contains(t, err.Error(), other.URL)
	assert.Contains(t, err.Error(), "/data/coral")
	assert.Empty(t, *otherSeen, "nothing but the hello reached it")
}

type recordRT struct{ hdr http.Header }

func (r *recordRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.hdr = req.Header.Clone()
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(nil)), Request: req}, nil
}

// Only a loopback Coral gets a signature (no hello to anyone else).
func TestTransportOnlySignsLoopback(t *testing.T) {
	rec := &recordRT{}
	c := &http.Client{Transport: Transport{Base: rec, Token: "the-token"}}
	resp, err := c.Get("http://coral.example.com:8420/api/sessions/live")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Empty(t, rec.hdr.Get(CLIAuthHeader))
	assert.True(t, loopbackHost("localhost"))
	assert.True(t, loopbackHost("127.0.0.1"))
	assert.True(t, loopbackHost("::1"))
	assert.False(t, loopbackHost("10.0.0.2"))
}

// The token file must be this user's private regular file.
func TestReadToken(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, TokenFile)
	require.NoError(t, os.WriteFile(p, []byte("tok\n"), 0o600))
	got, err := ReadToken(dir)
	require.NoError(t, err)
	assert.Equal(t, "tok", got)
	if runtime.GOOS == "windows" {
		return
	}
	require.NoError(t, os.Chmod(p, 0o644))
	_, err = ReadToken(dir)
	assert.Error(t, err, "readable by others")
	require.NoError(t, os.Chmod(p, 0o600))
	link := t.TempDir()
	require.NoError(t, os.Symlink(p, filepath.Join(link, TokenFile)))
	_, err = ReadToken(link)
	assert.Error(t, err, "a link")
	_, err = ReadToken(t.TempDir())
	assert.Error(t, err, "missing")
}

// Never inside an agent session (#3418, Orchestrator's case): an agent whose token was
// removed but whose environment points at a readable command-line token does NOT sign;
// nor does a process that has a session token.
func TestInstallOperatorSigningNeverInsideAnAgent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, TokenFile), []byte("tok"), 0o600))
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	t.Setenv("CORAL_DATA_DIR", dir)

	t.Setenv("CORAL_SESSION_NAME", "claude-dev")
	t.Setenv("CORAL_SESSION_TOKEN", "")
	assert.True(t, InsideAgentSession())
	assert.False(t, InstallOperatorSigning(), "inside an agent session")
	_, signing := http.DefaultTransport.(Transport)
	assert.False(t, signing)

	t.Setenv("CORAL_SESSION_NAME", "")
	for _, marker := range []string{"CORAL_SESSION_TOKEN", "CORAL_SESSION_ID", "CORAL_SUBSCRIBER_ID"} {
		t.Setenv(marker, "set-by-coral")
		assert.True(t, InsideAgentSession(), marker)
		assert.False(t, InstallOperatorSigning(), "any agent marker: "+marker)
		t.Setenv(marker, "")
	}

	assert.True(t, InstallOperatorSigning(), "a person's terminal")
	_, signing = http.DefaultTransport.(Transport)
	assert.True(t, signing)
	assert.True(t, InstallOperatorSigning(), "idempotent")
	_, inner := http.DefaultTransport.(Transport).Base.(Transport)
	assert.False(t, inner, "not wrapped twice")
}
