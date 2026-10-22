package background

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/teamhealth"
)

// ruleHeldNotice is the health rule that tells the Operator about a delivery held at a dialog.
const ruleHeldNotice = "held-notice"

// heldNotices tells the Operator, on the board, about the team's agents whose automated messages
// have waited at a dialog for the set minutes (#4175 step 3): the dialog needs a person at that
// terminal, and the agent's own card already shows it (#4104). Security's #4176 rule 10: the line
// names the agent and says "waiting at a dialog", nothing else (no screen text, no held text);
// nothing is typed into the terminal. Once per held episode (an episode is the agent and the time
// its hold began), within the rule's limits (rule 5), and one line per run for all of them (rule 6).
//
// Messages held behind text in the agent's input box that isn't Coral's (a draft) get the same notice, in a line of their own
// (#247): Coral never types over it, so it waits for someone at that terminal too. A Codex agent's messages were held that way
// for two hours with no notice.
func (h *HealthNudges) heldNotices(ctx context.Context, project string, subs []board.Subscriber, now time.Time,
	hs board.HealthSettings, take func(rule, target string, cooldown time.Duration) bool) {
	if !hs.HeldNotice.On {
		return
	}
	after := time.Duration(hs.HeldNotice.AfterMin) * time.Minute
	h.heldNotice(ctx, project, subs, now, after, take, h.heldDialogs, ruleHeldNotice+"-msg",
		"@Operator [Coral health] waiting at a dialog: %s. Coral's messages to them are held until someone answers the dialog at their terminal.")
	h.heldNotice(ctx, project, subs, now, after, take, h.heldDrafts, ruleHeldNotice+"-draft",
		"@Operator [Coral health] Coral's messages to %s are waiting behind text in their input box that Coral didn't type. "+
			"Coral never sends or clears that text. Send or clear it in their terminal, and the waiting messages are delivered.")
}

// heldNotice is one kind of heldNotices: the agents held (by held) for at least after, told once per episode under action.
func (h *HealthNudges) heldNotice(ctx context.Context, project string, subs []board.Subscriber, now time.Time, after time.Duration,
	take func(rule, target string, cooldown time.Duration) bool, heldFn func() map[string]time.Time, action, format string) {
	if heldFn == nil {
		return
	}
	held := heldFn()
	var names []string
	for _, sub := range subs {
		since, ok := held[sub.SessionName]
		if !ok || now.Sub(since) < after {
			continue
		}
		episode := sub.SessionName + "@" + since.UTC().Format(time.RFC3339Nano)
		if done, _ := h.store.HealthActionTaken(ctx, project, action, episode); done {
			continue // already told about this hold
		}
		// Repeats are stopped by the episode record above; the limits still cap a flapping hold.
		if !take(ruleHeldNotice, sub.SubscriberID, 5*time.Minute) {
			continue
		}
		h.store.RecordHealthAction(ctx, project, action, episode, now)
		names = append(names, fmt.Sprintf("%s (%d min)", teamhealth.Name(sub.SubscriberID), int(now.Sub(since).Minutes())))
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	h.store.PostSystemMessage(ctx, project, "Coral Task Queue", fmt.Sprintf(format, strings.Join(names, ", ")), nil)
}
