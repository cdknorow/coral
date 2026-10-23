package ptymanager

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A server started inside a Claude Code session does not hand that session's markers to an agent it launches; the credentials, the provider switches and a person's own settings do pass.
func TestSessionEnvDropsClaudeCodeSessionMarkers(t *testing.T) {
	for k, v := range map[string]string{
		"CLAUDECODE": "1", "CLAUDE_CODE_CHILD_SESSION": "1", "CLAUDE_CODE_ENTRYPOINT": "cli", "CLAUDE_CODE_SESSION_ID": "abc", "CLAUDE_CODE_MESSAGING_TOKEN": "t",
		"CLAUDE_CODE_OAUTH_TOKEN": "o", "CLAUDE_CODE_USE_BEDROCK": "1", "ANTHROPIC_API_KEY": "k", "CLAUDE_CONFIG_DIR": "/c",
	} {
		t.Setenv(k, v)
	}
	names := map[string]string{}
	for _, kv := range sessionEnv("agent-1") {
		n, v, _ := strings.Cut(kv, "=")
		names[strings.ToUpper(n)] = v
	}
	for _, gone := range []string{"CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_MESSAGING_TOKEN"} {
		assert.NotContains(t, names, gone, "the parent session's marker does not reach the agent")
	}
	assert.Equal(t, "o", names["CLAUDE_CODE_OAUTH_TOKEN"])
	assert.Equal(t, "1", names["CLAUDE_CODE_USE_BEDROCK"])
	assert.Equal(t, "k", names["ANTHROPIC_API_KEY"])
	assert.Equal(t, "/c", names["CLAUDE_CONFIG_DIR"])
	assert.Equal(t, "agent-1", names["CORAL_SESSION_NAME"])
}
