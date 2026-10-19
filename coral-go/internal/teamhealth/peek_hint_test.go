package teamhealth

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const peekPrefix = "To see why, read their terminal: coral-board peek "

func peekLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, peekPrefix) {
			out = append(out, l)
		}
	}
	return out
}

func waitingRoster() *Roster {
	return &Roster{Recipient: "Orchestrator", Counts: counts(1, 3, 0, 0, 0, 0),
		Waits: []WaitGroup{
			{On: "Security Reviewer", Waiters: []WaitEntry{{Name: "Frontend Dev", Age: min10(37)}, {Name: "Lead Developer", Age: min10(17)}}},
			{On: "Orchestrator", You: true, Waiters: []WaitEntry{{Name: "QA Engineer", Age: min10(12)}, {Name: "DevOps Engineer", Age: min10(19)}}},
		}}
}

// #4983, the operator: the check reminds the orchestrator that coral-board peek shows an agent's terminal, so it looks instead of guessing.
func TestPeekHintNamesTheFirstAgentToLookAt(t *testing.T) {
	in := CheckInput{Now: rosterNow(), Roster: waitingRoster(), PeekHint: true, Progress: Progress{Since: rosterNow().Add(-10 * time.Minute), Posts: 4}}
	lines := ComposeLines(in)
	hint := peekLines(lines)
	require.Len(t, hint, 1)
	assert.Equal(t, `To see why, read their terminal: coral-board peek "DevOps Engineer" --lines 30.`, hint[0], "the recipient's group first, its longest waiter")
	// Before the progress line, after what needs the reader.
	assert.Equal(t, hint[0], lines[len(lines)-2])
	assert.True(t, strings.HasPrefix(lines[len(lines)-1], "Since "), lines[len(lines)-1])
	assert.Contains(t, Compose(in), "coral-board peek", "and in the line that is typed")
	assert.NotContains(t, Compose(in), "\n")
}

func TestPeekHintPrefersARankedItemsAgent(t *testing.T) {
	in := CheckInput{Now: rosterNow(), Roster: waitingRoster(), PeekHint: true, Progress: Progress{Since: rosterNow()},
		Items: []Item{{Rank: 4, Agent: "Board", Problem: "queue"}, {Rank: 2, Agent: "Backend Dev", Problem: "silent on #12"}, {Rank: 3, Agent: "Tester", Problem: "idle"}}}
	hint := peekLines(ComposeLines(in))
	require.Len(t, hint, 1)
	assert.Contains(t, hint[0], `peek "Backend Dev"`, "the first by rank, and not the Board's queue problem")
}

func TestPeekHintForAnIdleOrStuckAgentWithoutAName(t *testing.T) {
	for name, r := range map[string]*Roster{
		"idle":   {Recipient: "O", Counts: counts(1, 0, 0, 1, 0, 0)},
		"prompt": {Recipient: "O", Counts: counts(1, 0, 1, 0, 0, 0)},
	} {
		hint := peekLines(ComposeLines(CheckInput{Now: rosterNow(), Roster: r, PeekHint: true, Progress: Progress{Since: rosterNow()}}))
		require.Len(t, hint, 1, name)
		assert.Equal(t, `To see why, read their terminal: coral-board peek "<agent>" --lines 30.`, hint[0], name)
	}
}

func TestPeekHintIsLeftOutWhenThereIsNothingToLookAt(t *testing.T) {
	for name, in := range map[string]CheckInput{
		"everyone working":     {Roster: &Roster{Recipient: "O", Counts: counts(3, 0, 0, 0, 0, 0)}},
		"only sleepers":        {Roster: &Roster{Recipient: "O", Counts: counts(1, 0, 0, 0, 2, 0), Asleep: []AsleepUnread{{Name: "Sleeper", Count: 1, Since: rosterNow()}}}},
		"not the orchestrator": {Roster: waitingRoster()}, // PeekHint false: the check goes to someone else
	} {
		in.Now, in.Progress, in.PeekHint = rosterNow(), Progress{Since: rosterNow()}, name != "not the orchestrator"
		assert.Empty(t, peekLines(ComposeLines(in)), name)
	}
	// No roster: the earlier check is unchanged.
	assert.NotContains(t, Compose(CheckInput{Now: rosterNow(), Agents: 2, PeekHint: true}), "peek")
}

// A member's name is typed into an example command: a quote or a backslash in it is left out.
func TestPeekHintNameCannotBreakOutOfItsQuotes(t *testing.T) {
	r := &Roster{Recipient: "O", Counts: counts(0, 1, 0, 0, 0, 0), Waits: []WaitGroup{{On: "O", You: true, Waiters: []WaitEntry{{Name: `Evil" ; rm -rf x \`, Age: min10(5)}}}}}
	hint := peekLines(ComposeLines(CheckInput{Now: rosterNow(), Roster: r, PeekHint: true, Progress: Progress{Since: rosterNow()}}))
	require.Len(t, hint, 1)
	assert.Equal(t, 2, strings.Count(hint[0], `"`), "only the example's own two quotes: "+hint[0])
	assert.NotContains(t, hint[0], `\`)
}

// The hint is a pointer, not a fact: when the check is already near its cap it is the first thing left out, and the rest is as it was.
func TestPeekHintIsTheFirstLeftOutWhenTheCheckIsFull(t *testing.T) {
	build := func(pad int, hint bool) []string {
		return ComposeLines(CheckInput{Now: rosterNow(), Roster: waitingRoster(), PeekHint: hint, Progress: Progress{Since: rosterNow()},
			Items: []Item{{Rank: 1, Agent: "Backend Dev", Problem: strings.Repeat("x", pad)}}})
	}
	joined := func(ls []string) int { return len(strings.Join(ls, " • ")) }
	pad := 0
	for joined(build(pad, false)) < MaxCheck-30 {
		pad += 5
	}
	base := build(pad, false)
	require.LessOrEqual(t, joined(base), MaxCheck)
	require.Greater(t, joined(base)+len(peekPrefix)+30, MaxCheck, "the fixture is full enough that the hint cannot fit")
	assert.Equal(t, base, build(pad, true), "no hint, and nothing else lost")
	assert.Empty(t, peekLines(build(pad, true)))
	assert.NotEmpty(t, peekLines(build(0, true)), "with room it is there")
}
