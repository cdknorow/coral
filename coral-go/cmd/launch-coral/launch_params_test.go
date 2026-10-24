package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/config"
)

// #6799 (the #6767 class): an agent launch-coral starts carries the address of the server it starts, its data folder and its own session name; before, only its session id, so its hooks and coral-board
// dialed the default :8420.
func TestLaunchCoralAgentsKnowTheServerAddress(t *testing.T) {
	cfg := &config.Config{Port: 8461}
	cfg.UseCoralDir(t.TempDir())
	p := launchParamsFor(cfg, "00000000-0000-4000-8000-000000000001", "claude-00000000-0000-4000-8000-000000000001", "/proto/PROTOCOL.md", t.TempDir())
	assert.Equal(t, 8461, p.CoralPort)
	assert.Equal(t, cfg.CoralDir(), p.CoralDir)
	assert.Equal(t, "claude-00000000-0000-4000-8000-000000000001", p.SessionName)
	assert.NotEmpty(t, p.CoralHost)

	cmd, err := agent.GetAgent("claude").LaunchCommand(p)
	require.NoError(t, err)
	for _, want := range []string{"CORAL_PORT", "8461", "CORAL_URL", "CORAL_SESSION_NAME", "CORAL_DATA_DIR"} {
		assert.True(t, strings.Contains(cmd, want), "the launch line carries %s: %s", want, cmd)
	}
}
