package jsonl

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"time"

	at "github.com/subgentic/coral/internal/agenttypes"
)

// ReadCodexTurnEvent reuses the session reader's resolved path so frequent UI
// updates do not rescan the transcript directory for every agent.
func (r *SessionReader) ReadCodexTurnEvent(sessionID, workingDir string) (string, time.Time) {
	kind, at, _ := r.ReadCodexTurn(sessionID, workingDir)
	return kind, at
}

// ReadCodexTurn is ReadCodexTurnEvent that also says whether the event is a record of the turn going on (activity) rather than a turn's start or end: only a start (task_started,
// user_message) and an end (task_complete, turn_aborted) can answer an open request, an activity record never does (#6672).
func (r *SessionReader) ReadCodexTurn(sessionID, workingDir string) (kind string, at time.Time, activity bool) {
	c := r.getOrCreateSessionCache(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	refreshCodexLink(c, sessionID)
	if c.path == "" {
		if c.lastResolved.IsZero() || time.Since(c.lastResolved) >= 2*time.Second {
			c.lastResolved = time.Now()
			c.path = resolveTranscriptPath(sessionID, workingDir, "codex")
		}
	}
	kind, at = readCodexTurnEventIncremental(c.path, &c.codexState)
	return kind, at, c.codexState.activity
}

// readCodexTurnEventIncremental tracks lifecycle records by byte offset. A
// cold read scans the transcript once (including turns larger than the bounded
// UI tail); ordinary refreshes read only newly appended complete lines. Header
// and size checks reset the tracker when a rollout is replaced or truncated.
func readCodexTurnEventIncremental(path string, state *codexTurnCache) (string, time.Time) {
	scanTranscriptAppend(path, &state.transcriptReadState, func() {
		state.event, state.at, state.active, state.activity, state.model, state.effort = "", time.Time{}, false, false, "", ""
	}, func(line []byte) {
		if m, e := codexLineModelEffort(line); m != "" {
			state.model, state.effort = m, e
		}
		updateCodexTurnState(state, line)
	})
	return state.event, state.at
}

func updateCodexTurnState(state *codexTurnCache, line []byte) {
	var entry struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type string `json:"type"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &entry) == nil && entry.Type == "event_msg" {
		kind := ""
		switch entry.Payload.Type {
		case "task_started", "user_message":
			kind, state.active = "prompt_submit", true
		case "task_complete":
			kind, state.active = "stop", false
		case "turn_aborted":
			kind, state.active = "session_reset", false
		}
		if kind != "" {
			if timestamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
				state.event, state.at, state.activity = kind, timestamp, false
			}
		}
		return
	}
	if !state.active {
		return
	}
	// Bookkeeping the CLI writes is not the turn going on: a late one must never close an open approval request (a line written while the dialog waits would make the row read as working,
	// and a person's message would be typed into the permission box, #6658). token_usage_record and turn_context are the two Codex 0.163.0 writes outside event_msg that carry no turn content.
	if entry.Type == "token_usage_record" || entry.Type == "turn_context" {
		return
	}
	var activity struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(line, &activity) == nil {
		if timestamp, err := time.Parse(time.RFC3339Nano, activity.Timestamp); err == nil {
			state.event, state.at, state.activity = "prompt_submit", timestamp, true
		}
	}
}

func agyTurnLineWithErr(line []byte) (event string, at time.Time, summary, apiErr string) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return "", time.Time{}, "", ""
	}
	var entry struct {
		StepIndex int    `json:"step_index"`
		Source    string `json:"source"`
		Type      string `json:"type"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
		Timestamp string `json:"timestamp"`
		Content   string `json:"content"`
		Error     string `json:"error"`
		ToolCalls []struct {
			Name string `json:"name"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal(line, &entry) != nil {
		return "", time.Time{}, "", ""
	}
	tsStr := entry.CreatedAt
	if tsStr == "" {
		tsStr = entry.Timestamp
	}
	var timestamp time.Time
	if tsStr != "" {
		var err error
		timestamp, err = time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			timestamp, _ = time.Parse(time.RFC3339, tsStr)
		}
	}

	switch entry.Type {
	case "USER_INPUT":
		return "prompt_submit", timestamp, "", ""

	case "PLANNER_RESPONSE":
		if entry.Status == "ERROR" {
			errText := entry.Error
			if errText == "" {
				errText = entry.Content
			}
			return "stop", timestamp, "", errText
		}
		if len(entry.ToolCalls) > 0 {
			isAsk := false
			for _, tc := range entry.ToolCalls {
				if tc.Name == "ask_question" {
					isAsk = true
					break
				}
			}
			if isAsk {
				return "notification", timestamp, "Antigravity needs your input", ""
			}
			return "tool_use", timestamp, "", ""
		}
		return "stop", timestamp, "", ""

	case "GENERIC":
		// Tool output / execution result
		return "tool_use", timestamp, "", ""
	}
	return "", time.Time{}, "", ""
}

// ReadAgyTurnEvent reuses the session reader's resolved path so frequent UI
// updates do not rescan the transcript directory for every agent.
func (r *SessionReader) ReadAgyTurnEvent(sessionID, workingDir string) (string, time.Time, string) {
	c := r.getOrCreateSessionCache(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		if c.lastResolved.IsZero() || time.Since(c.lastResolved) >= 2*time.Second {
			c.lastResolved = time.Now()
			c.path = resolveTranscriptPathMode(sessionID, workingDir, at.Agy, true) // a live session: its own marker, never the folder (#4590)
		}
	}
	return readAgyTurnEventIncremental(c.path, &c.agyState)
}

func readAgyTurnEventIncremental(path string, state *agyTurnCache) (string, time.Time, string) {
	// Legacy JSON arrays cannot be tailed as JSONL. Decode changed files as
	// a stream, retaining only the last message, and reuse unchanged results.
	info, err := os.Stat(path)
	if err != nil {
		*state = agyTurnCache{}
		return "", time.Time{}, ""
	}
	if sameTranscriptSnapshot(state.info, info) {
		return state.event, state.at, state.summary
	}
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, ""
	}
	head := transcriptHead(f)
	legacy := strings.HasSuffix(path, ".json") || strings.HasPrefix(strings.TrimSpace(head), "[")
	if legacy {
		defer f.Close()
		decoder := json.NewDecoder(f)
		token, err := decoder.Token()
		if err != nil || token != json.Delim('[') {
			return "", time.Time{}, ""
		}
		var last struct {
			Role      string `json:"role"`
			Timestamp string `json:"timestamp"`
		}
		for decoder.More() {
			last.Role, last.Timestamp = "", ""
			if decoder.Decode(&last) != nil {
				return "", time.Time{}, ""
			}
		}
		if _, err := decoder.Token(); err != nil {
			return "", time.Time{}, ""
		}
		*state = agyTurnCache{}
		switch last.Role {
		case "user":
			state.event = "prompt_submit"
		case "model":
			state.event = "stop"
		}
		state.at, _ = time.Parse(time.RFC3339Nano, last.Timestamp)
		state.info = info
		return state.event, state.at, state.summary
	}
	f.Close()
	scanTranscriptAppend(path, &state.transcriptReadState, func() {
		state.event, state.at, state.summary = "", time.Time{}, ""
		state.apiErr = ""
	}, func(line []byte) {
		event, atTime, summary, errText := agyTurnLineWithErr(line)
		if event != "" {
			state.event, state.at, state.summary = event, atTime, summary
		}
		if event == "prompt_submit" {
			state.apiErr = ""
		} else if errText != "" {
			state.apiErr = errText
		}
	})
	return state.event, state.at, state.summary
}

// ReadAgyAPIError returns the latest transcript error message for an agy session, and whether one was found.
func (r *SessionReader) ReadAgyAPIError(sessionID, workingDir string) (string, bool) {
	c := r.getOrCreateSessionCache(sessionID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		if c.lastResolved.IsZero() || time.Since(c.lastResolved) >= 2*time.Second {
			c.lastResolved = time.Now()
			c.path = resolveTranscriptPath(sessionID, workingDir, at.Agy)
		}
	}
	readAgyTurnEventIncremental(c.path, &c.agyState)
	return c.agyState.apiErr, c.agyState.apiErr != ""
}
