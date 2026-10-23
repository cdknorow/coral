package jsonl

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// ReadClaudeTurnEvent returns the latest end of a turn recorded in a Claude session's
// transcript ("stop") and when, or "" (#3481, #3475 phase 1). Claude Code fires no hook
// when the user denies a permission prompt or interrupts a turn (verified on 2.1.300),
// but its transcript records both: a system record with subtype turn_duration at the end
// of a turn, and a user text "[Request interrupted by user ...]" on an interrupt. Turn
// starts come from the UserPromptSubmit hook. The path is resolved and the file read
// incrementally, like Codex's.
func (r *SessionReader) ReadClaudeTurnEvent(sessionID, workingDir string) (string, time.Time) {
	st := r.scanClaudeTranscript(sessionID, workingDir)
	return st.event, st.at
}

// ReadClaudeVersion returns the Claude Code version the session's newest transcript line
// carries, or "" (#3573). It is the running process's own version: Claude Code updates itself
// while an agent keeps running, so the binary's --version can already be newer.
func (r *SessionReader) ReadClaudeVersion(sessionID, workingDir string) string {
	return r.scanClaudeTranscript(sessionID, workingDir).version
}

// ReadClaudeAPIError returns the API error that is a Claude session's latest transcript message
// (the zero value when the latest message is not one), and whether the session has a transcript
// at all (#3830). It is the CLI's own record of why a turn failed: an authentication error
// means the agent needs the user to sign in, a rate_limit error that it hit its usage limit.
func (r *SessionReader) ReadClaudeAPIError(sessionID, workingDir string) (ClaudeAPIError, bool) {
	st := r.scanClaudeTranscript(sessionID, workingDir)
	return st.apiErr, st.initialized
}

// scanClaudeTranscript reads what a Claude transcript gained since the last call and returns the
// turn and version state; the zero state when no transcript is found.
func (r *SessionReader) scanClaudeTranscript(sessionID, workingDir string) claudeTurnCache {
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
		return claudeTurnCache{}
	}
	reset := func() {
		c.claudeState.event, c.claudeState.at, c.claudeState.version, c.claudeState.model, c.claudeState.effort = "", time.Time{}, "", "", ""
		c.claudeState.turnAt = time.Time{}
		c.claudeState.apiErr = ClaudeAPIError{}
	}
	consume := func(line []byte) {
		if v := claudeLineVersion(line); v != "" {
			c.claudeState.version = v
		}
		if m, e := claudeLineModelEffort(line); m != "" {
			c.claudeState.model = m
			c.claudeState.effort = e // the level of the same turn: "" when it recorded none, so a stale one is not shown (#4969)
			c.claudeState.turnAt = claudeLineTime(line)
		}
		updateClaudeTurnState(&c.claudeState, line)
		updateClaudeAPIError(&c.claudeState.apiErr, line)
	}
	// Every field is the newest value in the file, so a transcript not read before is read from its end, in longer and longer
	// tails, until the turn end, the version and the model each have a value (a model comes from an assistant record, so the API
	// error, the latest user or assistant record's, is the file's too): the same state as the whole file gives, without decoding
	// tens of megabytes at the first board status (#4788). A file that lacks a field is read whole, as before. Once read, the file
	// is followed by what is appended.
	for i := 0; ; i++ {
		s := &c.claudeState
		first := !s.initialized
		var tail int64
		if first && i < len(claudeTailWindows) {
			tail = claudeTailWindows[i]
		}
		scanTranscriptAppendTail(c.path, &s.transcriptReadState, reset, consume, tail)
		if !first || tail == 0 || !s.initialized || s.from == 0 || (s.event != "" && s.version != "" && s.model != "") {
			break
		}
		s.transcriptReadState = transcriptReadState{} // the tail missed something an older record may hold
		reset()
	}
	return c.claudeState
}

var claudeVersionKey = []byte(`"version"`)

// claudeLineVersion is a transcript line's top-level "version" value, or "". The line is decoded
// for that one field (Go's decoder takes the top-level key whatever the order): a tool's
// structured result (toolUseResult) can carry its own "version" and come before the record's,
// so the first occurrence in the text isn't the record's (QA's #3573 review). It must look like
// a version. Only lines that contain the key are decoded, each once (the scan reads new lines).
func claudeLineVersion(line []byte) string {
	if !bytes.Contains(line, claudeVersionKey) {
		return ""
	}
	var rec struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(line, &rec) != nil || !versionLike(rec.Version) {
		return ""
	}
	return rec.Version
}

// versionLike: a semantic version as Claude Code writes it ("2.1.300", "2.2.0-beta.1").
var versionLike = regexp.MustCompile(`^\d+\.\d+\.\d+[0-9A-Za-z.+-]*$`).MatchString

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

// ClaudeAPIError is an API error Claude Code recorded as a transcript message (#3830): an
// assistant entry with isApiErrorMessage, the error's kind and the CLI's text, e.g.
// {"error":"authentication_failed", text "Invalid API key · Please run /login"}.
type ClaudeAPIError struct {
	Kind string // "authentication_failed", "oauth_org_not_allowed", "rate_limit", "billing_error", ...
	Text string // the CLI's message, capped
	At   time.Time
}

// NeedsLogin reports whether the error means the user must sign in. Older CLIs wrote no kind:
// their text names /login.
func (e ClaudeAPIError) NeedsLogin() bool {
	switch e.Kind {
	case "authentication_failed", "oauth_org_not_allowed":
		return true
	case "":
		return e.Text != "" && (strings.Contains(e.Text, "Please run /login") || strings.HasPrefix(e.Text, "Invalid API key") ||
			strings.HasPrefix(e.Text, "OAuth token has expired") || strings.HasPrefix(e.Text, "OAuth token revoked"))
	}
	return false
}

// UsageLimit reports whether the error is the CLI's rate-limit error (a usage limit, or the
// server limiting requests), whose text then says which.
func (e ClaudeAPIError) UsageLimit() bool {
	return e.Kind == "rate_limit" || e.Kind == "" && strings.HasPrefix(e.Text, "Claude AI usage limit reached")
}

const claudeAPIErrorTextMax = 300

var (
	claudeUserType      = []byte(`"type":"user"`)
	claudeAssistantType = []byte(`"type":"assistant"`)
)

// updateClaudeAPIError keeps the latest user or assistant message's API error: an error line
// sets it, any other message clears it (the user sent something, or a turn ran since).
// claudeTailWindows are the tail lengths of the first read of a Claude transcript, shortest first (a var for the tests).
var claudeTailWindows = []int64{256 << 10, 1 << 20, 4 << 20, 16 << 20}

func updateClaudeAPIError(e *ClaudeAPIError, line []byte) {
	if !bytes.Contains(line, claudeUserType) && !bytes.Contains(line, claudeAssistantType) {
		return // cheap filter: system, summary and progress lines are neither
	}
	var entry struct {
		Type       string          `json:"type"`
		IsAPIError bool            `json:"isApiErrorMessage"`
		Error      json.RawMessage `json:"error"`
		Status     int             `json:"apiErrorStatus"`
		Timestamp  string          `json:"timestamp"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.Type != "user" && entry.Type != "assistant" {
		return
	}
	if entry.Type != "assistant" || !entry.IsAPIError {
		*e = ClaudeAPIError{}
		return
	}
	var kind string
	_ = json.Unmarshal(entry.Error, &kind) // a kind other than a string is no kind
	if kind == "" && entry.Status == 401 {
		kind = "authentication_failed"
	}
	var msg struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	_ = json.Unmarshal(line, &msg)
	text := claudeMessageText(msg.Message.Content)
	if len(text) > claudeAPIErrorTextMax {
		text = strings.ToValidUTF8(text[:claudeAPIErrorTextMax], "")
	}
	ts, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
	*e = ClaudeAPIError{Kind: kind, Text: strings.TrimSpace(text), At: ts}
}

// claudeMessageText is a message's text: the content itself when it is a string, or its text
// blocks joined.
func claudeMessageText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" && p.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
