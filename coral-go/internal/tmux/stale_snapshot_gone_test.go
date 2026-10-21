package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// #4049 follow-up: a pane killed outside Coral still resolves from the shared pane list for up to a
// second, so only the command on it fails. That failure is ErrTerminalGone, as it was when every
// lookup listed the panes: the dashboard is told the terminal is gone (#3218), not a plain error.
func TestCommandOnAPaneKilledSinceTheSharedListIsTerminalGone(t *testing.T) {
	restore := SetSendKeysSettleForTest(time.Millisecond)
	defer restore()
	c, sibling := newSiblingOnly(t)
	ctx := context.Background()
	keep := filepath.Join(t.TempDir(), "keep")
	require.NoError(t, os.MkdirAll(keep, 0o755))
	createTestSession(t, c, "keep-4049", keep) // the tmux server outlives the killed session

	for _, tc := range []struct {
		name string
		do   func() error
	}{
		{"send-keys", func() error {
			return c.SendKeys(ctx, terminalinput.Unclassified, sibling, "echo hi", "claude", siblingSID)
		}},
		{"raw keys", func() error {
			return c.SendRawKeys(ctx, terminalinput.Unclassified, sibling, []string{"Escape"}, "claude", siblingSID)
		}},
		{"display", func() error {
			_, err := c.DisplayForAgent(ctx, sibling, "claude", siblingSID, "#{pane_pid}")
			return err
		}},
		{"resize", func() error { return c.ResizePane(ctx, sibling, 100, "claude", siblingSID) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !c.HasSession(ctx, "="+sibling) {
				createTestSession(t, c, sibling, keep)
			}
			target, err := c.FindPaneTarget(ctx, sibling, "claude", siblingSID) // the shared list now has it
			require.NoError(t, err)
			require.NotEmpty(t, target)
			// Killed behind Coral's back, so the shared list isn't dropped.
			require.NoError(t, exec.Command(c.resolveTmuxBin(), "-S", c.SocketPath, "kill-session", "-t", "="+sibling).Run())
			stale, err := c.FindPaneTarget(ctx, sibling, "claude", siblingSID)
			require.NoError(t, err)
			require.NotEmpty(t, stale, "the shared list still has the killed pane (the case under test)")

			err = tc.do()
			assert.True(t, errors.Is(err, ErrTerminalGone), "got %v", err)
		})
	}
}
