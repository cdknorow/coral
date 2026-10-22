package server

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/serverproof"
	"github.com/subgentic/coral/internal/store"
	"github.com/subgentic/coral/internal/store/storetest"
	"github.com/subgentic/coral/pkg/tunnel"
)

// #6103: the real router proves itself to a tool on this computer, on the connection the tool then uses: to the person's command-line tools with the command-line token's key, and to an agent's tools
// with the key of the session token it holds. It answers with no credential even under strict local auth, and never through Coral Hub.

func proofServer(t *testing.T, strict bool) (*httptest.Server, *store.DB, string) {
	t.Helper()
	cfg := newE2EConfig(t, t.TempDir())
	cfg.StrictLocalAuth = strict
	db, err := storetest.Open(t, cfg.DBPath)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	stack := newTermStack(t)
	srv := newTestServer(t, cfg, db, stack.backend, stack.terminal)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts, db, cfg.CoralDir()
}

func healthThrough(t *testing.T, base string, cred serverproof.Credential) error {
	t.Helper()
	rt, ok := serverproof.NewTransport(&http.Transport{}, cred)
	require.True(t, ok)
	defer rt.(*http.Transport).CloseIdleConnections()
	resp, err := (&http.Client{Transport: rt}).Get(base + "/api/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	require.Equal(t, 200, resp.StatusCode)
	return nil
}

func TestTheRealRouterProvesItselfToTheCommandLineAndToAnAgentsTools(t *testing.T) {
	for _, strict := range []bool{false, true} {
		ts, db, dir := proofServer(t, strict)
		tok, err := cliauth.ReadToken(dir)
		require.NoError(t, err)
		assert.NoError(t, healthThrough(t, ts.URL, serverproof.Credential{Kind: serverproof.KindOperator, Secret: tok}), "strict=%v: the command-line token's key", strict)
		assert.Error(t, healthThrough(t, ts.URL, serverproof.Credential{Kind: serverproof.KindOperator, Secret: "another"}), "another command-line token is refused")

		session, err := db.MintSessionToken(context.Background(), "aaaaaaaa-6103-4000-8000-000000000001", "claude-x")
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(session, serverproof.SessionTokenPrefix), "a new session token is marked: its tools ask for the proof")
		assert.NoError(t, healthThrough(t, ts.URL, serverproof.Credential{Kind: serverproof.KindSession, Secret: session}), "strict=%v: the session's key", strict)

		// minting again revokes the first token: its key is gone with it
		_, err = db.MintSessionToken(context.Background(), "aaaaaaaa-6103-4000-8000-000000000001", "claude-x")
		require.NoError(t, err)
		err = healthThrough(t, ts.URL, serverproof.Credential{Kind: serverproof.KindSession, Secret: session})
		var re *serverproof.RefusedError
		require.ErrorAs(t, err, &re, "a revoked session token cannot be proved to")
		var stored sql.NullString
		require.NoError(t, db.GetContext(context.Background(), &stored, `SELECT proof_key FROM session_tokens WHERE token_hash = ?`, serverproof.TokenHash(session)))
		assert.False(t, stored.Valid, "the revoked token's proof key is dropped from the database, not only refused")
	}
}

// An agent started before proof keys existed holds a token with no mark and no stored key: Coral has none to prove with, and its tools do not ask (the transport only asks for a marked token).
func TestAnOlderSessionTokenHasNoProofKey(t *testing.T) {
	_, db, _ := proofServer(t, false)
	old := "an-older-token-without-the-mark"
	_, err := db.ExecContext(context.Background(), `INSERT INTO session_tokens (token_hash, session_id, session_name, created_at) VALUES (?, ?, ?, ?)`,
		serverproof.TokenHash(old), "aaaaaaaa-6103-4000-8000-000000000002", "claude-old", "2026-10-22T00:00:00Z")
	require.NoError(t, err)
	_, ok := db.SessionProofKey(context.Background(), serverproof.TokenHash(old))
	assert.False(t, ok)
	owner, err := db.SessionTokenOwnerOf(context.Background(), old)
	require.NoError(t, err, "the token itself still works as before")
	assert.Equal(t, "claude-old", owner.SessionName)
}

func TestTheProofRouteAnswersOnlyAChallengeAndNeverThroughTheHub(t *testing.T) {
	ts, _, _ := proofServer(t, true)
	nonce := strings.Repeat("ab", 16)
	do := func(method, query string, hdr map[string]string) int {
		req, _ := http.NewRequest(method, ts.URL+serverproof.Path+query, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	assert.Equal(t, 200, do("GET", "?k=operator&n="+nonce, nil), "no credential, strict local auth on")
	assert.Equal(t, 400, do("GET", "?k=operator", nil))
	assert.Equal(t, 400, do("GET", "?k=operator&n="+nonce+"&x=1", nil))
	assert.Equal(t, 404, do("GET", "?k=session&n="+nonce+"&h="+strings.Repeat("a", 64), nil), "a session Coral has no key for")
	assert.Contains(t, []int{401, 405}, do("POST", "?k=operator&n="+nonce, nil), "only GET is open: another method is refused (by strict local auth first, or as not allowed)")
	assert.Equal(t, 403, do("GET", "?k=operator&n="+nonce, map[string]string{tunnel.ViaHeader: "hub"}), "never through Coral Hub")
}
