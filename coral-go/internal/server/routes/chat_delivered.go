package routes

import (
	"time"

	"github.com/subgentic/coral/internal/naming"
	"github.com/subgentic/coral/internal/ptymanager"
)

// markCoralDeliveries flags the user turns of a transcript that Coral itself typed into the agent (#4306): a task notice, the
// unread-messages nudge, the health check, a UI panel request. The transcript records them as the user's own turns, so the
// dashboard drew the ones whose text it didn't recognise as the Operator's bubbles. The automated-input gate remembers a hash of
// each text it delivered (see ptymanager.FlagDelivered for the rules that keep the operator's own turns theirs); a turn it matches
// gets "coral_notice": true; a turn the Operator sent from the dashboard (#5482) gets "coral_notice": false, which the page reads as theirs whatever the words. Every other turn is left as it is (the dashboard's text patterns still read older history and
// anything sent before the server started). It runs on the whole transcript, before any page or "after" cut, because each
// delivery is used by one turn: which turn that is depends on the turns before it.
// The messages come from the reader's cache, which every client shares: a flagged message is a copy, never an edit.
func markCoralDeliveries(msgs []map[string]any, agentType, sessionID, name string) []map[string]any {
	if len(msgs) == 0 {
		return msgs
	}
	names := []string{name}
	if sessionID != "" {
		names = append(names, naming.SessionName(agentType, sessionID))
	}
	coral, operator := ptymanager.HasDeliveredTexts(names...), ptymanager.HasOperatorTexts(names...)
	if !coral && !operator {
		return msgs
	}
	texts := make([]string, len(msgs))
	stamps := make([]time.Time, len(msgs))
	for i, m := range msgs {
		if m["type"] != "user" {
			continue
		}
		texts[i], _ = m["content"].(string)
		if ts, _ := m["timestamp"].(string); ts != "" {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
				stamps[i] = t
			}
		}
	}
	var flags, byOperator []bool
	if coral {
		flags = ptymanager.FlagDelivered(texts, stamps, names...)
	}
	if operator {
		// What a person sent from the dashboard is theirs whatever its words look like (#5482): "coral_notice": false, so the page does not take "[Coral ...]" or "Stop hook feedback:" typed by the
		// Operator for a notice. A turn Coral delivered is not also the Operator's: the delivery wins.
		own := make([]string, len(texts))
		for i := range texts {
			if i >= len(flags) || !flags[i] {
				own[i] = texts[i]
			}
		}
		byOperator = ptymanager.FlagOperatorSent(own, stamps, names...)
	}
	var out []map[string]any
	for i := range msgs {
		delivered := i < len(flags) && flags[i]
		ownTurn := !delivered && i < len(byOperator) && byOperator[i]
		if !delivered && !ownTurn {
			continue
		}
		if out == nil {
			out = append([]map[string]any(nil), msgs...)
		}
		c := make(map[string]any, len(msgs[i])+1)
		for k, v := range msgs[i] {
			c[k] = v
		}
		c["coral_notice"] = delivered
		out[i] = c
	}
	if out == nil {
		return msgs
	}
	return out
}
