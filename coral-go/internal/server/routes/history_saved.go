package routes

import (
	"container/list"
	"context"
	"strings"
	"sync"

	"github.com/subgentic/coral/internal/jsonl"
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

// SavedAgentPresence is which of these message keys are in the transcript of that session (#5802, the Saved list's marks). reachable is false when there is no transcript of the
// session here. The answer for a key is kept while the transcript file is as it was (same path, size and modification time: a stat per session, a read only when it changed).
func (h *HistoryHandler) SavedAgentPresence(ctx context.Context, sessionID string, keys []string) (found map[string]bool, reachable bool) {
	h.presenceOnce.Do(func() {
		h.presence = &savedPresence{
			fingerprint: func(ctx context.Context, sid string) (string, bool) {
				agentType, workingDir := h.sessionIdentity(ctx, sid)
				tried := map[string]bool{}
				for _, t := range []string{agentType, "claude", "codex", "agy"} { // the order sessionTranscript reads in
					if tried[t] {
						continue
					}
					tried[t] = true
					if fp, ok := jsonl.TranscriptFingerprint(sid, workingDir, t); ok {
						return fp, true
					}
				}
				return "", false
			},
			read: func(ctx context.Context, sid string) []map[string]any {
				messages, _ := h.sessionTranscript(ctx, sid)
				return messages
			},
		}
	})
	return h.presence.check(ctx, messageKeySecretFor(h.cfg), sessionID, keys)
}

// maxSavedPresenceEntries bounds the kept answers ("is key K in session S"): the oldest used go first.
const maxSavedPresenceEntries = 512

// savedPresence is the Saved list's cache of whether a message is in a session's transcript (#5802: 50 transcripts of 10 MB cost 650 ms on every list). An answer holds while the
// transcript's fingerprint (path, size, modification time) is the one it was made under; fingerprint and read are injected so a test counts the reads.
type savedPresence struct {
	fingerprint func(ctx context.Context, sessionID string) (string, bool)
	read        func(ctx context.Context, sessionID string) []map[string]any

	mu      sync.Mutex
	order   *list.List // most recently used first; elements are *presenceEntry
	entries map[string]*list.Element
}

type presenceEntry struct {
	id, fp string
	found  bool
}

func (c *savedPresence) check(ctx context.Context, secret []byte, sessionID string, keys []string) (map[string]bool, bool) {
	fp, stat := c.fingerprint(ctx, sessionID)
	if !stat {
		// Nothing to stat: the transcript is read as the history view reads it (and nothing is kept), or there is none.
		messages := c.read(ctx, sessionID)
		if messages == nil {
			return nil, false
		}
		return presentKeys(secret, messages, keys), true
	}
	found := make(map[string]bool, len(keys))
	var missed []string
	c.mu.Lock()
	for _, k := range keys {
		if el, ok := c.entries[sessionID+"\x00"+k]; ok && el.Value.(*presenceEntry).fp == fp {
			c.order.MoveToFront(el)
			found[k] = el.Value.(*presenceEntry).found
		} else {
			missed = append(missed, k)
		}
	}
	c.mu.Unlock()
	if len(missed) == 0 {
		return found, true
	}
	messages := c.read(ctx, sessionID)
	if messages == nil {
		return nil, false // not kept: the file went between the stat and the read
	}
	read := presentKeys(secret, messages, missed)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.order, c.entries = list.New(), map[string]*list.Element{}
	}
	for _, k := range missed {
		found[k] = read[k]
		id := sessionID + "\x00" + k
		if el, ok := c.entries[id]; ok {
			*el.Value.(*presenceEntry) = presenceEntry{id: id, fp: fp, found: read[k]}
			c.order.MoveToFront(el)
			continue
		}
		c.entries[id] = c.order.PushFront(&presenceEntry{id: id, fp: fp, found: read[k]})
	}
	for c.order.Len() > maxSavedPresenceEntries {
		last := c.order.Back()
		delete(c.entries, last.Value.(*presenceEntry).id)
		c.order.Remove(last)
	}
	return found, true
}

// presentKeys is which of keys are the key of one of the messages.
func presentKeys(secret []byte, messages []map[string]any, keys []string) map[string]bool {
	present := make(map[string]bool, len(messages))
	for _, m := range messages {
		present[messageKey(secret, m)] = true
	}
	found := make(map[string]bool, len(keys))
	for _, k := range keys {
		found[k] = present[k]
	}
	return found
}
