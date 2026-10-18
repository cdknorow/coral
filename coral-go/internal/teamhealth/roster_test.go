package teamhealth

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func rosterNow() time.Time { return time.Date(2026, 10, 18, 8, 5, 0, 0, time.UTC) }

func counts(working, waiting, prompt, idle, asleep, offline int) map[string]int {
	return map[string]int{StateWorking: working, StateWaiting: waiting, StatePrompt: prompt, StateIdle: idle, StateAsleep: asleep, StateOffline: offline}
}

func min10(n int) time.Duration { return time.Duration(n) * time.Minute }

// coral-hub's example: two working, eight waiting (six on the recipient, two on Security), four asleep one of them with tagged
// messages unread, and a stale wait.
func TestRosterCheckLooksLikeTheExample(t *testing.T) {
	r := &Roster{Recipient: "Orchestrator", Counts: counts(2, 8, 0, 0, 4, 0), Claimable: 0,
		Waits: []WaitGroup{
			{On: "Security Reviewer", Waiters: []WaitEntry{{Name: "Frontend Dev", Age: min10(37)}, {Name: "Lead Developer", Age: min10(17), Stale: true}}},
			{On: "Orchestrator", You: true, Waiters: []WaitEntry{{Name: "QA Engineer", Age: min10(12)}, {Name: "DevOps Engineer", Age: min10(19)},
				{Name: "UX Engineer", Age: min10(8)}, {Name: "Backend Dev", Age: min10(7)}, {Name: "Doc Writer", Age: min10(6)}, {Name: "Tester", Age: min10(5)}}},
		},
		Asleep: []AsleepUnread{{Name: "Digital Ocean Expert", Count: 2, Since: time.Date(2026, 10, 18, 7, 23, 0, 0, time.UTC)}},
	}
	in := CheckInput{Now: rosterNow(), Agents: 14, Roster: r, Progress: Progress{Since: rosterNow().Add(-10 * time.Minute), Posts: 55}}
	lines := ComposeLines(in)
	assert.Equal(t, "[Coral health check] 08:05Z · 2 working · 8 WAITING · 4 asleep · 0 claimable tasks", lines[0])
	assert.Equal(t, "Waiting on YOU (Orchestrator), 6: DevOps Engineer 19m, QA Engineer 12m, UX Engineer 8m, Backend Dev 7m, Doc Writer 6m, Tester 5m", lines[1], "the recipient's group first, the longest wait first")
	assert.Equal(t, "Waiting on Security Reviewer, 2: Frontend Dev 37m, Lead Developer 17m (STALE: Security Reviewer has posted a message mentioning them since the wait started)", lines[2])
	assert.Equal(t, "Asleep with unread tagged messages: Digital Ocean Expert (2 tagged since 07:23)", lines[3])
	assert.Equal(t, "Since 07:55: 55 posts.", lines[4], "post volume is a count, never 'progressing'")
	assert.Len(t, lines, 5)
	out := Compose(in)
	assert.NotContains(t, out, "\n", "typed as one paste")
	assert.NotContains(t, strings.ToLower(out), "progressing")
	assert.True(t, strings.HasPrefix(out, lines[0]+" • "+lines[1]), out)
	assert.Equal(t, lines[0]+"\n- "+lines[1], strings.SplitN(FormatList(lines), "\n- "+lines[2], 2)[0])
}

// Rule 1: "progressing" is never said from post volume; with everyone awake working the check says so, and nothing else.
func TestRosterNeverSaysProgressingFromPosts(t *testing.T) {
	in := CheckInput{Now: rosterNow(), Roster: &Roster{Recipient: "Orchestrator", Counts: counts(3, 0, 0, 0, 1, 0)}, Progress: Progress{Since: rosterNow().Add(-10 * time.Minute), Posts: 40, Completed: 2}}
	lines := ComposeLines(in)
	assert.Equal(t, "[Coral health check] 08:05Z · 3 working · 0 WAITING · 1 asleep · 0 claimable tasks", lines[0])
	assert.Contains(t, strings.Join(lines, "\n"), "All 3 awake agents are working. Nothing needs you.")
	assert.Contains(t, strings.Join(lines, "\n"), "Since 07:55: 2 tasks completed, 40 posts.")
	// one idle agent breaks it
	in.Roster.Counts = counts(2, 0, 0, 1, 0, 0)
	text := strings.Join(ComposeLines(in), "\n")
	assert.NotContains(t, text, "are working")
	assert.NotContains(t, text, "Nothing needs you")
	assert.Contains(t, text, "1 idle")
	// a prompt too, and an offline agent is counted
	in.Roster.Counts = counts(2, 0, 1, 0, 0, 2)
	assert.Contains(t, ComposeLines(in)[0], "1 at a prompt")
	assert.Contains(t, ComposeLines(in)[0], "2 offline")
}

func TestRosterAllClear(t *testing.T) {
	in := CheckInput{Now: rosterNow(), Roster: &Roster{Recipient: "Orchestrator", Counts: counts(0, 0, 0, 0, 2, 0)}, Progress: Progress{Since: rosterNow().Add(-10 * time.Minute)}}
	lines := ComposeLines(in)
	assert.Equal(t, "No progress since the last check (07:55).", lines[1])
	assert.Equal(t, "Nothing needs you.", lines[2])
	// with an item, an operator question and parked tasks the old parts are still there
	in.Items = []Item{{Rank: 1, Agent: "Dev", Problem: "silent on #12 for 34 min since Coral asked for a status", Suggest: "check on it", Key: "k1", State: "s"}}
	in.Questions = []Question{{Asker: "Orchestrator", Text: ShownText("ship it?", nil), Since: rosterNow().Add(-50 * time.Minute)}}
	in.Parked = 2
	text := strings.Join(ComposeLines(in), "\n")
	assert.Contains(t, text, "Needs you, 1: 1) Dev: silent on #12")
	assert.Contains(t, text, "[ack "+ItemID("k1")+"]", "the ack mechanism is kept")
	assert.Contains(t, text, "Waiting on the operator: Orchestrator asked 50 min ago")
	assert.Contains(t, text, "(2 parked)")
	assert.NotContains(t, text, "Nothing needs you")
}

// A wait group lists at most eight and counts the rest; the whole check stays within the cap.
func TestRosterStaysWithinTheCap(t *testing.T) {
	var many []WaitEntry
	for i := 0; i < 60; i++ {
		many = append(many, WaitEntry{Name: fmt.Sprintf("Agent %02d", i), Age: min10(i)})
	}
	var asleep []AsleepUnread
	for i := 0; i < 40; i++ {
		asleep = append(asleep, AsleepUnread{Name: fmt.Sprintf("Sleeper %02d", i), Count: 3, Since: rosterNow().Add(-time.Duration(i) * time.Hour)})
	}
	var items []Item
	for i := 0; i < 200; i++ {
		items = append(items, Item{Rank: i, Agent: fmt.Sprintf("Agent %d", i), Problem: "idle " + strings.Repeat("x", 60)})
	}
	in := CheckInput{Now: rosterNow(), Items: items, Progress: Progress{Since: rosterNow()},
		Roster: &Roster{Recipient: "Orchestrator", Counts: counts(0, 60, 0, 0, 40, 0),
			Waits: []WaitGroup{{On: "Orchestrator", You: true, Waiters: many}}, Asleep: asleep}}
	out := Compose(in)
	assert.LessOrEqual(t, len(out), MaxCheck)
	assert.Contains(t, out, "[Coral health check] 08:05Z · 0 working · 60 WAITING")
	assert.Contains(t, out, "…+52 more")
	assert.Contains(t, out, "Waiting on YOU (Orchestrator), 60:", "the recipient's group is never the part left out")
	assert.Contains(t, out, "lines not shown")
}

func TestRosterGroupOrder(t *testing.T) {
	r := &Roster{Recipient: "Orch", Counts: counts(0, 6, 0, 0, 0, 0), Waits: []WaitGroup{
		{On: "Zed", Waiters: []WaitEntry{{Name: "a", Age: min10(1)}}},
		{On: "Bob", Waiters: []WaitEntry{{Name: "b", Age: min10(2)}, {Name: "c", Age: min10(3)}}},
		{On: "Amy", Waiters: []WaitEntry{{Name: "d", Age: min10(4)}}},
		{On: "Orch", You: true, Waiters: []WaitEntry{{Name: "e", Age: min10(1)}}},
		{On: "the Operator", Waiters: []WaitEntry{{Name: "f", Age: min10(9)}}},
	}}
	lines := r.waitLines()
	var ons []string
	for _, l := range lines {
		ons = append(ons, strings.SplitN(strings.TrimPrefix(l, "Waiting on "), ",", 2)[0])
	}
	assert.Equal(t, []string{"YOU (Orch)", "Bob", "Amy", "Zed", "the Operator"}, ons, "YOU first, then the larger group, then by name")
	assert.Equal(t, "1h05m", shortAge(65*time.Minute))
	assert.Equal(t, "0m", shortAge(-time.Minute))
}
