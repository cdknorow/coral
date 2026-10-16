package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/cliauth"
	"github.com/cdknorow/coral/internal/cloud"
	"github.com/cdknorow/coral/internal/store/storetest"
)

// #4041 (Security's #4039 finding 4): coral reset-password also replaces the command-line
// token, so whatever read the old one can no longer sign as the Operator; the command line
// (which reads the token file) keeps working.
func TestResetPasswordRotatesTheCLIToken(t *testing.T) {
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	cfg.StrictLocalAuth = true
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := NewWithKey(cfg, db, stack.backend, stack.terminal, "")
	t.Cleanup(srv.Close)
	local := httptest.NewServer(srv.Router())
	t.Cleanup(local.Close)
	ctx := context.Background()

	old, err := cloud.ReadCLIToken(cfg.CoralDir())
	require.NoError(t, err)
	status := func(tok string) (int, error) {
		resp, err := cloud.CLIClient{Base: local.URL, Token: tok}.Do(ctx, http.MethodGet, "/api/sessions/live", nil, nil, 5*time.Second)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return resp.StatusCode, nil
	}
	code, err := status(old)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code, "the token signs before the reset")

	resp, err := cloud.CLIClient{Base: local.URL, Token: old}.Do(ctx, http.MethodPost, "/api/auth/password/reset", []byte(`{"password":"a new one"}`), nil, 30*time.Second)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	fresh, err := cloud.ReadCLIToken(cfg.CoralDir())
	require.NoError(t, err)
	assert.NotEqual(t, old, fresh, "a new token was written")
	_, err = status(old)
	assert.ErrorIs(t, err, cliauth.ErrNotCoral, "the old token no longer signs")
	code, err = status(fresh)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, code, "the new token signs")

	// A long-running tool that read the old token at its start reads the new one, once.
	c := &http.Client{Transport: cliauth.Transport{Base: http.DefaultTransport, Token: old, Dir: cfg.CoralDir()}}
	r2, err := c.Get(local.URL + "/api/sessions/live")
	require.NoError(t, err)
	r2.Body.Close()
	assert.Equal(t, http.StatusOK, r2.StatusCode)
}

// #4041: a coral command from before the query was signed (its MAC leaves the query out) is
// refused on a request with a query, and the refusal says to use the coral of this version.
func TestOlderCLISignatureIsRefusedWithAnUpdateHint(t *testing.T) {
	dir := t.TempDir()
	cfg := newE2EConfig(t, dir)
	cfg.StrictLocalAuth = true
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := NewWithKey(cfg, db, stack.backend, stack.terminal, "")
	t.Cleanup(srv.Close)
	local := httptest.NewServer(srv.Router())
	t.Cleanup(local.Close)
	tok, err := cloud.ReadCLIToken(cfg.CoralDir())
	require.NoError(t, err)

	send := func(sig string) (int, string) {
		req, err := http.NewRequest("GET", local.URL+"/api/sessions/live?limit=5", nil)
		require.NoError(t, err)
		req.Header.Set(cliauth.CLIAuthHeader, sig)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	code, body := send(cliSignServer(t, local.URL, tok, "GET", "/api/sessions/live", "")) // the old MAC: no query
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Contains(t, body, "cli_signature_required")
	assert.Contains(t, body, "Use the coral command that came with this Coral")
	code, _ = send(cliSignServer(t, local.URL, tok, "GET", "/api/sessions/live?limit=5", ""))
	assert.Equal(t, http.StatusOK, code, "this version's signature")
}
