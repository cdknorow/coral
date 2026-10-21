package background

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/naming"
)

// Case C10 of Security's #4180 fixture (#4176 rule 10): a dialog is up on Dev's terminal (its
// screen holds a password). Nothing is typed into that terminal; after 10 minutes the Operator
// is told once, on the board, with Dev's name and "waiting at a dialog" only; a later hold is a
// new notice; the team's settings turn it off or change the minutes.
func TestHeldAtADialogTellsTheOperatorOnce(t *testing.T) {
	n := newNudgeHarness(t, "")
	ctx := context.Background()
	dev := "99999999-9999-9999-9999-999999999999"
	n.agent("Dev", dev)
	session := naming.SessionName("claude", dev)
	held := map[string]time.Time{session: n.now.Add(-4 * time.Minute)} // e31: 4 minutes ago
	n.h.heldDialogs = func() map[string]time.Time { return held }
	n.sig[dev] = agentSignal{NeedsInput: true, Since: n.now.Add(-4 * time.Minute)}
	notices := func() []string {
		var out []string
		for _, l := range n.lines() {
			if strings.Contains(l, "waiting at a dialog") {
				out = append(out, l)
			}
		}
		return out
	}

	n.run(0)
	assert.Empty(t, notices(), "4 minutes: not yet")
	n.run(6 * time.Minute)
	require.Len(t, notices(), 1, "10 minutes: told")
	line := notices()[0]
	assert.Equal(t, "@Operator [Coral health] waiting at a dialog: Dev (10 min). Coral's messages to them are held until someone answers the dialog at their terminal.", line)
	for _, screen := range []string{"Password", "hunter2", "deploy@host", "Allow once"} {
		assert.NotContains(t, line, screen, "no screen text")
	}
	n.run(5 * time.Minute)
	n.run(30 * time.Minute)
	assert.Len(t, notices(), 1, "once per hold")
	assert.Empty(t, n.sentTo("Dev"), "nothing typed into the terminal at a dialog")

	// The dialog was answered, and later another one holds it: a new notice after its 10 minutes.
	held[session] = n.now.Add(-time.Minute)
	n.run(5 * time.Minute)
	assert.Len(t, notices(), 1)
	n.run(5 * time.Minute)
	assert.Len(t, notices(), 2, "a new hold is a new notice")

	// The team's settings: off, or later.
	hs := healthOn()
	hs.HeldNotice.On = false
	_, err := n.bs.UpdateBoardSettings(ctx, "team", board.BoardSettingsUpdate{Health: &hs})
	require.NoError(t, err)
	held[session] = n.now.Add(-time.Hour)
	n.run(time.Minute)
	assert.Len(t, notices(), 2, "off")
	hs.HeldNotice = board.HealthMentionNudge{On: true, AfterMin: 120}
	_, err = n.bs.UpdateBoardSettings(ctx, "team", board.BoardSettingsUpdate{Health: &hs})
	require.NoError(t, err)
	n.run(time.Minute)
	assert.Len(t, notices(), 2, "120 minutes not reached")
	n.run(time.Hour)
	assert.Len(t, notices(), 3)
}

// The notice's setting: on, 10 minutes by default; clamped to 5..1440 like the others.
func TestHeldNoticeSettingDefaultsAndClamp(t *testing.T) {
	d := healthOn()
	assert.Equal(t, board.HealthMentionNudge{On: true, AfterMin: 10}, d.HeldNotice)
	d.HeldNotice.AfterMin = 1
	assert.Equal(t, 5, d.Clamp().HeldNotice.AfterMin)
	d.HeldNotice.AfterMin = 99999
	assert.Equal(t, 1440, d.Clamp().HeldNotice.AfterMin)
}
