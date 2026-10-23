package jsonl

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"
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

// CleanModelID is cleanModelID for callers outside the package: a model id a CLI reported, or ""
// when it does not look like one.
func CleanModelID(m string) string { return cleanModelID(m) }

// claudeLineModel is an assistant entry's message.model, or "".
func claudeLineModel(line []byte) string {
	m, _ := claudeLineModelEffort(line)
	return m
}

// claudeLineModelEffort is an assistant entry's message.model and its top-level effort (the thinking level the turn ran at:
// "low", "medium", "high", ...; null or missing is ""), or "", "" for any other line (#4969).
func claudeLineModelEffort(line []byte) (model, effort string) {
	if !bytes.Contains(line, modelKey) || !bytes.Contains(line, assistantType) {
		return "", ""
	}
	var rec struct {
		Type    string `json:"type"`
		Effort  string `json:"effort"`
		Message struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "assistant" {
		return "", ""
	}
	return cleanModelID(rec.Message.Model), cleanEffort(rec.Effort)
}

// claudeLineTime is a line's timestamp, the zero time when it has none.
func claudeLineTime(line []byte) time.Time {
	var rec struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, rec.Timestamp)
	return t
}

// TranscriptTurnAt is when the assistant entry that TranscriptModel and TranscriptThinking come from was written (zero when none was read, or for a Codex session): a setting made after it is newer
// than what it recorded (#6225). Like them it reads nothing itself.
func (r *SessionReader) TranscriptTurnAt(sessionID string) time.Time {
	r.mu.Lock()
	c := r.cache[sessionID]
	r.mu.Unlock()
	if c == nil {
		return time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.claudeState.turnAt
}

// codexLineModel is a turn_context entry's payload.model, or "".
func codexLineModel(line []byte) string {
	m, _ := codexLineModelEffort(line)
	return m
}

// codexLineModelEffort is a turn_context entry's payload.model and payload.effort, or "", "".
func codexLineModelEffort(line []byte) (model, effort string) {
	if !bytes.Contains(line, turnContextType) || !bytes.Contains(line, modelKey) {
		return "", ""
	}
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			Model  string `json:"model"`
			Effort string `json:"effort"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "turn_context" {
		return "", ""
	}
	return cleanModelID(rec.Payload.Model), cleanEffort(rec.Payload.Effort)
}

// CleanEffort is cleanEffort for callers outside the package: a thinking level a transcript recorded, or "" when it does not look like one.
func CleanEffort(e string) string { return cleanEffort(e) }

var effortR = regexp.MustCompile(`^[a-z]{2,8}$`)

// cleanEffort keeps a thinking level that looks like one ("low", "xhigh", "max"), else "".
func cleanEffort(e string) string {
	e = strings.TrimSpace(e)
	if !effortR.MatchString(e) {
		return ""
	}
	return e
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

// TranscriptThinking is the thinking level the session's newest turn ran at, as its transcript records it (#4969): Claude's
// top-level effort on an assistant entry, Codex's turn_context effort. Like TranscriptModel it reads nothing itself. "" when none
// was recorded (the CLI does not write one, or the turn has not been read yet): the caller shows no level, never a guess.
func (r *SessionReader) TranscriptThinking(sessionID string) string {
	r.mu.Lock()
	c := r.cache[sessionID]
	r.mu.Unlock()
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.claudeState.model != "" {
		return c.claudeState.effort
	}
	if c.codexState.model != "" {
		return c.codexState.effort
	}
	return ""
}
