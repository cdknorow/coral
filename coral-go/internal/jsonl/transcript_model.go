package jsonl

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// The model an agent is actually using, as its own transcript records it (#4065): Claude
// Code writes it on every assistant entry (message.model), Codex on every turn's
// turn_context (payload.model). It changes when the user switches with /model.

var (
	modelKey         = []byte(`"model"`)
	assistantType    = []byte(`"assistant"`)
	turnContextType  = []byte(`"turn_context"`)
	transcriptModelR = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]+-]{0,127}$`)
)

// cleanModelID keeps a model id that looks like one ("claude-opus-5-5", "gpt-6-astra",
// "opus[1m]"), and drops Claude Code's own "<synthetic>" messages and anything else.
func cleanModelID(m string) string {
	m = strings.TrimSpace(m)
	if !transcriptModelR.MatchString(m) {
		return ""
	}
	return m
}

// claudeLineModel is an assistant entry's message.model, or "".
func claudeLineModel(line []byte) string {
	if !bytes.Contains(line, modelKey) || !bytes.Contains(line, assistantType) {
		return ""
	}
	var rec struct {
		Type    string `json:"type"`
		Message struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "assistant" {
		return ""
	}
	return cleanModelID(rec.Message.Model)
}

// codexLineModel is a turn_context entry's payload.model, or "".
func codexLineModel(line []byte) string {
	if !bytes.Contains(line, turnContextType) || !bytes.Contains(line, modelKey) {
		return ""
	}
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			Model string `json:"model"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "turn_context" {
		return ""
	}
	return cleanModelID(rec.Payload.Model)
}

// TranscriptModel is the model a session's transcript last recorded, from what the turn
// readers (ReadClaudeTurnEvent, ReadCodexTurnEvent) have already read: it never reads the
// file itself, so a list can ask for every session at no cost. "" when none was seen yet;
// the caller then shows the model the session was launched or started with.
func (r *SessionReader) TranscriptModel(sessionID string) string {
	r.mu.Lock()
	c := r.cache[sessionID]
	r.mu.Unlock()
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.claudeState.model != "" {
		return c.claudeState.model
	}
	return c.codexState.model
}
