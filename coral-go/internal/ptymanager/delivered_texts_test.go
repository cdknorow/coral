package ptymanager

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const taskNotice = "[Task #4300 completed by QA Engineer] Outcome: success. Live smoke of the local-auth chain: all 4 cases pass."

// A text the gate delivered is recognised in the transcript's own form: Claude Code wraps a pasted text in pasted_content tags and
// the text may be re-wrapped; what the operator typed is not recognised (#4306).
func TestDeliveredTextIsMatchedAsTheTranscriptRecordsIt(t *testing.T) {
	name := "claude-delivered-match"
	noteDeliveredText(name+".0", taskNotice) // a pane target names the session

	match := DeliveredMatcher(name)
	require.NotNil(t, match)
	assert.True(t, match(taskNotice))
	assert.True(t, match("<pasted_content id=\"1\">\n"+taskNotice+"\n</pasted_content id=\"1\">"), "the paste wrapper is not part of the text")
	assert.True(t, match("/plan "+taskNotice), "a CLI agent's own slash prefix is not either")
	assert.True(t, match(strings.ReplaceAll(taskNotice, " ", "  \n")), "whitespace is collapsed")
	assert.False(t, match("[Task #4300 completed by QA Engineer] Outcome: failure."), "a different text with the same shape")
	assert.False(t, match("please look at "+taskNotice), "the operator quoting a notice")
	assert.False(t, match(""))
	assert.Nil(t, DeliveredMatcher("claude-delivered-never"), "a session Coral typed nothing into")
}

// Only a text that was actually typed is recorded: through the gate, held while a prompt is open, then once it is delivered.
func TestOnlyDeliveredSendsAreRecorded(t *testing.T) {
	fastGate(t)
	ctx := context.Background()
	a := &fakeAgent{screen: permissionMenu}
	name := "claude-delivered-gate"
	in := a.input(name, "", taskNotice, false)
	d, err := DeliverAutomated(ctx, in)
	require.NoError(t, err)
	require.Equal(t, Held, d)
	assert.Nil(t, DeliveredMatcher(name), "held behind a menu: not typed, not recorded")

	a.show(idleInputBox)
	require.Eventually(t, func() bool { return len(a.sent()) == 1 }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { m := DeliveredMatcher(name); return m != nil && m(taskNotice) }, time.Second, 10*time.Millisecond, "recorded when the held send is delivered")

	d, err = DeliverAutomated(ctx, a.input(name, "", "You have 1 unread message on the message board.", false))
	require.NoError(t, err)
	require.Equal(t, Sent, d)
	assert.True(t, DeliveredMatcher(name)("You have 1 unread message on the message board."), "recorded at once when typed at once")
}

// Memory stays bounded: a session remembers its last deliveredTextsPerSession texts, and the number of sessions is capped.
func TestDeliveredTextsAreBounded(t *testing.T) {
	name := "claude-delivered-ring"
	for i := 0; i < deliveredTextsPerSession+10; i++ {
		noteDeliveredText(name, fmt.Sprintf("notice number %d", i))
	}
	match := DeliveredMatcher(name)
	assert.False(t, match("notice number 0"), "the oldest fell out of the ring")
	assert.True(t, match(fmt.Sprintf("notice number %d", deliveredTextsPerSession+9)))

	for i := 0; i < deliveredTextSessions+5; i++ {
		noteDeliveredText(fmt.Sprintf("claude-delivered-cap-%d", i), "x notice")
	}
	delivered.Lock()
	n := len(delivered.rings)
	delivered.Unlock()
	assert.LessOrEqual(t, n, deliveredTextSessions)
}
