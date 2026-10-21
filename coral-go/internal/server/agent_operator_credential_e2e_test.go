package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/cliauth"
	"github.com/subgentic/coral/internal/store"
)

// #3513 end to end with strict local auth, the real router: an agent's token can't read the
// API key (with it the agent would drop its token and be the Operator) or delete its board;
// the Operator's signed command line can do both, and the agent can still leave its board.
func TestAgentTokenCantReadTheAPIKeyOrDeleteABoard(t *testing.T) {
	srv, ts, dir := strictServer(t, true)
	ctx := context.Background()
	agentTok, err := srv.db.MintSessionToken(ctx, "sid-dev", "claude-dev")
	require.NoError(t, err)
	cliTok, err := cliauth.ReadToken(dir)
	require.NoError(t, err)
	cli := cliauth.CLIClient{Base: ts.URL, Token: cliTok}
	asCLI := func(method, path, body string) (int, string) {
		resp, err := cli.Do(ctx, method, path, []byte(body), nil, 0)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	asAgent := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(store.SessionTokenHeader, agentTok)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	code, body := asCLI("POST", "/api/board/team/subscribe", `{"subscriber_id":"Dev","job_title":"Dev"}`)
	require.Equal(t, http.StatusOK, code, body)

	code, body = asAgent("GET", "/api/system/api-key", "")
	assert.Equal(t, http.StatusForbidden, code, body)
	assert.Equal(t, "forbidden_for_agent", codeOfBody([]byte(body)))
	assert.NotContains(t, body, "key\":\"", "no key in the refusal")

	code, body = asAgent("DELETE", "/api/board/team", "")
	assert.Equal(t, http.StatusForbidden, code, body)
	assert.Equal(t, "forbidden_for_agent", codeOfBody([]byte(body)))
	code, body = asCLI("GET", "/api/board/projects", "")
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, `"team"`, "the board is still there")

	code, body = asCLI("GET", "/api/system/api-key", "")
	assert.Equal(t, http.StatusOK, code, "the Operator reads the key: %s", body)
	code, body = asCLI("DELETE", "/api/board/team", "")
	assert.Equal(t, http.StatusOK, code, "the Operator deletes a board: %s", body)
}
