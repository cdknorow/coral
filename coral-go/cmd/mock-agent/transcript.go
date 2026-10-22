package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/subgentic/coral/internal/transcriptlink"
)

// mockClaudeVersion is the Claude Code version the mock transcript claims to be written by.
const mockClaudeVersion = "2.1.301"

// transcript writes a transcript in Claude Code's JSONL format, where Coral looks for one
// (CLAUDE_PROJECTS_DIR, else ~/.claude/projects, in a folder named after the working
// directory), so stress and soak runs exercise the real transcript paths: the turn state, the
// model an agent uses, the login-screen check that a transcript ends, and the indexer.
// Each turn is a user entry, an assistant entry (message.model, usage; every other turn a tool
// use and its result too) and the turn_duration entry that ends it, as Claude Code writes them.
type transcript struct {
	mu        sync.Mutex
	path      string
	sessionID string
	cwd       string
	model     string
	last      string // the previous entry's uuid
	turns     int
}

// sessionIDRe is a session id as Coral passes it (a uuid): it names the file, so nothing else
// (a stray "../x") can put the transcript outside the projects folder.
var sessionIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// newTranscript prepares the transcript for sessionID, or returns nil when it can't be written
// (no session id, or not a uuid; no home). The folder is created when the first turn is written.
func newTranscript(sessionID, model string) *transcript {
	if !sessionIDRe.MatchString(sessionID) {
		return nil
	}
	base := os.Getenv("CLAUDE_PROJECTS_DIR")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return nil
		}
		base = filepath.Join(home, ".claude", "projects")
	}
	cwd, _ := os.Getwd()
	if model == "" {
		model = "claude-opus-5-5"
	}
	dir := filepath.Join(base, encodeProjectDir(cwd))
	return &transcript{path: filepath.Join(dir, sessionID+".jsonl"), sessionID: sessionID, cwd: cwd, model: model}
}

// encodeProjectDir is Claude Code's folder name for a working directory (the shared rule, #5308).
func encodeProjectDir(cwd string) string { return transcriptlink.ClaudeProjectDirName(cwd) }

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// common is the envelope every entry carries.
func (t *transcript) common(kind string, at time.Time) map[string]any {
	e := map[string]any{
		"parentUuid": nil, "isSidechain": false, "type": kind, "uuid": newUUID(),
		"timestamp": at.UTC().Format("2006-01-02T15:04:05.000Z"), "userType": "external",
		"entrypoint": "cli", "cwd": t.cwd, "sessionId": t.sessionID, "version": mockClaudeVersion, "gitBranch": "",
	}
	if t.last != "" {
		e["parentUuid"] = t.last
	}
	t.last = e["uuid"].(string)
	return e
}

// Turn appends one turn answering prompt. Errors are reported, not fatal: the mock keeps running.
func (t *transcript) Turn(prompt string) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.turns++
	start := time.Now()
	var lines []map[string]any

	user := t.common("user", start)
	user["message"] = map[string]any{"role": "user", "content": prompt}
	user["permissionMode"] = "default"
	lines = append(lines, user)

	usage := map[string]any{"input_tokens": 1200 + 10*t.turns, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 9000, "output_tokens": 80, "service_tier": "standard"}
	if t.turns%2 == 0 {
		toolID := "toolu_mock" + strings.ReplaceAll(newUUID(), "-", "")[:20]
		call := t.common("assistant", start.Add(300*time.Millisecond))
		call["message"] = map[string]any{
			"model": t.model, "id": "msg_mock" + strings.ReplaceAll(newUUID(), "-", "")[:20], "type": "message", "role": "assistant",
			"content":     []any{map[string]any{"type": "tool_use", "id": toolID, "name": "Bash", "input": map[string]any{"command": "ls", "description": "List files"}}},
			"stop_reason": "tool_use", "stop_sequence": nil, "usage": usage,
		}
		call["requestId"] = "req_mock" + strings.ReplaceAll(newUUID(), "-", "")[:20]
		lines = append(lines, call)
		result := t.common("user", start.Add(600*time.Millisecond))
		result["message"] = map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": toolID, "content": "README.md\nmain.go", "is_error": false}}}
		result["toolUseResult"] = map[string]any{"stdout": "README.md\nmain.go", "stderr": "", "interrupted": false}
		lines = append(lines, result)
	}
	reply := t.common("assistant", start.Add(time.Second))
	reply["message"] = map[string]any{
		"model": t.model, "id": "msg_mock" + strings.ReplaceAll(newUUID(), "-", "")[:20], "type": "message", "role": "assistant",
		"content":     []any{map[string]any{"type": "text", "text": fmt.Sprintf("Mock reply %d to: %s", t.turns, firstLine(prompt))}},
		"stop_reason": "end_turn", "stop_sequence": nil, "usage": usage,
	}
	reply["requestId"] = "req_mock" + strings.ReplaceAll(newUUID(), "-", "")[:20]
	lines = append(lines, reply)
	end := t.common("system", start.Add(1100*time.Millisecond))
	end["subtype"] = "turn_duration"
	end["durationMs"] = 1100
	end["messageCount"] = len(lines) + 1
	end["isMeta"] = false
	lines = append(lines, end)

	// MOCK_AGENT_REPLY_DELAY=<seconds>: the prompt is in the transcript at once, as Claude records it, and the reply only after the delay (a slow first answer).
	if d := replyDelay(); d > 0 && len(lines) > 1 {
		if err := t.append(lines[:1]); err != nil {
			return err
		}
		time.Sleep(d)
		return t.append(lines[1:])
	}
	return t.append(lines)
}

// replyDelay is MOCK_AGENT_REPLY_DELAY as a duration (seconds), 0 when unset or not a number.
func replyDelay() time.Duration {
	secs, err := strconv.ParseFloat(os.Getenv("MOCK_AGENT_REPLY_DELAY"), 64)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

// append writes lines to the transcript.
func (t *transcript) append(lines []map[string]any) error {
	var buf strings.Builder
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(t.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(buf.String()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return s
}
