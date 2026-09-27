package jsonl

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"time"
)

// CodexTurnEvent reads an explicit lifecycle signal from a bounded transcript
// tail. It never infers idle from silence, assistant text, or file age.
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
	r.mu.Lock()
	c := r.cache[sessionID]
	if c == nil {
		c = &sessionCache{toolUseNames: make(map[string]string)}
		r.cache[sessionID] = c
	}
	if c.path == "" {
		c.path = resolveTranscriptPath(sessionID, workingDir, "codex")
	}
	path := c.path
	r.mu.Unlock()
	return CodexTurnEvent(path)
}
