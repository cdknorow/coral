package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3627 F1: a live session's JSON names its MCP servers and nothing more; the stored value
// (env, headers, args) is still there for launch, restart and wake.
func TestLiveSessionJSONListsMCPServerNamesOnly(t *testing.T) {
	mcp := `{"github":{"command":"gh-mcp","env":{"GITHUB_TOKEN":"ghp_SECRETvalue123"}},"search":{"url":"https://x","headers":{"Authorization":"Bearer SECRET2"}}}`
	ls := LiveSession{SessionID: "s1", AgentType: "claude", AgentName: "claude-s1", MCPServers: &mcp}
	for _, v := range []any{ls, &ls, []LiveSession{ls}, map[string]any{"session": ls}} {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		s := string(b)
		for _, secret := range []string{"ghp_SECRETvalue123", "SECRET2", "GITHUB_TOKEN", "Authorization", "gh-mcp"} {
			assert.NotContains(t, s, secret)
		}
		assert.Contains(t, s, `"mcp_servers":["github","search"]`)
		assert.Contains(t, s, `"session_id":"s1"`, "the other fields are unchanged")
	}
	assert.Contains(t, *ls.MCPServers, "ghp_SECRETvalue123", "the stored configuration is untouched")
	got := UnmarshalMCPServers(ls.MCPServers)
	require.Contains(t, got, "github")

	none := LiveSession{SessionID: "s2"}
	b, _ := json.Marshal(none)
	assert.False(t, strings.Contains(string(b), "mcp_servers"), "omitted when there are none")
}
