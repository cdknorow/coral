package background

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/board"
	"github.com/cdknorow/coral/internal/teamhealth"
)

// The roster of the orchestrator's check (#4583): what each agent is doing, who waits on whom, who sleeps with tagged messages
// unread, and the one escalation a long wait earns.

// waitEscalateAfter is how long an agent waits on someone before that someone is told, once.
const waitEscalateAfter = 10 * time.Minute

// ruleWaitEscalation names the escalation in the store's counters: one per (awaited agent, wait id).
const ruleWaitEscalation = "wait-escalation"

// waitTarget is who a registered wait waits on: a member (sub), or a label for a wait on something else.
type waitTarget struct {
	sub   *board.Subscriber
	label string
}

// resolveWait works out from the wait (coral-board wait --from X, --task N, --commit R, --operator, or none) what it waits on.
func resolveWait(w board.RegisteredWait, subs []board.Subscriber, tasks map[int64]board.Task) waitTarget {
	member := func(name string) *board.Subscriber {
		name = strings.TrimSpace(name)
		for i := range subs {
			if name != "" && (board.SameIdentity(subs[i].SubscriberID, name) || strings.EqualFold(strings.TrimSpace(subs[i].JobTitle), name)) {
				return &subs[i]
			}
		}
		return nil
	}
	switch w.WaitType {
	case board.WaitOperator:
		return waitTarget{label: "the Operator"}
	case "message":
		if w.TargetID == "" {
			return waitTarget{label: "any board message"}
		}
		if s := member(w.TargetID); s != nil {
			return waitTarget{sub: s}
		}
		return waitTarget{label: teamhealth.Name(w.TargetID)}
	case "task":
		var id int64
		if _, err := fmt.Sscanf(w.TargetID, "%d", &id); err == nil {
			if t, ok := tasks[id]; ok && t.AssignedTo != nil {
				if s := member(*t.AssignedTo); s != nil {
					return waitTarget{sub: s}
				}
			}
			return waitTarget{label: fmt.Sprintf("task #%d", id)}
		}
		return waitTarget{label: "a task"}
	case "commit":
		return waitTarget{label: "a commit"}
	}
	return waitTarget{label: "any board update"}
}

// rosterInputs is what buildRoster reads.
type rosterInputs struct {
	project   string
	recipient *board.Subscriber
	subs      []board.Subscriber
	bySession map[string]AgentInfo
	waits     []board.RegisteredWait
	tasks     []board.Task // open, not parked
	now       time.Time
}

// buildRoster classifies every agent but the recipient from its real state and groups the waits by who they wait on.
// attention is how many things in it are for the reader (waits not on the Operator, prompts, idle agents, sleepers with tags unread).
func (m *BoardHealthMonitor) buildRoster(ctx context.Context, in rosterInputs) (*teamhealth.Roster, int) {
	r := &teamhealth.Roster{Recipient: teamhealth.Name(in.recipient.SubscriberID), Counts: map[string]int{}}
	sleeping, _ := m.store.SleepingSubscribers(ctx, in.project)
	taskByID := map[int64]board.Task{}
	for _, t := range in.tasks {
		taskByID[t.ID] = t
		if t.Status == "pending" && t.AssignedTo == nil {
			r.Claimable++
		}
	}
	waitOf := map[string]board.RegisteredWait{}
	for _, w := range in.waits {
		waitOf[w.SubscriberID] = w
	}
	groups := map[string]*teamhealth.WaitGroup{}
	var order []string
	attention := 0
	for i := range in.subs {
		sub := &in.subs[i]
		if board.SameIdentity(sub.SubscriberID, in.recipient.SubscriberID) {
			continue
		}
		name := teamhealth.Name(sub.SubscriberID)
		a, live := in.bySession[sub.SessionName]
		switch {
		case sleeping[sub.SubscriberID]:
			r.Counts[teamhealth.StateAsleep]++
			if n, since, err := m.store.UnreadTagged(ctx, in.project, sub.SubscriberID); err == nil && n > 0 {
				r.Asleep = append(r.Asleep, teamhealth.AsleepUnread{Name: name, Count: n, Since: since})
				attention++
			}
		case !live:
			r.Counts[teamhealth.StateOffline]++
		default:
			if w, ok := waitOf[sub.SubscriberID]; ok {
				r.Counts[teamhealth.StateWaiting]++
				at, _ := time.Parse(time.RFC3339, w.CreatedAt)
				target := resolveWait(w, in.subs, taskByID)
				if w.WaitType == board.WaitOperator {
					continue // listed with the operator's questions
				}
				attention++
				on, you := target.label, false
				var senders []string
				if target.sub != nil {
					on = teamhealth.Name(target.sub.SubscriberID)
					you = board.SameIdentity(target.sub.SubscriberID, in.recipient.SubscriberID)
					senders = []string{target.sub.SubscriberID, target.sub.JobTitle}
				}
				entry := teamhealth.WaitEntry{Name: name, Age: in.now.Sub(at)}
				if len(senders) > 0 && !at.IsZero() {
					if when, err := m.store.MentionedBy(ctx, in.project, sub.SubscriberID, sub.JobTitle, at, senders...); err == nil && !when.IsZero() {
						entry.Stale = true
					}
				}
				key := strings.ToLower(on)
				g := groups[key]
				if g == nil {
					g = &teamhealth.WaitGroup{On: on, You: you}
					groups[key] = g
					order = append(order, key)
				}
				g.Waiters = append(g.Waiters, entry)
				continue
			}
			sig, known := m.signal(ctx, a.SessionID)
			switch {
			case !known:
				r.Counts[teamhealth.StateUnknown]++
				attention++
			case sig.NeedsInput:
				r.Counts[teamhealth.StatePrompt]++
				attention++
			case sig.InTurn:
				r.Counts[teamhealth.StateWorking]++
			default:
				if resets, blocked := m.blockedByLimit(ctx, a); blocked {
					r.Counts[teamhealth.StateBlocked]++
					r.Blocked = append(r.Blocked, teamhealth.BlockedEntry{Name: name, Resets: resets})
					attention++
					break
				}
				r.Counts[teamhealth.StateIdle]++
				attention++
			}
		}
	}
	sort.Strings(order)
	for _, k := range order {
		r.Waits = append(r.Waits, *groups[k])
	}
	return r, attention
}

// escalateWaits tells an agent, once per wait, that someone has waited on it for more than ten minutes: a direct mention on the
// board ("Lead Developer has waited 17m on you"), within the store's per-target and per-team limits (a refused one is tried again
// at the next scan, and one that went out is recorded so a restart never repeats it).
func (m *BoardHealthMonitor) escalateWaits(ctx context.Context, project string) {
	waits, err := m.store.ListActiveWaits(ctx, project)
	if err != nil || len(waits) == 0 {
		return
	}
	subs, err := m.store.ListSubscribers(ctx, project)
	if err != nil {
		return
	}
	tasks := map[int64]board.Task{}
	if open, err := m.store.ListOpenTasks(ctx, project); err == nil {
		for _, t := range open {
			tasks[t.ID] = t
		}
	}
	now := m.now()
	for _, w := range waits {
		at, err := time.Parse(time.RFC3339, w.CreatedAt)
		if err != nil || now.Sub(at) <= waitEscalateAfter {
			continue
		}
		target := resolveWait(w, subs, tasks)
		if target.sub == nil || board.SameIdentity(target.sub.SubscriberID, w.SubscriberID) {
			continue // the Operator, a commit or a label: nobody to tell
		}
		subject := fmt.Sprint(w.ID)
		if c, err := m.store.GetHealthCounter(ctx, project, ruleWaitEscalation, target.sub.SubscriberID, subject); err != nil || c.Escalated {
			continue
		}
		if why, err := m.store.TakeHealthAction(ctx, project, ruleWaitEscalation, target.sub.SubscriberID, now,
			board.HealthLimits{PerTargetPerHour: 6, TeamPerHour: 30}); err != nil || why != "" {
			continue
		}
		text := fmt.Sprintf("@%s %s has waited %s on you (coral-board wait). Reply to them, or tell them what to do instead.",
			teamhealth.Name(target.sub.SubscriberID), teamhealth.Name(w.SubscriberID), teamhealth.Age(now.Sub(at)))
		if _, err := m.store.PostSystemMessage(ctx, project, "Coral Health Monitor", text, nil); err != nil {
			m.logger.Warn("wait escalation could not be posted", "project", project, "error", err)
			continue
		}
		_ = m.store.MarkHealthEscalated(ctx, project, ruleWaitEscalation, target.sub.SubscriberID, subject, now)
	}
}
