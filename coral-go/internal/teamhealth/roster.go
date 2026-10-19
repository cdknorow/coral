package teamhealth

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The roster part of the check (#4583): what each agent is really doing, who waits on whom, and who sleeps with tagged messages
// unread. The old check said "All 10 agents progressing; 55 posts" while eight agents sat in coral-board wait on the Orchestrator
// or on Security and one slept with tagged messages unread: post volume says nothing about whether anyone is progressing.
// Every field holds member names (through Name), numbers and fixed phrases; agent text never appears here.

// Agent states, from what Coral can see (its terminal, its waits, its session record).
const (
	StateWorking = "working" // in a turn
	StateWaiting = "waiting" // parked in coral-board wait
	StatePrompt  = "prompt"  // stopped at a prompt that needs input
	StateIdle    = "idle"    // awake, in no turn and in no wait
	StateAsleep  = "asleep"  // its session is asleep
	StateOffline = "offline" // it has a session but nothing is running
	StateUnknown = "unknown" // awake, but Coral has no signal of its state (no hook events)
)

// WaitEntry is one agent in a wait group.
type WaitEntry struct {
	Name string        // a member name, through Name
	Age  time.Duration // how long it has waited
	// Stale: the awaited party has posted a message mentioning the waiter since the wait began, yet the waiter has not woken.
	Stale bool
}

// WaitGroup is the agents waiting on one party.
type WaitGroup struct {
	On      string // who they wait on (a member name, "the Operator", "any update", "a commit"...), through Name
	You     bool   // the party is the check's recipient
	Waiters []WaitEntry
}

// AsleepUnread is a sleeping agent with messages tagged to it that it has not read.
type AsleepUnread struct {
	Name  string
	Count int
	Since time.Time // the oldest of them
}

// Roster is everything the check says about the agents.
type Roster struct {
	Recipient string // the recipient's own name, shown as "YOU (<name>)"
	Counts    map[string]int
	Claimable int // pending tasks nobody holds, that can be claimed
	Waits     []WaitGroup
	Asleep    []AsleepUnread
}

// Awake is how many agents are awake (every state but asleep and offline).
func (r *Roster) Awake() int {
	return r.Counts[StateWorking] + r.Counts[StateWaiting] + r.Counts[StatePrompt] + r.Counts[StateIdle] + r.Counts[StateUnknown]
}

// AllWorking: every awake agent is in a turn (and there is at least one): the only case the check says nobody is stuck.
func (r *Roster) AllWorking() bool {
	return r.Awake() > 0 && r.Counts[StateWorking] == r.Awake()
}

// NeedsAttention: something in the roster is for the reader: a wait, a prompt, an idle agent or a sleeper with tags unread.
func (r *Roster) NeedsAttention() bool {
	return len(r.Waits) > 0 || len(r.Asleep) > 0 || r.Counts[StatePrompt] > 0 || r.Counts[StateIdle] > 0 || r.Counts[StateUnknown] > 0
}

// maxWaiters is how many agents one wait group lists before counting the rest.
const maxWaiters = 8

func (r *Roster) headerCounts() string {
	parts := []string{
		fmt.Sprintf("%d working", r.Counts[StateWorking]),
		fmt.Sprintf("%d WAITING", r.Counts[StateWaiting]),
	}
	if n := r.Counts[StatePrompt]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d at a prompt", n))
	}
	if n := r.Counts[StateIdle]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d idle", n))
	}
	if n := r.Counts[StateUnknown]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d of unknown state", n))
	}
	parts = append(parts, fmt.Sprintf("%d asleep", r.Counts[StateAsleep]))
	if n := r.Counts[StateOffline]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d offline", n))
	}
	parts = append(parts, plural(r.Claimable, "claimable task", "claimable tasks"))
	return strings.Join(parts, " · ")
}

// waitLines: the groups, the recipient's first, then the larger, then by name; each group's waiters longest first.
func (r *Roster) waitLines() []string {
	groups := append([]WaitGroup(nil), r.Waits...)
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.You != b.You {
			return a.You
		}
		if len(a.Waiters) != len(b.Waiters) {
			return len(a.Waiters) > len(b.Waiters)
		}
		return a.On < b.On
	})
	var lines []string
	for _, g := range groups {
		ws := append([]WaitEntry(nil), g.Waiters...)
		sort.SliceStable(ws, func(i, j int) bool {
			if ws[i].Age != ws[j].Age {
				return ws[i].Age > ws[j].Age
			}
			return ws[i].Name < ws[j].Name
		})
		var parts []string
		for i, w := range ws {
			if i == maxWaiters {
				parts = append(parts, fmt.Sprintf("…+%d more", len(ws)-maxWaiters))
				break
			}
			p := fmt.Sprintf("%s %s", w.Name, shortAge(w.Age))
			if w.Stale {
				p += fmt.Sprintf(" (STALE: %s has posted a message mentioning them since the wait started)", staleWho(g))
			}
			parts = append(parts, p)
		}
		who := g.On
		if g.You {
			who = "YOU (" + r.Recipient + ")"
		}
		lines = append(lines, fmt.Sprintf("Waiting on %s, %d: %s", who, len(g.Waiters), strings.Join(parts, ", ")))
	}
	return lines
}

func staleWho(g WaitGroup) string {
	if g.You {
		return "you"
	}
	return g.On
}

func (r *Roster) asleepLine() string {
	if len(r.Asleep) == 0 {
		return ""
	}
	as := append([]AsleepUnread(nil), r.Asleep...)
	sort.SliceStable(as, func(i, j int) bool { return as[i].Since.Before(as[j].Since) })
	var parts []string
	for i, a := range as {
		if i == maxWaiters {
			parts = append(parts, fmt.Sprintf("…+%d more", len(as)-maxWaiters))
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%d tagged since %s)", a.Name, a.Count, a.Since.UTC().Format("15:04")))
	}
	return "Asleep with unread tagged messages: " + strings.Join(parts, ", ")
}

// shortAge is a wait's age as the roster says it ("19m", "1h05m").
func shortAge(d time.Duration) string {
	m := int(d.Minutes())
	if m < 0 {
		m = 0
	}
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}

// ComposeLines writes the check as a header line and a short list of lines, in priority order (the first is the header). Compose is
// the same text on one line, which is how it is typed: a newline would be an Enter in the agent's terminal.
func ComposeLines(in CheckInput) []string {
	if in.Roster == nil {
		return []string{Compose(in)}
	}
	r := in.Roster
	var lines []string
	lines = append(lines, fmt.Sprintf("[Coral health check] %s · %s", in.Now.UTC().Format("15:04Z"), r.headerCounts()))
	lines = append(lines, r.waitLines()...)
	if l := r.asleepLine(); l != "" {
		lines = append(lines, l)
	}
	items := append([]Item(nil), in.Items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Rank < items[j].Rank })
	itemLines := itemTexts(items)
	if len(itemLines) > 0 {
		lines = append(lines, fmt.Sprintf("Needs you, %d: %s", len(itemLines), strings.Join(itemLines, " ")))
	}
	if len(in.Questions) > 0 {
		lines = append(lines, questionsLine(in))
	}
	hintAt := len(lines) // the hint goes before the progress line
	progressed := in.Progress.Completed+in.Progress.Reviewed+in.Progress.Posts > 0
	since := in.Progress.Since.Format("15:04")
	switch {
	case !progressed:
		lines = append(lines, fmt.Sprintf("No progress since the last check (%s)%s.", since, parkedNote(in)))
	default:
		lines = append(lines, fmt.Sprintf("Since %s: %s%s.", since, progressPhrase(in.Progress), parkedNote(in)))
	}
	switch {
	case r.AllWorking() && len(in.Items) == 0 && len(in.Questions) == 0:
		who := fmt.Sprintf("All %d awake agents are working.", r.Awake())
		if r.Awake() == 1 {
			who = "The 1 awake agent is working."
		}
		lines = append(lines, who+" Nothing needs you.")
	case !r.NeedsAttention() && len(in.Items) == 0 && len(in.Questions) == 0:
		lines = append(lines, "Nothing needs you.")
	}
	// The peek hint is a pointer, not a fact: it is left out first when the check would not fit (the other lines then drop as before).
	if hint := in.peekLine(items); hint != "" {
		withHint := append(append(append([]string(nil), lines[:hintAt]...), hint), lines[hintAt:]...)
		if len(strings.Join(withHint, " • ")) <= MaxCheck {
			return withHint
		}
	}
	return fitLines(lines)
}

// peekLine is the line that tells the orchestrator how to see why an agent is idle, waiting or stuck: its terminal, with coral-board peek. It names
// the first such agent (a ranked item's, else the longest waiter's) in the example, and is empty when the recipient is not the orchestrator, when no
// awake agent is idle, waiting, at a prompt or stuck, or when everyone is working. A sleeper alone has no terminal to read.
func (in CheckInput) peekLine(items []Item) string {
	r := in.Roster
	if !in.PeekHint || r == nil {
		return ""
	}
	name := ""
	for _, it := range items {
		if it.Agent != "" && it.Agent != "Board" { // "Board" is a queue problem, not an agent
			name = it.Agent
			break
		}
	}
	if name == "" && len(r.Waits) > 0 {
		groups := append([]WaitGroup(nil), r.Waits...)
		sort.SliceStable(groups, func(i, j int) bool {
			a, b := groups[i], groups[j]
			if a.You != b.You {
				return a.You
			}
			if len(a.Waiters) != len(b.Waiters) {
				return len(a.Waiters) > len(b.Waiters)
			}
			return a.On < b.On
		})
		for _, g := range groups {
			best := ""
			var age time.Duration = -1
			for _, w := range g.Waiters {
				if w.Age > age || (w.Age == age && w.Name < best) {
					best, age = w.Name, w.Age
				}
			}
			if best != "" {
				name = best
				break
			}
		}
	}
	stuck := name != "" || r.Counts[StatePrompt] > 0 || r.Counts[StateIdle] > 0 || r.Counts[StateUnknown] > 0
	if !stuck {
		return ""
	}
	if name == "" {
		name = "<agent>"
	}
	name = strings.NewReplacer(`"`, "", `\`, "").Replace(name) // the example is a command: no quote or backslash from a member's name
	return fmt.Sprintf(`To see why, read their terminal: coral-board peek "%s" --lines 30.`, name)
}

// fitLines keeps the check within MaxCheck: the header stays, and lines from the end are left out first, counted.
func fitLines(lines []string) []string {
	joined := func(ls []string) string { return strings.Join(ls, " • ") }
	if len(joined(lines)) <= MaxCheck {
		return lines
	}
	for keep := len(lines) - 1; keep >= 1; keep-- {
		out := append(append([]string(nil), lines[:keep]...), fmt.Sprintf("(%d more lines not shown.)", len(lines)-keep))
		if len(joined(out)) <= MaxCheck {
			return out
		}
	}
	return []string{strings.ToValidUTF8(lines[0][:min(len(lines[0]), MaxCheck)], "")}
}

// roster check: Compose's one-line form.
func composeRoster(in CheckInput) string {
	return strings.Join(ComposeLines(in), " • ")
}

// FormatList writes the lines as the check reads when shown: the header, then one "- " line each (what Compose types is the same
// text on one line).
func FormatList(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	out := lines[0]
	for _, l := range lines[1:] {
		out += "\n- " + l
	}
	return out
}
