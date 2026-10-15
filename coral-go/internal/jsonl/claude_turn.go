package jsonl

import (
	"encoding/json"
	"strings"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
)

// ReadClaudeTurnEvent returns the latest end of a turn recorded in a Claude session's
// transcript ("stop") and when, or "" (#3481, #3475 phase 1). Claude Code fires no hook
// when the user denies a permission prompt or interrupts a turn (verified on 2.1.300),
// but its transcript records both: a system record with subtype turn_duration at the end
// of a turn, and a user text "[Request interrupted by user ...]" on an interrupt. Turn
// starts come from the UserPromptSubmit hook. The path is resolved and the file read
// incrementally, like Codex's.
func (r *SessionReader) ReadClaudeTurnEvent(sessionID, workingDir string) (string, time.Time) {
	c := r.getOrCreateSessionCache(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		if c.lastResolved.IsZero() || time.Since(c.lastResolved) >= 2*time.Second {
			c.lastResolved = time.Now()
			c.path = resolveTranscriptPath(sessionID, workingDir, at.Claude)
		}
	}
	if c.path == "" {
		return "", time.Time{}
	}
	scanTranscriptAppend(c.path, &c.claudeState.transcriptReadState, func() {
		c.claudeState.event, c.claudeState.at = "", time.Time{}
	}, func(line []byte) { updateClaudeTurnState(&c.claudeState, line) })
	return c.claudeState.event, c.claudeState.at
}

// updateClaudeTurnState records a turn's end from one transcript line.
func updateClaudeTurnState(state *claudeTurnCache, line []byte) {
	if !strings.Contains(string(line), "turn_duration") && !strings.Contains(string(line), "[Request interrupted by user") {
		return // cheap filter: most lines are neither
	}
	var entry struct {
		Type      string          `json:"type"`
		Subtype   string          `json:"subtype"`
		Timestamp string          `json:"timestamp"`
		Message   json.RawMessage `json:"message"`
	}
	if json.Unmarshal(line, &entry) != nil {
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
	if err != nil {
		return
	}
	switch {
	case entry.Type == "system" && entry.Subtype == "turn_duration":
	case entry.Type == "user" && claudeInterruptText(entry.Message):
	default:
		return
	}
	state.event, state.at = "stop", ts
}

// claudeInterruptText reports whether a user message is Claude's own interrupt marker
// (plain text content, not a tool result quoting it).
func claudeInterruptText(raw json.RawMessage) bool {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return strings.HasPrefix(text, "[Request interrupted by user")
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "text" && strings.HasPrefix(p.Text, "[Request interrupted by user") {
			return true
		}
	}
	return false
}
