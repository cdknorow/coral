package tmux

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// #3687 (#3684 finding 2): an agent sets its own pane's title (OSC 2), so a title naming another
// agent must never make its pane that agent's. Agent B titles its pane with agent A's name and
// session name; only A's session id (or A's exact session name) finds A, and once A is gone
// nothing finds B in its place.
func TestAPaneTitledWithAnotherAgentIsNeverThatAgent(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const sidA, sidB = "aaaaaaaa-3687-4000-8000-00000000000a", "bbbbbbbb-3687-4000-8000-00000000000b"
	nameA, nameB := "claude-"+sidA, "claude-"+sidB
	createTestSession(t, c, nameA, t.TempDir())
	createTestSession(t, c, nameB, t.TempDir())

	// B sets its title from inside its pane, as an agent's output would.
	require.NoError(t, c.SendKeysToTarget(ctx, terminalinput.Unclassified, nameB+":0.0", `printf '\033]2;`+nameA+` Backend Dev\007'`))
	titled := func() bool {
		panes, _ := c.ListPanes(ctx)
		for _, p := range panes {
			if p.SessionName == nameB && p.PaneTitle == nameA+" Backend Dev" {
				return true
			}
		}
		return false
	}
	if !assert.Eventually(t, titled, 3*time.Second, 25*time.Millisecond, "OSC 2 set the title") {
		c.SetPaneTitle(ctx, nameB+":0.0", nameA+" Backend Dev")
		require.Eventually(t, titled, 3*time.Second, 25*time.Millisecond)
	}

	for _, name := range []string{"Backend Dev", "backend-dev", "Backend", sidA[:8]} {
		pane, err := c.FindPane(ctx, name, "claude", "")
		require.NoError(t, err)
		assert.Nil(t, pane, "bare name %q", name)
	}
	pane, err := c.FindPane(ctx, "Backend Dev", "claude", sidA)
	require.NoError(t, err)
	require.NotNil(t, pane)
	assert.Equal(t, nameA, pane.SessionName, "by session id")
	pane, err = c.FindPane(ctx, nameA, "", "")
	require.NoError(t, err)
	require.NotNil(t, pane)
	assert.Equal(t, nameA, pane.SessionName, "by exact session name")

	// A is gone: B's title names it, and still nothing is found.
	require.NoError(t, c.KillSessionOnly(ctx, nameA, "claude", sidA))
	for _, args := range [][2]string{{nameA, ""}, {"Backend Dev", sidA}, {"Backend Dev", ""}} {
		pane, err := c.FindPane(ctx, args[0], "claude", args[1])
		require.NoError(t, err)
		assert.Nil(t, pane, "%q / %q after A ended", args[0], args[1])
	}
	out, _ := c.CapturePane(ctx, "Backend Dev", 10, "claude", "")
	assert.Empty(t, out, "nothing of B's pane is captured by a bare name")
}
