// Package jsonl provides incremental JSONL reading for live agent session transcripts.
package jsonl

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/cdknorow/coral/internal/agent"
	at "github.com/cdknorow/coral/internal/agenttypes"
)

// SessionReader incrementally reads JSONL session files for live chat display.
type SessionReader struct {
	mu    sync.Mutex
	cache map[string]*sessionCache
}

type sessionCache struct {
	path         string
	offset       int64
	messages     []map[string]any
	toolUseNames map[string]string // tool_use_id → tool_name
}

// NewSessionReader creates a new JSONL session reader.
func NewSessionReader() *SessionReader {
	return &SessionReader{cache: make(map[string]*sessionCache)}
}

// ReadNewMessages reads new messages since the last call for the given session.
// Returns (new_messages, total_count).
func (r *SessionReader) ReadNewMessages(sessionID, workingDirectory, agentType string) ([]map[string]any, int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	c := r.cache[sessionID]
	if c == nil {
		c = &sessionCache{toolUseNames: make(map[string]string)}
		r.cache[sessionID] = c
	}

	// Resolve path on first call
	if c.path == "" {
		c.path = resolveTranscriptPath(sessionID, workingDirectory, agentType)
		if c.path == "" {
			return nil, 0
		}
	}

	// Read new data from file
	f, err := os.Open(c.path)
	if err != nil {
		return nil, len(c.messages)
	}
	defer f.Close()

	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return nil, len(c.messages)
	}
	newData, err := io.ReadAll(f)
	if err != nil {
		return nil, len(c.messages)
	}
	c.offset += int64(len(newData))

	if len(newData) == 0 {
		return nil, len(c.messages)
	}

	// Parse new lines
	var newMessages []map[string]any
	for _, line := range strings.Split(string(newData), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		parsed := parseTranscriptEntry(entry, c.toolUseNames, agentType)
		newMessages = append(newMessages, parsed...)
	}

	c.messages = append(c.messages, newMessages...)
	return newMessages, len(c.messages)
}

// ReadAllMessages reads any new data from the file and returns ALL accumulated
// messages (not just the new ones). Use this when the client needs the full
// conversation history (e.g. after=0).
func (r *SessionReader) ReadAllMessages(sessionID, workingDirectory, agentType string) ([]map[string]any, int) {
	// ReadNewMessages updates the cache with any new data
	r.ReadNewMessages(sessionID, workingDirectory, agentType)

	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.cache[sessionID]
	if c == nil {
		return nil, 0
	}
	return c.messages, len(c.messages)
}

// ClearSession removes cached state for a session.
func (r *SessionReader) ClearSession(sessionID string) {
	r.mu.Lock()
	delete(r.cache, sessionID)
	r.mu.Unlock()
}

// resolveTranscriptPath finds the transcript file for a session.
func resolveTranscriptPath(sessionID, workingDirectory, agentType string) string {
	switch agentType {
	case at.Gemini:
		return resolveGeminiTranscript(sessionID)
	case at.Codex:
		return resolveCodexTranscript(sessionID)
	default:
		return resolveClaudeTranscript(sessionID, workingDirectory)
	}
}

func resolveClaudeTranscript(sessionID, workingDirectory string) string {
	home, _ := os.UserHomeDir()
	basePath := os.Getenv("CLAUDE_PROJECTS_DIR")
	if basePath == "" {
		basePath = filepath.Join(home, ".claude", "projects")
	}

	// Try working directory hint first
	if workingDirectory != "" {
		encoded := strings.ReplaceAll(workingDirectory, "/", "-")
		candidate := filepath.Join(basePath, encoded, sessionID+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	// Search all project dirs
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(basePath, entry.Name(), sessionID+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func resolveGeminiTranscript(sessionID string) string {
	home, _ := os.UserHomeDir()
	basePath := filepath.Join(home, ".gemini", "tmp")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(basePath, entry.Name(), "chats", sessionID+".json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ""
}

func resolveCodexTranscript(sessionID string) string {
	home, _ := os.UserHomeDir()
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	basePath := filepath.Join(codexHome, "sessions")

	// Codex stores transcripts in YYYY/MM/DD/rollout-{timestamp}-{id}.jsonl
	// Walk the date-based directory structure to find the session
	matches, err := filepath.Glob(filepath.Join(basePath, "*", "*", "*", "rollout-*"+sessionID+"*.jsonl"))
	if err == nil && len(matches) > 0 {
		return matches[0]
	}

	// Fallback: search all rollout files for a matching session ID
	_ = filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.Contains(filepath.Base(path), sessionID) && strings.HasSuffix(path, ".jsonl") {
			matches = append(matches, path)
			return filepath.SkipAll
		}
		return nil
	})
	if len(matches) > 0 {
		return matches[0]
	}
	return resolveCodexTranscriptByMarker(basePath, sessionID)
}

func resolveCodexTranscriptByMarker(basePath, sessionID string) string {
	var match string
	_ = filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if !strings.HasPrefix(filepath.Base(path), "rollout-") || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if codexTranscriptCoralSessionID(path) == sessionID {
			match = path
			return filepath.SkipAll
		}
		return nil
	})
	return match
}

func codexTranscriptCoralSessionID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if sessionID := agent.ExtractCoralSessionID(entry); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

// parseTranscriptEntry converts a raw JSONL entry into normalized frontend messages.
func parseTranscriptEntry(entry map[string]any, toolUseNames map[string]string, agentType string) []map[string]any {
	switch agentType {
	case at.Gemini:
		return parseGeminiEntry(entry)
	case at.Codex:
		return parseCodexEntry(entry, toolUseNames)
	default:
		return parseClaudeEntry(entry, toolUseNames)
	}
}

var pulseRE = regexp.MustCompile(`\|\|PULSE:\w+[^|]*\|\|`)

func parseClaudeEntry(entry map[string]any, toolUseNames map[string]string) []map[string]any {
	etype, _ := entry["type"].(string)
	timestamp, _ := entry["timestamp"].(string)

	switch etype {
	case "user":
		return parseClaudeUserEntry(entry, timestamp, toolUseNames)
	case "assistant":
		return parseClaudeAssistantEntry(entry, timestamp, toolUseNames)
	}
	return nil
}

func parseClaudeUserEntry(entry map[string]any, timestamp string, toolUseNames map[string]string) []map[string]any {
	msg, _ := entry["message"].(map[string]any)
	if msg == nil {
		return nil
	}
	content := msg["content"]

	switch c := content.(type) {
	case string:
		if strings.TrimSpace(c) == "" || isSystemInjected(c) {
			return nil
		}
		return []map[string]any{{"type": "user", "timestamp": timestamp, "content": c}}

	case []any:
		var results []map[string]any
		var textParts []string

		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			bt, _ := b["type"].(string)
			if bt == "text" {
				if text, _ := b["text"].(string); text != "" {
					textParts = append(textParts, text)
				}
			} else if bt == "tool_result" {
				toolUseID, _ := b["tool_use_id"].(string)
				isError, _ := b["is_error"].(bool)
				resultContent := extractToolResultContent(b["content"])
				if resultContent == "" {
					continue
				}
				resultContent = truncateContent(resultContent)
				toolName := ""
				if toolUseID != "" {
					toolName = toolUseNames[toolUseID]
				}
				results = append(results, map[string]any{
					"type":        "tool_result",
					"timestamp":   timestamp,
					"content":     resultContent,
					"tool_name":   toolName,
					"tool_use_id": toolUseID,
					"is_error":    isError,
				})
			}
		}

		if len(results) > 0 {
			return results
		}
		if len(textParts) == 0 {
			return nil
		}
		text := strings.Join(textParts, "\n")
		if strings.TrimSpace(text) == "" {
			return nil
		}
		if isSystemInjected(text) {
			return nil
		}
		return []map[string]any{{"type": "user", "timestamp": timestamp, "content": text}}
	}
	return nil
}

// isSystemInjected detects user-role messages that contain system-injected
// content (task notifications, hook output, system reminders) and should
// be hidden from the chat view.
func isSystemInjected(content string) bool {
	systemTags := []string{
		"<system-reminder>",
		"<task-notification>",
		"<user-prompt-submit-hook>",
		"<available-deferred-tools>",
		"<fast_mode_info>",
	}
	for _, tag := range systemTags {
		if strings.Contains(content, tag) {
			return true
		}
	}
	return false
}

func extractToolResultContent(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, p := range c {
			if pm, ok := p.(map[string]any); ok {
				if pt, _ := pm["type"].(string); pt == "text" {
					if text, _ := pm["text"].(string); text != "" {
						parts = append(parts, text)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func parseClaudeAssistantEntry(entry map[string]any, timestamp string, toolUseNames map[string]string) []map[string]any {
	msg, _ := entry["message"].(map[string]any)
	if msg == nil {
		return nil
	}
	content := msg["content"]

	var text string
	var toolUses []map[string]any

	switch c := content.(type) {
	case string:
		text = c
	case []any:
		var textParts []string
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			bt, _ := b["type"].(string)
			if bt == "text" {
				if t, _ := b["text"].(string); t != "" {
					textParts = append(textParts, t)
				}
			} else if bt == "tool_use" {
				toolName, _ := b["name"].(string)
				toolUseID, _ := b["id"].(string)
				toolInput, _ := b["input"].(map[string]any)
				if toolInput == nil {
					toolInput = map[string]any{}
				}

				toolEntry := map[string]any{
					"name":          toolName,
					"tool_use_id":   toolUseID,
					"input_summary": summarizeToolInput(toolName, toolInput),
				}

				// Add extra fields for specific tools
				switch toolName {
				case "Bash":
					if cmd, _ := toolInput["command"].(string); cmd != "" {
						toolEntry["command"] = cmd
					}
					if desc, _ := toolInput["description"].(string); desc != "" {
						toolEntry["description"] = desc
					}
				case "AskUserQuestion":
					if q, ok := toolInput["questions"]; ok {
						toolEntry["questions"] = q
					}
				case "Edit":
					if old, _ := toolInput["old_string"].(string); old != "" {
						toolEntry["old_string"] = old
					}
					if new_, _ := toolInput["new_string"].(string); new_ != "" {
						toolEntry["new_string"] = new_
					}
				case "Write":
					if wc, _ := toolInput["content"].(string); wc != "" {
						toolEntry["write_content"] = truncateContent(wc)
					}
				}

				toolUses = append(toolUses, toolEntry)
				if toolUseID != "" {
					toolUseNames[toolUseID] = toolName
				}
			}
		}
		text = strings.Join(textParts, "\n")
	default:
		return nil
	}

	// Strip PULSE markers
	text = pulseRE.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)

	if text == "" && len(toolUses) == 0 {
		return nil
	}

	result := map[string]any{
		"type":      "assistant",
		"timestamp": timestamp,
		"text":      text,
	}
	if toolUses != nil {
		result["tool_uses"] = toolUses
	} else {
		result["tool_uses"] = []map[string]any{}
	}
	return []map[string]any{result}
}

func summarizeToolInput(name string, input map[string]any) string {
	switch name {
	case "Read", "Edit", "Write", "NotebookEdit":
		if fp, _ := input["file_path"].(string); fp != "" {
			return fp
		}
		if np, _ := input["notebook_path"].(string); np != "" {
			return np
		}
	case "Bash":
		if cmd, _ := input["command"].(string); cmd != "" {
			if len(cmd) > 120 {
				return cmd[:120] + "..."
			}
			return cmd
		}
	case "Grep", "Glob":
		pattern, _ := input["pattern"].(string)
		path, _ := input["path"].(string)
		if path != "" {
			return pattern + " in " + path
		}
		return pattern
	case "Agent":
		if desc, _ := input["description"].(string); desc != "" {
			return truncate(desc, 120)
		}
		if prompt, _ := input["prompt"].(string); prompt != "" {
			return truncate(prompt, 120)
		}
	case "TaskCreate", "TaskUpdate":
		if subj, _ := input["subject"].(string); subj != "" {
			return subj
		}
		if tid, _ := input["taskId"].(string); tid != "" {
			return tid
		}
	case "WebSearch":
		if q, _ := input["query"].(string); q != "" {
			return q
		}
	case "WebFetch":
		if u, _ := input["url"].(string); u != "" {
			return u
		}
	}
	// Fallback: first non-empty string value
	for _, v := range input {
		if s, ok := v.(string); ok && s != "" {
			return truncate(s, 100)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// truncateContent truncates large content blocks (tool results, file writes) to 10KB.
func truncateContent(s string) string {
	if len(s) > 10000 {
		return s[:10000] + "\n... (truncated)"
	}
	return s
}

// parseGeminiEntry handles Gemini JSON transcript format.
func parseGeminiEntry(entry map[string]any) []map[string]any {
	role, _ := entry["role"].(string)
	parts, _ := entry["parts"].([]any)
	timestamp, _ := entry["timestamp"].(string)

	if len(parts) == 0 {
		return nil
	}

	var textParts []string
	for _, p := range parts {
		if pm, ok := p.(map[string]any); ok {
			if text, _ := pm["text"].(string); text != "" {
				textParts = append(textParts, text)
			}
		}
	}

	text := strings.Join(textParts, "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}

	msgType := "user"
	if role == "model" {
		msgType = "assistant"
	}

	result := map[string]any{
		"type":      msgType,
		"timestamp": timestamp,
		"content":   text,
	}
	if msgType == "assistant" {
		result["text"] = text
		result["tool_uses"] = []map[string]any{}
	}
	return []map[string]any{result}
}

// parseCodexEntry handles Codex CLI JSONL transcript format.
// Codex JSONL entries have a "role" field ("user", "assistant", "system")
// and a "content" field that can be a string or array of content blocks.
// Tool calls appear as content blocks with type "function_call".
func parseCodexEntry(entry map[string]any, toolUseNames map[string]string) []map[string]any {
	if messages := parseCodexEventEntry(entry, toolUseNames); messages != nil {
		return messages
	}

	role, _ := entry["role"].(string)
	timestamp, _ := entry["timestamp"].(string)

	if role == "system" {
		return nil
	}

	content := entry["content"]

	switch role {
	case "user":
		return parseCodexUserEntry(content, timestamp, toolUseNames)
	case "assistant":
		return parseCodexAssistantEntry(content, timestamp, toolUseNames)
	}
	return nil
}

func parseCodexEventEntry(entry map[string]any, toolUseNames map[string]string) []map[string]any {
	entryType, _ := entry["type"].(string)
	timestamp, _ := entry["timestamp"].(string)
	payload, _ := entry["payload"].(map[string]any)
	if payload == nil {
		return nil
	}

	switch entryType {
	case "event_msg":
		payloadType, _ := payload["type"].(string)
		message, _ := payload["message"].(string)
		switch payloadType {
		case "user_message":
			return parseCodexUserEntry(message, timestamp, toolUseNames)
		case "agent_message":
			return parseCodexAssistantEntry(message, timestamp, toolUseNames)
		}
	}

	return nil
}

func parseCodexUserEntry(content any, timestamp string, toolUseNames map[string]string) []map[string]any {
	switch c := content.(type) {
	case string:
		if strings.TrimSpace(c) == "" || isSystemInjected(c) {
			return nil
		}
		return []map[string]any{{"type": "user", "timestamp": timestamp, "content": c}}
	case []any:
		var textParts []string
		var results []map[string]any
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			bt, _ := b["type"].(string)
			if bt == "text" || bt == "input_text" {
				if text, _ := b["text"].(string); text != "" {
					textParts = append(textParts, text)
				}
			} else if bt == "function_call_output" || bt == "tool_result" {
				callID, _ := b["call_id"].(string)
				if callID == "" {
					callID, _ = b["tool_use_id"].(string)
				}
				output, _ := b["output"].(string)
				if output == "" {
					output = extractToolResultContent(b["content"])
				}
				if output == "" {
					continue
				}
				output = truncateContent(output)
				toolName := ""
				if callID != "" {
					toolName = toolUseNames[callID]
				}
				results = append(results, map[string]any{
					"type":        "tool_result",
					"timestamp":   timestamp,
					"content":     output,
					"tool_name":   toolName,
					"tool_use_id": callID,
				})
			}
		}
		if len(results) > 0 {
			return results
		}
		if len(textParts) == 0 {
			return nil
		}
		text := strings.Join(textParts, "\n")
		if strings.TrimSpace(text) == "" || isSystemInjected(text) {
			return nil
		}
		return []map[string]any{{"type": "user", "timestamp": timestamp, "content": text}}
	}
	return nil
}

func parseCodexAssistantEntry(content any, timestamp string, toolUseNames map[string]string) []map[string]any {
	var text string
	var toolUses []map[string]any

	switch c := content.(type) {
	case string:
		text = c
	case []any:
		var textParts []string
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			bt, _ := b["type"].(string)
			if bt == "text" || bt == "output_text" {
				if t, _ := b["text"].(string); t != "" {
					textParts = append(textParts, t)
				}
			} else if bt == "function_call" {
				fnName, _ := b["name"].(string)
				callID, _ := b["call_id"].(string)
				args, _ := b["arguments"].(string)
				toolEntry := map[string]any{
					"name":          fnName,
					"tool_use_id":   callID,
					"input_summary": truncate(fnName+": "+args, 200),
				}
				// Parse arguments JSON for common tool fields
				if args != "" {
					var argMap map[string]any
					if json.Unmarshal([]byte(args), &argMap) == nil {
						if cmd, _ := argMap["command"].(string); cmd != "" {
							toolEntry["command"] = cmd
						}
						if fp, _ := argMap["file_path"].(string); fp != "" {
							toolEntry["input_summary"] = fnName + ": " + fp
						}
					}
				}
				toolUses = append(toolUses, toolEntry)
				if callID != "" {
					toolUseNames[callID] = fnName
				}
			}
		}
		text = strings.Join(textParts, "\n")
	default:
		return nil
	}

	// Strip PULSE markers
	text = pulseRE.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)

	if text == "" && len(toolUses) == 0 {
		return nil
	}

	result := map[string]any{
		"type":      "assistant",
		"timestamp": timestamp,
		"content":   text,
		"text":      text,
		"tool_uses": toolUses,
	}
	return []map[string]any{result}
}
