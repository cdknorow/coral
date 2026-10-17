package routes

import (
	"github.com/cdknorow/coral/internal/naming"
	"github.com/cdknorow/coral/internal/ptymanager"
)

// markCoralDeliveries flags the user turns of a transcript page that Coral itself typed into the agent (#4306): a task
// notice, the unread-messages nudge, the health check, a UI panel request. The transcript records them as the user's own
// turns, so the dashboard drew the ones whose text it didn't recognise as the Operator's bubbles. The automated-input gate
// remembers a hash of each text it delivered; a turn that matches gets "coral_notice": true. A turn with no match is left
// as it is (the dashboard's text patterns still read older history and anything sent before the server started).
// The messages come from the reader's cache, which every client shares: a flagged message is a copy, never an edit.
func markCoralDeliveries(msgs []map[string]any, agentType, sessionID, name string) []map[string]any {
	if len(msgs) == 0 {
		return msgs
	}
	names := []string{name}
	if sessionID != "" {
		names = append(names, naming.SessionName(agentType, sessionID))
	}
	match := ptymanager.DeliveredMatcher(names...)
	if match == nil {
		return msgs
	}
	var out []map[string]any
	for i, m := range msgs {
		if m["type"] != "user" {
			continue
		}
		text, _ := m["content"].(string)
		if text == "" || !match(text) {
			continue
		}
		if out == nil {
			out = append([]map[string]any(nil), msgs...)
		}
		c := make(map[string]any, len(m)+1)
		for k, v := range m {
			c[k] = v
		}
		c["coral_notice"] = true
		out[i] = c
	}
	if out == nil {
		return msgs
	}
	return out
}
