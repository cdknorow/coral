package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/terminalinput"
)

// #3211: an agent whose own pane is gone must never reach another agent's pane. Before the
// fix FindPane fell back to a fuzzy name match (folder, title or session-name substring), so
// input, prompt answers, kills and reads for one agent landed on a teammate.
const (
	goneSID    = "11111111-2222-4333-8444-555555555555"
	siblingSID = "99999999-8888-4777-8666-555555555555"
)

func newSiblingOnly(t *testing.T) (*Client, string) {
	t.Helper()
	c := newTestClient(t)
	ctx := context.Background()
	// Both agents work in the same folder ("team"); only the sibling's pane exists.
	dir := filepath.Join(t.TempDir(), "team")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	sibling := "claude-" + siblingSID
	createTestSession(t, c, sibling, dir)
	c.SetPaneTitle(ctx, sibling+":0.0", "team claude") // the title the fuzzy match reads
	return c, sibling
}

func siblingScreen(t *testing.T, c *Client) string {
	t.Helper()
	text, err := c.CapturePane(context.Background(), "claude-"+siblingSID, 200, "claude", siblingSID)
	require.NoError(t, err)
	return text
}

func TestMissingSessionPaneNeverReachesASibling(t *testing.T) {
	restore := SetSendKeysSettleForTest(time.Millisecond)
	defer restore()
	c, sibling := newSiblingOnly(t)
	ctx := context.Background()

	// (a) the same folder, (b) a prefix of the sibling's names, (c) a short substring of them,
	// and the gone agent's own exact session name.
	for _, name := range []string{"team", "claude", "te", "claude-" + goneSID} {
		t.Run(name, func(t *testing.T) {
			for _, sid := range []string{goneSID, ""} {
				if sid == "" && name != "claude-"+goneSID {
					continue // a bare name without a sid is the legacy fuzzy path, below
				}
				pane, err := c.FindPane(ctx, name, "claude", sid)
				require.NoError(t, err)
				assert.Nil(t, pane, "FindPane(%q, sid %q) resolved to %v", name, sid, pane)

				marker := "GONE_" + strings.ReplaceAll(name, "-", "_")
				err = c.SendKeys(ctx, terminalinput.Unclassified, name, "echo "+marker+"_keys", "claude", sid)
				assert.True(t, errors.Is(err, ErrTerminalGone), "SendKeys: %v", err)
				err = c.SendPrompt(ctx, terminalinput.Unclassified, name, "echo "+marker+"_prompt", "claude", sid)
				assert.True(t, errors.Is(err, ErrTerminalGone), "SendPrompt: %v", err)
				err = c.SendRawKeys(ctx, terminalinput.Unclassified, name, []string{"ZQXRAW", "Enter"}, "claude", sid)
				assert.True(t, errors.Is(err, ErrTerminalGone), "SendRawKeys: %v", err)
				err = c.ResizePane(ctx, name, 33, "claude", sid)
				assert.True(t, errors.Is(err, ErrTerminalGone), "ResizePane: %v", err)

				text, err := c.CapturePane(ctx, name, 50, "claude", sid)
				assert.NoError(t, err)
				assert.Empty(t, text, "a read of the gone pane returned another pane's screen")

				err = c.KillSessionOnly(ctx, name, "claude", sid)
				assert.True(t, errors.Is(err, ErrTerminalGone), "KillSessionOnly: %v", err)
				err = c.KillSession(ctx, name, "claude", sid)
				assert.True(t, errors.Is(err, ErrTerminalGone), "KillSession: %v", err)

				require.True(t, c.HasSession(ctx, sibling), "the sibling was killed")
				time.Sleep(150 * time.Millisecond) // sleep-ok: let any stray keystroke render before reading
				screen := siblingScreen(t, c)
				assert.NotContains(t, screen, marker, "input for the gone agent reached the sibling")
				assert.NotContains(t, screen, "ZQXRAW", "raw keys for the gone agent reached the sibling")
			}
		})
	}
}

// Without a session id there is no name match at all any more (#3687): the folder and the
// title the sibling shares with "team" find nothing.
func TestNoNameMatchWithoutASessionID(t *testing.T) {
	c, _ := newSiblingOnly(t)
	for _, name := range []string{"team", "Team", "claude", "team claude"} {
		pane, err := c.FindPane(context.Background(), name, "claude", "")
		require.NoError(t, err)
		assert.Nil(t, pane, name)
	}
}

// The agent's own pane is still found by its session id, whatever name the caller passes.
func TestSessionIDFindsItsOwnPane(t *testing.T) {
	c, sibling := newSiblingOnly(t)
	ctx := context.Background()
	for _, name := range []string{"team", "anything", sibling} {
		pane, err := c.FindPane(ctx, name, "claude", siblingSID)
		require.NoError(t, err)
		require.NotNil(t, pane, name)
		assert.Equal(t, sibling, pane.SessionName)
	}
	// An exact session name without a sid names that session too.
	pane, err := c.FindPane(ctx, sibling, "", "")
	require.NoError(t, err)
	require.NotNil(t, pane)
	assert.Equal(t, sibling, pane.SessionName)
}
