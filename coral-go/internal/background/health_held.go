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
func (h *HealthNudges) heldNotices(ctx context.Context, project string, subs []board.Subscriber, now time.Time,
	hs board.HealthSettings, take func(rule, target string, cooldown time.Duration) bool) {
	if !hs.HeldNotice.On || h.heldDialogs == nil {
		return
	}
	after := time.Duration(hs.HeldNotice.AfterMin) * time.Minute
	held := h.heldDialogs()
	var names []string
	for _, sub := range subs {
		since, ok := held[sub.SessionName]
		if !ok || now.Sub(since) < after {
			continue
		}
		episode := sub.SessionName + "@" + since.UTC().Format(time.RFC3339Nano)
		if done, _ := h.store.HealthActionTaken(ctx, project, ruleHeldNotice+"-msg", episode); done {
			continue // already told about this hold
		}
		// Repeats are stopped by the episode record above; the limits still cap a flapping dialog.
		if !take(ruleHeldNotice, sub.SubscriberID, 5*time.Minute) {
			continue
		}
		h.store.RecordHealthAction(ctx, project, ruleHeldNotice+"-msg", episode, now)
		names = append(names, fmt.Sprintf("%s (%d min)", teamhealth.Name(sub.SubscriberID), int(now.Sub(since).Minutes())))
	}
	if len(names) == 0 {
		return
	}
	sort.Strings(names)
	h.store.PostSystemMessage(ctx, project, "Coral Task Queue", fmt.Sprintf(
		"@Operator [Coral health] waiting at a dialog: %s. Coral's messages to them are held until someone answers the dialog at their terminal.",
		strings.Join(names, ", ")), nil)
}
