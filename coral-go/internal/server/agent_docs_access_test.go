package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/store"
)

// With strict local auth (the default) the agent docs are reachable the way an agent's own tool reaches them:
// with its session token, as coral-agent docs sends it. A plain curl has no credential and is refused, and the
// refusal tells an agent what to do instead of only how a person signs in.
func TestAgentDocsAreReachableWithTheSessionToken(t *testing.T) {
	srv, ts, _ := strictServer(t, true)
	get := func(path string, hdr http.Header) (int, []byte) {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		for k, v := range hdr {
			req.Header[k] = v
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	tok, err := srv.db.MintSessionToken(context.Background(), "sid-docs", "claude-docs")
	require.NoError(t, err)
	agent := http.Header{store.SessionTokenHeader: {tok}, "User-Agent": {"coral-agent/dev"}}

	code, body := get("/api/agent-docs", agent)
	require.Equal(t, http.StatusOK, code, string(body))
	var list []struct{ Name, Title string }
	require.NoError(t, json.Unmarshal(body, &list), string(body))
	if len(list) == 0 {
		t.Skip("this build embeds no agent docs (run tools/sync-agent-docs.sh)")
	}
	code, body = get("/api/agent-docs/"+list[0].Name, agent)
	assert.Equal(t, http.StatusOK, code, string(body))
	assert.Contains(t, string(body), `"content"`)
	code, body = get("/api/agent-docs/all", agent)
	assert.Equal(t, http.StatusOK, code)
	assert.NotEmpty(t, body)

	// A bare curl: refused, and the text names the agent's way in.
	code, body = get("/api/agent-docs", nil)
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "dashboard_auth_required", codeOfBody(body))
	var refusal struct{ Error string }
	require.NoError(t, json.Unmarshal(body, &refusal))
	assert.True(t, strings.Contains(refusal.Error, "coral-agent docs") && strings.Contains(refusal.Error, "X-Coral-Session-Token"), refusal.Error)
	assert.Contains(t, refusal.Error, "coral open", "a person is still told how to sign in")
}
