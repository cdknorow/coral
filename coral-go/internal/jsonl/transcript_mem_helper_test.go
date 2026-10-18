package jsonl

import "context"

// chatRead is what the live chat does to read a session: its transcript from the cache, the
// window and the count (#4620; before the window it read every message).
func chatRead(r *SessionReader, sessionID, agentType string) ([]map[string]any, int) {
	t := r.ReadTranscriptForLiveContext(context.Background(), sessionID, "", agentType)
	return t.Window, t.Total
}
