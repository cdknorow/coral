package background

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// #3376: the notifier and the remote-board poller never type into a terminal agent,
// whose terminal is a shell.
func TestSkipIfAgentExitedSkipsTerminalAgents(t *testing.T) {
	assert.True(t, skipIfAgentExited(context.Background(), nil, nil, "terminal-11111111-2222-3333-4444-555555555555", "board nudge"))
	assert.False(t, skipIfAgentExited(context.Background(), nil, nil, "claude-11111111-2222-3333-4444-555555555555", "board nudge"))
}
