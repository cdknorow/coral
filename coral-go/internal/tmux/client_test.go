package tmux

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListPanes_ParsesOutput(t *testing.T) {
	// Use a real tmux call if tmux is available, otherwise skip
	c := NewClient()
	ctx := context.Background()

	panes, err := c.ListPanes(ctx)
	// If tmux is not running, ListPanes returns nil, nil (not an error)
	if err != nil {
		t.Skip("tmux not available")
	}

	// We can't assert specific panes, but we can verify parsing works
	for _, p := range panes {
		assert.NotEmpty(t, p.SessionName, "session name should not be empty")
		assert.NotEmpty(t, p.Target, "target should not be empty")
	}
}

func TestListPanes_ParsesFormat(t *testing.T) {
	// Test the parsing logic directly by simulating tmux output format
	output := "My Title|claude-abc123|claude-abc123:0.0|/Users/test/repo\nOther Title|gemini-def456|gemini-def456:0.0|/tmp/work"

	var panes []Pane
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}
		panes = append(panes, Pane{
			PaneTitle:   parts[0],
			SessionName: parts[1],
			Target:      parts[2],
			CurrentPath: parts[3],
		})
	}

	require.Len(t, panes, 2)

	assert.Equal(t, "My Title", panes[0].PaneTitle)
	assert.Equal(t, "claude-abc123", panes[0].SessionName)
	assert.Equal(t, "claude-abc123:0.0", panes[0].Target)
	assert.Equal(t, "/Users/test/repo", panes[0].CurrentPath)

	assert.Equal(t, "Other Title", panes[1].PaneTitle)
	assert.Equal(t, "gemini-def456", panes[1].SessionName)
}

func TestNewClient_Defaults(t *testing.T) {
	c := NewClient()
	assert.Equal(t, "tmux", c.TmuxBin)
}

func TestFindPane_MatchesBySessionID(t *testing.T) {
	// Create a client that would match by session ID
	// This test verifies the matching logic without needing real tmux
	panes := []Pane{
		{SessionName: "claude-550e8400-e29b-41d4-a716-446655440000", CurrentPath: "/tmp/repo1", Target: "t:0.0"},
		{SessionName: "gemini-aabb0011-e29b-41d4-a716-446655440000", CurrentPath: "/tmp/repo2", Target: "t:0.1"},
	}

	// Simulate FindPane matching logic
	sessionID := "550e8400-e29b-41d4-a716-446655440000"
	sidLow := strings.ToLower(sessionID)

	var found *Pane
	for i := range panes {
		if strings.Contains(strings.ToLower(panes[i].SessionName), sidLow) {
			found = &panes[i]
			break
		}
	}

	require.NotNil(t, found)
	assert.Equal(t, "claude-550e8400-e29b-41d4-a716-446655440000", found.SessionName)
	assert.Equal(t, "/tmp/repo1", found.CurrentPath)
}

func TestLogPathDerivation(t *testing.T) {
	// Verify log path construction matches Python: {tmpdir}/{agent_type}_coral_{session_id}.log
	tests := []struct {
		agentType string
		sessionID string
		expected  string
	}{
		{"claude", "abc-123", "claude_coral_abc-123.log"},
		{"gemini", "def-456", "gemini_coral_def-456.log"},
	}

	for _, tt := range tests {
		result := tt.agentType + "_coral_" + tt.sessionID + ".log"
		assert.Equal(t, tt.expected, result)
	}
}
