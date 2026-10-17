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

// turns flags a list of user turn texts the way the chat reader does, with no timestamps.
func turns(name string, texts ...string) []bool {
	return FlagDelivered(texts, make([]time.Time, len(texts)), name)
}

// A text the gate delivered is recognised in the transcript's own form: Claude Code wraps a pasted text in pasted_content tags and
// the whitespace may differ; what the operator typed is not recognised (#4306).
func TestDeliveredTextIsMatchedAsTheTranscriptRecordsIt(t *testing.T) {
	name := "claude-delivered-match"
	noteDeliveredText(name+".0", taskNotice) // a pane target names the session

	assert.Equal(t, []bool{true}, turns(name, taskNotice))
	assert.Equal(t, []bool{false, true}, turns(name, "please look at "+taskNotice, "<pasted_content id=\"1\">\n"+taskNotice+"\n</pasted_content id=\"1\">"),
		"the paste wrapper is not part of the text, and the operator quoting a notice is not it")
	noteDeliveredText(name, taskNotice)
	assert.Equal(t, []bool{true, false}, turns(name, strings.ReplaceAll(taskNotice, " ", "  \n"), "[Task #4300 completed by QA Engineer] Outcome: failure. Live smoke of the local-auth chain."), "whitespace is collapsed; a different text is not it")
	assert.Nil(t, turns("claude-delivered-never", taskNotice), "a session Coral typed nothing into")
}

// The whole text must match: a slash command in front is part of the text, so "/status check the queue" is not "check the queue" (#4306 follow-up).
func TestDeliveredTextMatchesTheWholeText(t *testing.T) {
	name := "claude-delivered-whole"
	body := "check the queue and report every task that is waiting for a reviewer"
	noteDeliveredText(name, body)
	assert.Equal(t, []bool{false}, turns(name, "/status "+body), "a slash command makes it a different text")
	noteDeliveredText(name, "/status "+body)
	assert.Equal(t, []bool{false, true}, turns(name, "/plan "+body, "/status "+body), "and the other way round")
}

// A short delivered text ("continue") is never recorded or matched: the operator's identical message stays theirs.
func TestShortDeliveredTextsAreNeverMatched(t *testing.T) {
	name := "claude-delivered-short"
	noteDeliveredText(name, "continue")
	noteDeliveredText(name, strings.Repeat("x", minDeliveredLen-1))
	assert.False(t, HasDeliveredTexts(name), "nothing under the floor is recorded")
	assert.Nil(t, turns(name, "continue", strings.Repeat("x", minDeliveredLen-1)))

	noteDeliveredText(name, strings.Repeat("x", minDeliveredLen)) // exactly the floor is recorded
	assert.Equal(t, []bool{false, true}, turns(name, "continue", strings.Repeat("x", minDeliveredLen)))
}

// Each delivery flags at most one turn: an operator who repeats a long text they saw keeps their turn (#4306 follow-up).
func TestEachDeliveryFlagsOneTurn(t *testing.T) {
	name := "claude-delivered-countdown"
	noteDeliveredText(name, taskNotice)
	assert.Equal(t, []bool{true, false, false}, turns(name, taskNotice, taskNotice, taskNotice), "one delivery, three identical turns: the first only")
	noteDeliveredText(name, taskNotice)
	assert.Equal(t, []bool{true, true, false}, turns(name, taskNotice, taskNotice, taskNotice), "two deliveries, two turns")

	// Typed by the operator before Coral delivered it: that turn is theirs, and the delivered one is still found.
	other := "[Task #77 approved by Security Reviewer] Approved with one comment."
	before := time.Now().Add(-time.Hour)
	noteDeliveredText(name, other)
	got := FlagDelivered([]string{other, other}, []time.Time{before, time.Now().Add(time.Second)}, name)
	assert.Equal(t, []bool{false, true}, got, "a turn stamped before the delivery is the operator's")
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
	assert.False(t, HasDeliveredTexts(name), "held behind a menu: not typed, not recorded")

	a.show(idleInputBox)
	require.Eventually(t, func() bool { return len(a.sent()) == 1 }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return HasDeliveredTexts(name) }, time.Second, 10*time.Millisecond, "recorded when the held send is delivered")
	assert.Equal(t, []bool{true}, turns(name, taskNotice))

	next := "You have 1 unread message on the message board. Run 'coral-board read' to see it."
	d, err = DeliverAutomated(ctx, a.input(name, "", next, false))
	require.NoError(t, err)
	require.Equal(t, Sent, d)
	assert.Equal(t, []bool{true}, turns(name, next), "recorded at once when typed at once")
}

// Memory stays bounded: a session remembers its last deliveredTextsPerSession texts, and the number of sessions is capped.
func TestDeliveredTextsAreBounded(t *testing.T) {
	name := "claude-delivered-ring"
	text := func(i int) string { return fmt.Sprintf("a notice that is long enough to be recorded, number %d", i) }
	for i := 0; i < deliveredTextsPerSession+10; i++ {
		noteDeliveredText(name, text(i))
	}
	got := turns(name, text(0), text(deliveredTextsPerSession+9))
	assert.Equal(t, []bool{false, true}, got, "the oldest fell out of the ring")

	for i := 0; i < deliveredTextSessions+5; i++ {
		noteDeliveredText(fmt.Sprintf("claude-delivered-cap-%d", i), text(i))
	}
	delivered.Lock()
	n := len(delivered.rings)
	delivered.Unlock()
	assert.LessOrEqual(t, n, deliveredTextSessions)
}
