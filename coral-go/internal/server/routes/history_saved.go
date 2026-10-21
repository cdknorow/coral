package routes

import (
	"context"
	"strings"
)

// SavedAgentText reads one agent message of this machine for a bookmark (#5753): the text of the message with that key in the transcript of that session, found as
// the history view finds it. reachable is false when there is no transcript of the session here (an agent that ended on another computer, or a history that was
// deleted); found is false when there is one and the message is not in it.
func (h *HistoryHandler) SavedAgentText(ctx context.Context, sessionID, agentType, key string) (text string, found, reachable bool) {
	messages, _ := h.sessionTranscript(ctx, sessionID)
	if messages == nil {
		return "", false, false
	}
	i := findKey(messageKeySecretFor(h.cfg), messages, key)
	if i < 0 {
		return "", false, true
	}
	m := messages[i]
	for _, k := range []string{"content", "text"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s, true, true
		}
	}
	return "", false, true
}
