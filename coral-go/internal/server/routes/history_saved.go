package routes

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"

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

// SavedAgentPresence is which of these message keys are in the transcript of that session (#5802, the Saved list's marks). The transcript is looked for in the store of the
// bookmark's own agent type only (a Claude bookmark never walks ~/.codex, #5820). reachable is false when there is no transcript of the session here; known is false when the
// list's time budget ended before that could be told. The answer for a key is kept while the transcript file is as it was (same path, size and modification time: a stat per
// session, a read only when it changed), and "no transcript" for a short while.
func (h *HistoryHandler) SavedAgentPresence(ctx context.Context, sessionID, agentType string, keys []string) (found map[string]bool, reachable, known bool) {
	h.presenceOnce.Do(func() {
		h.presence = &savedPresence{
			now: time.Now,
			fingerprint: func(ctx context.Context, sid, agentType string) (path, fp string, ok, aborted bool) {
				_, workingDir := h.sessionIdentity(ctx, sid)
				return jsonl.TranscriptFingerprintCtx(ctx, sid, workingDir, agentType)
			},
			read: func(ctx context.Context, sid, agentType string) ([]map[string]any, string) {
				_, workingDir := h.sessionIdentity(ctx, sid)
				rd := jsonl.NewSessionReader() // each request reads through its own reader (#3651)
				messages, _ := rd.ReadNewMessages(sid, workingDir, agentType)
				return messages, rd.SessionPath(sid)
			},
		}
	})
	if agentType == "" {
		agentType, _ = h.sessionIdentity(ctx, sessionID)
	}
	return h.presence.check(ctx, messageKeySecretFor(h.cfg), sessionID, agentType, keys)
}

// maxSavedPresenceEntries bounds the kept answers ("is key K in session S"): the oldest used go first.
const maxSavedPresenceEntries = 512

// The Saved list's checks: how long all of them may take together (the bookmarks not reached are listed unmarked), how long "no transcript here" is believed, and how many
// sessions that is remembered for (#5820).
var savedCheckBudget = 1500 * time.Millisecond

const (
	savedNoTranscriptFor = 60 * time.Second
	maxSavedNoTranscript = 1024
)

// savedPresence is the Saved list's cache of whether a message is in a session's transcript (#5802: 50 transcripts of 10 MB cost 650 ms on every list). An answer holds while the
// transcript's fingerprint (path, size, modification time) is the one it was made under; fingerprint and read are injected so a test counts the reads.
type savedPresence struct {
	now         func() time.Time
	fingerprint func(ctx context.Context, sessionID, agentType string) (path, fingerprint string, ok, aborted bool) // the transcript file of that agent type, and its path, size and time
	read        func(ctx context.Context, sessionID, agentType string) (messages []map[string]any, path string)     // the whole transcript and the file it was read from

	mu        sync.Mutex
	order     *list.List // most recently used first; elements are *presenceEntry
	entries   map[string]*list.Element
	noneUntil map[string]time.Time // session id + agent type -> when "no transcript here" stops being believed
}

type presenceEntry struct {
	id, fp string
	found  bool
}

func (c *savedPresence) check(ctx context.Context, secret []byte, sessionID, agentType string, keys []string) (found map[string]bool, reachable, known bool) {
	noneKey := sessionID + "\x00" + agentType
	c.mu.Lock()
	if until, ok := c.noneUntil[noneKey]; ok && c.now().Before(until) {
		c.mu.Unlock()
		return nil, false, true
	}
	c.mu.Unlock()
	statPath, fp, stat, aborted := c.fingerprint(ctx, sessionID, agentType)
	if aborted {
		return nil, false, false // the budget ended: nothing is known, and nothing is kept
	}
	if !stat {
		c.mu.Lock()
		if c.noneUntil == nil || len(c.noneUntil) >= maxSavedNoTranscript {
			c.noneUntil = map[string]time.Time{}
		}
		c.noneUntil[noneKey] = c.now().Add(savedNoTranscriptFor)
		c.mu.Unlock()
		return nil, false, true
	}
	found = make(map[string]bool, len(keys))
	var missed []string
	c.mu.Lock()
	delete(c.noneUntil, noneKey)
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
		return found, true, true
	}
	messages, readPath := c.read(ctx, sessionID, agentType)
	if messages == nil {
		return nil, false, true // not kept: the file went between the stat and the read
	}
	read := presentKeys(secret, messages, missed)
	if readPath != statPath {
		// The read came from another file than the one that was stat'ed: the answer is true of a file the fingerprint does not watch, so it is not kept (#5802, Security's LOW 1).
		for _, k := range missed {
			found[k] = read[k]
		}
		return found, true, true
	}
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
	return found, true, true
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
