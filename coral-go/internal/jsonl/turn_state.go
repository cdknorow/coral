package jsonl

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	at "github.com/cdknorow/coral/internal/agenttypes"
)

// CodexTurnEvent reads an explicit lifecycle signal from a bounded transcript
// tail. The window is deliberately larger than a typical turn because Codex
// can emit more than 1 MiB of tool/output records before task_complete. It
// never infers idle from silence, assistant text, or file age.
func CodexTurnEvent(path string) (event string, at time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	const limit int64 = 8 * 1024 * 1024
	start := info.Size() - limit
	if start < 0 {
		start = 0
	}
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return
	}
	if start > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return
		}
		data = data[i+1:]
	}
	lines := bytes.Split(data, []byte{'\n'})
	// A trailing partial record is not committed evidence.
	active := false
	for _, line := range lines[:len(lines)-1] {
		var entry struct {
			Timestamp string `json:"timestamp"`
			Type      string `json:"type"`
			Payload   struct {
				Type string `json:"type"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "event_msg" {
			// Codex writes response/tool records between task_started and
			// task_complete. They are transcript activity even when no Coral
			// hook event has been emitted for the latest write.
			var activity struct {
				Timestamp string `json:"timestamp"`
			}
			if active && json.Unmarshal(line, &activity) == nil {
				if timestamp, err := time.Parse(time.RFC3339Nano, activity.Timestamp); err == nil {
					event, at = "prompt_submit", timestamp
				}
			}
			continue
		}
		kind := ""
		switch entry.Payload.Type {
		case "task_started", "user_message":
			kind = "prompt_submit"
			active = true
		case "task_complete":
			kind = "stop"
			active = false
		case "turn_aborted":
			kind = "session_reset"
			active = false
		}
		if kind == "" {
			continue
		}
		timestamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
		if err != nil {
			continue
		}
		event, at = kind, timestamp
	}
	return
}

// ReadCodexTurnEvent reuses the session reader's resolved path so frequent UI
// updates do not rescan the transcript directory for every agent.
func (r *SessionReader) ReadCodexTurnEvent(sessionID, workingDir string) (string, time.Time) {
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
	return readCodexTurnEventIncremental(c.path, &c.codexState)
}

// readCodexTurnEventIncremental tracks lifecycle records by byte offset. A
// cold read scans the transcript once (including turns larger than the bounded
// UI tail); ordinary refreshes read only newly appended complete lines. Header
// and size checks reset the tracker when a rollout is replaced or truncated.
func readCodexTurnEventIncremental(path string, state *codexTurnCache) (string, time.Time) {
	scanTranscriptAppend(path, &state.transcriptReadState, func() {
		state.event, state.at, state.active = "", time.Time{}, false
	}, func(line []byte) { updateCodexTurnState(state, line) })
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
				state.event, state.at = kind, timestamp
			}
		}
		return
	}
	if !state.active {
		return
	}
	var activity struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(line, &activity) == nil {
		if timestamp, err := time.Parse(time.RFC3339Nano, activity.Timestamp); err == nil {
			state.event, state.at = "prompt_submit", timestamp
		}
	}
}

// AgyTurnEvent reads an explicit lifecycle signal from a bounded transcript
// tail for Antigravity (and legacy Gemini) sessions.
func AgyTurnEvent(path string) (event string, at time.Time, summary string) {
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	const limit int64 = 1024 * 1024
	start := info.Size() - limit
	if start < 0 {
		start = 0
	}
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return
	}

	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte{'['}) || strings.HasSuffix(path, ".json") {
		return geminiLegacyTurnEvent(data)
	}

	if start > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return
		}
		data = data[i+1:]
	}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines) == 0 {
		return
	}
	// A trailing partial record is not committed evidence.
	candidates := lines
	if len(lines) > 1 {
		candidates = lines[:len(lines)-1]
	}

	return agyTurnLines(candidates)
}

func agyTurnLines(candidates [][]byte) (event string, at time.Time, summary string) {
	for _, line := range candidates {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var entry struct {
			StepIndex int    `json:"step_index"`
			Source    string `json:"source"`
			Type      string `json:"type"`
			Status    string `json:"status"`
			CreatedAt string `json:"created_at"`
			Timestamp string `json:"timestamp"`
			ToolCalls []struct {
				Name string `json:"name"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		tsStr := entry.CreatedAt
		if tsStr == "" {
			tsStr = entry.Timestamp
		}
		if tsStr == "" {
			continue
		}
		timestamp, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			timestamp, err = time.Parse(time.RFC3339, tsStr)
			if err != nil {
				continue
			}
		}

		switch entry.Type {
		case "USER_INPUT":
			event = "prompt_submit"
			at = timestamp
			summary = ""

		case "PLANNER_RESPONSE":
			if entry.Status == "ERROR" {
				event = "stop"
				at = timestamp
				summary = ""
				continue
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
					event = "notification"
					summary = "Antigravity needs your input"
				} else {
					event = "tool_use"
					summary = ""
				}
				at = timestamp
			} else {
				// No tool calls means the model concluded its turn and is waiting for user
				event = "stop"
				at = timestamp
				summary = ""
			}

		case "GENERIC":
			// Tool output / execution result
			event = "tool_use"
			at = timestamp
			summary = ""
		}
	}
	return
}

func geminiLegacyTurnEvent(data []byte) (event string, at time.Time, summary string) {
	var messages []struct {
		Role      string `json:"role"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &messages); err != nil || len(messages) == 0 {
		return
	}
	last := messages[len(messages)-1]
	tsStr := last.Timestamp
	if tsStr != "" {
		if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			at = t
		} else if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
			at = t
		}
	}
	switch last.Role {
	case "user":
		event = "prompt_submit"
	case "model":
		event = "stop"
	}
	return
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
			c.path = resolveTranscriptPath(sessionID, workingDir, at.Agy)
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
	}, func(line []byte) {
		event, atTime, summary := agyTurnLines([][]byte{line})
		if event != "" {
			state.event, state.at, state.summary = event, atTime, summary
		}
	})
	return state.event, state.at, state.summary
}
