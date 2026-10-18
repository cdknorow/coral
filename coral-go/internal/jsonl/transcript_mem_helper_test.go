package jsonl

import "context"

// chatRead is what the live chat does to read a session: the whole transcript from the cache.
func chatRead(r *SessionReader, sessionID, agentType string) ([]map[string]any, int) {
	return r.ReadAllMessagesForLiveContext(context.Background(), sessionID, "", agentType)
}
