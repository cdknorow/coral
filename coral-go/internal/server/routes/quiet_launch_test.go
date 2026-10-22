package routes

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// quietTerm is a terminal that can start an agent quietly (ptymanager.QuietLauncher), as the PTY and tmux terminals can.
type quietTerm struct {
	*mockSessionTerminal
	quiet, plain []string
}

func (q *quietTerm) SendToTarget(ctx context.Context, o terminalinput.Origin, target, command string) error {
	q.plain = append(q.plain, command)
	return nil
}

func (q *quietTerm) SendQuietLaunchToTarget(ctx context.Context, o terminalinput.Origin, target, command string) error {
	q.quiet = append(q.quiet, command)
	return nil
}

// #5911: every start of an agent (new, team member, restart, resume, wake) is typed through sendLaunchCommand, which uses the quiet launch where the terminal has one and the old typing where
// it has not (a test's terminal), and delivers the same command either way.
func TestSendLaunchCommandIsQuietWhereTheTerminalCanBe(t *testing.T) {
	q := &quietTerm{mockSessionTerminal: newMockTerminal()}
	require.NoError(t, sendLaunchCommand(context.Background(), q, "t", "claude --x"))
	assert.Equal(t, []string{"claude --x"}, q.quiet)
	assert.Empty(t, q.plain, "not typed in full")

	plain := newMockTerminal()
	require.NoError(t, sendLaunchCommand(context.Background(), plain, "t", "claude --x"))
	assert.Equal(t, []string{"claude --x"}, plain.sentCommands())
}
