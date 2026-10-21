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

// Turns stamped before the oldest remembered delivery are not hashed (a long transcript costs what its recent turns cost); the flags are
// the same as when every turn is hashed, and an unstamped turn is still checked (#4306 follow-up, Frontend's cost review).
func TestOldTurnsAreNotHashed(t *testing.T) {
	name := "claude-delivered-cutoff"
	now := time.Now()
	notice := "[Task #4300 completed by QA Engineer] Outcome: success, the cutoff test notice."
	noteDeliveredText(name, notice)

	var texts []string
	var stamps []time.Time
	for i := 0; i < 1000; i++ { // an hour-old history: none of it can be the delivery
		texts = append(texts, fmt.Sprintf("an older operator message that is long enough to be hashed, number %d", i))
		stamps = append(stamps, now.Add(-time.Hour+time.Duration(i)*time.Second))
	}
	texts = append(texts, notice, "an unstamped turn that is long enough to be hashed, so it is checked", notice)
	stamps = append(stamps, now.Add(time.Second), time.Time{}, now.Add(-time.Hour)) // the delivery's turn, an unstamped one, an old identical text

	before := flagHashed.Load()
	got := FlagDelivered(texts, stamps, name)
	hashed := flagHashed.Load() - before
	assert.EqualValues(t, 2, hashed, "only the delivery's turn and the unstamped one were hashed, not the 1000 old turns or the old identical text")
	want := make([]bool, len(texts))
	want[1000] = true
	assert.Equal(t, want, got, "the flags are those of hashing every turn")

	// The same turns with no stamps at all: every one is hashed, the first identical one is the delivery's.
	before = flagHashed.Load()
	got = FlagDelivered(texts, make([]time.Time, len(texts)), name)
	assert.EqualValues(t, len(texts), flagHashed.Load()-before, "without stamps nothing can be skipped")
	assert.True(t, got[1000], "and the delivery is still found")
	assert.False(t, got[1002], "its repeat is not")
}

// BenchmarkFlagDelivered times a 5,000-turn transcript of 2 KB user turns, the whole of it hashed (no stamps) and with stamps (only the last
// minutes are).
func BenchmarkFlagDelivered(b *testing.B) {
	name := "claude-delivered-bench"
	noteDeliveredText(name, "[Task #1 completed by QA Engineer] Outcome: success, the benchmark notice text.")
	now := time.Now()
	body := strings.Repeat("a user message of about two kilobytes, ", 52)
	texts := make([]string, 5000)
	stamps := make([]time.Time, 5000)
	for i := range texts {
		texts[i] = fmt.Sprintf("%d %s", i, body)
		stamps[i] = now.Add(-time.Duration(5000-i) * time.Minute)
	}
	stamps[4999] = now
	b.Run("unstamped", func(b *testing.B) {
		none := make([]time.Time, len(texts))
		for i := 0; i < b.N; i++ {
			FlagDelivered(texts, none, name)
		}
	})
	b.Run("stamped", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			FlagDelivered(texts, stamps, name)
		}
	})
}

// What the Operator sent from the dashboard is remembered apart from what Coral delivered (#5482): any length, whole text, one turn each, and never mistaken for a delivery.
func TestOperatorSentTextsAreRememberedApartFromDeliveries(t *testing.T) {
	name := "claude-operator-sent"
	short := "[Coral] hi" // far under the delivered floor: a person's text that looks like a notice must still be theirs
	name2 := "claude-operator-sent-stamp"
	forget := func() { forgetOperatorTexts(name); forgetOperatorTexts(name2) }
	t.Cleanup(forget) // the tables are package-level: a second run of this test (-count=2) starts clean (#5677)
	forget()
	require.False(t, HasOperatorTexts(name))
	NoteOperatorText(name+".0", short) // a pane target names the session
	NoteOperatorText(name, "Stop hook feedback: typed by me")
	require.True(t, HasOperatorTexts(name))
	flags := func(texts ...string) []bool { return FlagOperatorSent(texts, make([]time.Time, len(texts)), name) }
	assert.Equal(t, []bool{true, false, true}, flags(short, "[Coral] hi there", "<pasted_content id=\"1\">Stop hook feedback:  typed by me</pasted_content id=\"1\">"),
		"the whole text matches, the paste wrapper and spacing do not count")
	assert.Equal(t, []bool{true, false}, flags(short, short), "each send is one turn")
	assert.Nil(t, FlagOperatorSent([]string{short}, make([]time.Time, 1), "claude-operator-never"), "a session the Operator sent nothing to")
	// the two logs are separate: an operator text is not a delivery, a delivery is not an operator text
	assert.Nil(t, turns(name, short), "Coral delivered nothing to this session")
	noteDeliveredText(name, taskNotice)
	assert.Equal(t, []bool{false}, flags(taskNotice))
	// a turn stamped before the send is not it (the Operator typed it earlier, or it is somebody else's)
	NoteOperatorText(name2, short)
	before := time.Now().Add(-time.Minute)
	assert.Equal(t, []bool{false}, FlagOperatorSent([]string{short}, []time.Time{before}, name2))
	assert.Equal(t, []bool{true}, FlagOperatorSent([]string{short}, []time.Time{time.Now()}, name2))
}

// forgetOperatorTexts drops what was noted for the session from the package-level tables of what the Operator sent and what Coral delivered (an emptied ring stays in the table, and a session with a
// ring answers "nothing delivered" differently from one without), so a test leaves nothing for the next run of itself.
func forgetOperatorTexts(name string) {
	for _, l := range []*textLog{operatorSent, delivered} {
		l.Lock()
		delete(l.rings, sessionOfTarget(name))
		l.Unlock()
	}
}
