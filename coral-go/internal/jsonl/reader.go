// Package jsonl provides incremental JSONL reading for live agent session transcripts.
package jsonl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cdknorow/coral/internal/agent"
	at "github.com/cdknorow/coral/internal/agenttypes"
)

// SessionReader incrementally reads JSONL session files for live chat display.
type SessionReader struct {
	mu           sync.Mutex
	cache        map[string]*sessionCache
	firstPrompts map[string]*firstPromptCache
}

type sessionCache struct {
	path         string
	offset       int64
	messages     []map[string]any
	toolUseNames map[string]string // tool_use_id → tool_name
}

// firstPromptCache is deliberately separate from sessionCache. The live
// sessions list needs one small identity string, not a pinned copy of every
// parsed transcript message.
type firstPromptCache struct {
	path   string
	offset int64
	prompt string
}

// NewSessionReader creates a new JSONL session reader.
func NewSessionReader() *SessionReader {
	return &SessionReader{
		cache:        make(map[string]*sessionCache),
		firstPrompts: make(map[string]*firstPromptCache),
	}
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
		for _, message := range parsed {
			var previous map[string]any
			if len(newMessages) > 0 {
				previous = newMessages[len(newMessages)-1]
			} else if len(c.messages) > 0 {
				previous = c.messages[len(c.messages)-1]
			}
			if agentType == at.Codex && mergeDuplicateCodexMessage(previous, message) {
				continue
			}
			newMessages = append(newMessages, message)
		}
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

// FirstUserPrompt returns the earliest non-empty user message in a session.
// Messages are read through the normal parser so system-injected user content
// is excluded consistently with the chat transcript.
func (r *SessionReader) FirstUserPrompt(sessionID, workingDirectory, agentType string) string {
	if agentType == at.Terminal {
		return ""
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	c := r.firstPrompts[sessionID]
	if c == nil {
		c = &firstPromptCache{}
		r.firstPrompts[sessionID] = c
	}
	if c.prompt != "" {
		return c.prompt
	}
	if c.path == "" {
		c.path = resolveTranscriptPath(sessionID, workingDirectory, agentType)
		if c.path == "" {
			return ""
		}
	}

	f, err := os.Open(c.path)
	if err != nil {
		return ""
	}
	defer f.Close()

	if info, err := f.Stat(); err == nil && info.Size() < c.offset {
		c.offset = 0
	}
	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return ""
	}
	reader := bufio.NewReader(f)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) == 0 {
			return ""
		}
		trimmed := strings.TrimSpace(string(line))
		if trimmed == "" {
			c.offset += int64(len(line))
			if readErr != nil {
				return ""
			}
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(trimmed), &entry); err != nil {
			// A final record may still be in the middle of being written. Leave
			// the offset at its start so the next call retries the completed line.
			if readErr == io.EOF && line[len(line)-1] != '\n' {
				return ""
			}
			c.offset += int64(len(line))
			if readErr != nil {
				return ""
			}
			continue
		}
		c.offset += int64(len(line))
		for _, message := range parseTranscriptEntry(entry, map[string]string{}, agentType) {
			if message["type"] != "user" {
				continue
			}
			content, _ := message["content"].(string)
			if content = strings.TrimSpace(content); content != "" {
				c.prompt = content
				return c.prompt
			}
		}
		if readErr != nil {
			return ""
		}
	}
}

// ClearSession removes cached state for a session.
func (r *SessionReader) ClearSession(sessionID string) {
	r.mu.Lock()
	delete(r.cache, sessionID)
	delete(r.firstPrompts, sessionID)
	r.mu.Unlock()
}

// ReadFrom parses the complete lines of a transcript file from offset on,
// and returns the offset after the last complete line, so a line still
// being written is read on the next call. An offset past the end of the
// file (it was truncated or replaced) starts over from the top.
func ReadFrom(path, agentType string, offset int64) ([]map[string]any, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if offset > info.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, err
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return nil, offset, nil
	}
	toolUseNames := make(map[string]string)
	var messages []map[string]any
	for _, line := range bytes.Split(data[:end], []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		for _, message := range parseTranscriptEntry(entry, toolUseNames, agentType) {
			var previous map[string]any
			if len(messages) > 0 {
				previous = messages[len(messages)-1]
			}
			if agentType == at.Codex && mergeDuplicateCodexMessage(previous, message) {
				continue
			}
			messages = append(messages, message)
		}
	}
	return messages, offset + int64(end) + 1, nil
}

// TranscriptPath returns the transcript file for a session, or "" when the
// agent has not written one yet.
func TranscriptPath(sessionID, workingDirectory, agentType string) string {
	return resolveTranscriptPath(sessionID, workingDirectory, agentType)
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
	case "attachment":
		return parseClaudeAttachmentEntry(entry, timestamp)
	}
	return nil
}

// parseClaudeAttachmentEntry reads a message the user sent while the agent was
// mid-turn. Claude Code logs it as a queued_command attachment rather than a
// user entry, so without this it never appears in the chat.
func parseClaudeAttachmentEntry(entry map[string]any, timestamp string) []map[string]any {
	att, _ := entry["attachment"].(map[string]any)
	if att == nil {
		return nil
	}
	if kind, _ := att["type"].(string); kind != "queued_command" {
		return nil
	}
	// Only what a person typed: skip commands queued by the system (task
	// notifications and the like).
	if origin, ok := att["origin"].(map[string]any); ok {
		if k, _ := origin["kind"].(string); k != "human" {
			return nil
		}
	}
	if mode, ok := att["commandMode"].(string); ok && mode != "prompt" {
		return nil
	}
	prompt, _ := att["prompt"].(string)
	if strings.TrimSpace(prompt) == "" || isSystemInjected(prompt) {
		return nil
	}
	return []map[string]any{{"type": "user", "timestamp": timestamp, "content": prompt}}
}

func parseClaudeUserEntry(entry map[string]any, timestamp string, toolUseNames map[string]string) []map[string]any {
	msg, _ := entry["message"].(map[string]any)
	if msg == nil {
		return nil
	}
	content := msg["content"]
	// isMeta marks text Claude Code injected itself (a loaded skill's
	// instructions, command expansions). It is not something the user typed,
	// so it never becomes a user message; tool results are still read.
	isMeta, _ := entry["isMeta"].(bool)

	switch c := content.(type) {
	case string:
		if isMeta || strings.TrimSpace(c) == "" || isSystemInjected(c) {
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
		if isMeta || len(textParts) == 0 {
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
		"<environment_context>",
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

	payloadType, _ := payload["type"].(string)
	switch entryType {
	case "event_msg":
		message, _ := payload["message"].(string)
		switch payloadType {
		case "user_message":
			return parseCodexUserEntry(message, timestamp, toolUseNames)
		case "agent_message":
			messages := parseCodexAssistantEntry(message, timestamp, toolUseNames)
			phase, _ := payload["phase"].(string)
			if phase != "" {
				for _, msg := range messages {
					msg["phase"] = phase
				}
			}
			return messages
		}
	case "response_item":
		name, _ := payload["name"].(string)
		callID, _ := payload["call_id"].(string)
		switch payloadType {
		case "message":
			role, _ := payload["role"].(string)
			var messages []map[string]any
			switch role {
			case "user":
				messages = parseCodexUserEntry(payload["content"], timestamp, toolUseNames)
			case "assistant":
				messages = parseCodexAssistantEntry(payload["content"], timestamp, toolUseNames)
			}
			phase, _ := payload["phase"].(string)
			if phase != "" {
				for _, msg := range messages {
					msg["phase"] = phase
				}
			}
			return messages
		case "function_call", "custom_tool_call":
			var tool map[string]any
			if payloadType == "function_call" {
				args, _ := payload["arguments"].(string)
				tool = codexFunctionCall(name, callID, args)
			} else {
				input, _ := payload["input"].(string)
				tool = codexCustomToolCall(name, callID, input)
			}
			if callID != "" {
				toolName := name
				if operation, _ := tool["operation"].(string); operation != "" {
					toolName = operation
				}
				toolUseNames[callID] = toolName
			}
			return []map[string]any{{
				"type":      "assistant",
				"timestamp": timestamp,
				"content":   "",
				"text":      "",
				"tool_uses": []map[string]any{tool},
			}}
		case "function_call_output", "custom_tool_call_output":
			output := codexToolOutput(payload["output"])
			if output == "" {
				return nil
			}
			return []map[string]any{{
				"type":        "tool_result",
				"timestamp":   timestamp,
				"content":     truncateContent(output),
				"tool_name":   toolUseNames[callID],
				"tool_use_id": callID,
			}}
		}
	}

	return nil
}

// Codex has used both event_msg and response_item records for the same visible
// message. Some versions emit both a few milliseconds apart, while newer
// versions emit only response_item. Merge only adjacent, equal messages in a
// tight time window so both formats work without hiding intentional repeats.
func mergeDuplicateCodexMessage(previous, current map[string]any) bool {
	if previous == nil || current == nil || previous["type"] != current["type"] {
		return false
	}
	kind, _ := current["type"].(string)
	var previousText, currentText string
	switch kind {
	case "user":
		previousText, _ = previous["content"].(string)
		currentText, _ = current["content"].(string)
	case "assistant":
		previousText, _ = previous["text"].(string)
		currentText, _ = current["text"].(string)
	default:
		return false
	}
	if previousText == "" || previousText != currentText {
		return false
	}
	previousAt, previousErr := time.Parse(time.RFC3339Nano, stringValue(previous["timestamp"]))
	currentAt, currentErr := time.Parse(time.RFC3339Nano, stringValue(current["timestamp"]))
	if previousErr != nil || currentErr != nil || currentAt.Sub(previousAt).Abs() > time.Second {
		return false
	}
	if previous["phase"] == nil && current["phase"] != nil {
		previous["phase"] = current["phase"]
	}
	return true
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func codexFunctionCall(name, callID, args string) map[string]any {
	tool := map[string]any{
		"name":          name,
		"tool_use_id":   callID,
		"input_summary": truncate(name+": "+args, 200),
	}
	var argMap map[string]any
	if args == "" || json.Unmarshal([]byte(args), &argMap) != nil {
		return tool
	}
	// exec_command sends "cmd"; the older shell tool sends "command" as an argv list.
	cmd, _ := argMap["cmd"].(string)
	if cmd == "" {
		switch c := argMap["command"].(type) {
		case string:
			cmd = c
		case []any:
			parts := make([]string, 0, len(c))
			for _, p := range c {
				if ps, ok := p.(string); ok {
					parts = append(parts, ps)
				}
			}
			cmd = strings.Join(parts, " ")
		}
	}
	if cmd != "" {
		tool["command"] = cmd
		tool["input_summary"] = truncate(cmd, 200)
	}
	if strings.HasSuffix(name, "request_user_input") {
		if questions, ok := argMap["questions"]; ok {
			tool["questions"] = questions
		}
	}
	// Codex's composite functions.exec tool carries JavaScript in "input".
	// Keep the raw source out of the collapsed row and summarize the nested
	// operation instead; the full arguments are still available in the rollout.
	if input, _ := argMap["input"].(string); input != "" {
		tool["input_summary"] = summarizeCodexCompositeInput(input)
		if operations := codexNestedOperations(input); len(operations) == 1 {
			tool["operation"] = operations[0]
		}
	}
	for _, key := range []string{"file_path", "path"} {
		if fp, _ := argMap[key].(string); fp != "" {
			tool["input_summary"] = fp
			break
		}
	}
	return tool
}

var codexPatchFileRE = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$`)
var codexNestedToolRE = regexp.MustCompile(`tools\.([A-Za-z0-9_]+)`)

func codexCustomToolCall(name, callID, input string) map[string]any {
	tool := map[string]any{
		"name":          name,
		"tool_use_id":   callID,
		"input_summary": truncate(name+": "+input, 200),
	}
	if name == "exec" {
		tool["input_summary"] = summarizeCodexCompositeInput(input)
		if operations := codexNestedOperations(input); len(operations) == 1 {
			tool["operation"] = operations[0]
		}
	}
	if name == "apply_patch" {
		var files []string
		for _, m := range codexPatchFileRE.FindAllStringSubmatch(input, -1) {
			files = append(files, strings.TrimSpace(m[1]))
		}
		if len(files) > 0 {
			tool["input_summary"] = truncate(strings.Join(files, ", "), 200)
		}
		tool["patch"] = truncateContent(input)
	}
	return tool
}

func summarizeCodexCompositeInput(input string) string {
	operations := codexNestedOperations(input)
	if len(operations) == 1 {
		return strings.ReplaceAll(operations[0], "__", " ")
	}
	if len(operations) > 1 {
		return fmt.Sprintf("%d tool calls", len(operations))
	}
	return "tool call"
}

func codexNestedOperations(input string) []string {
	matches := codexNestedToolRE.FindAllStringSubmatch(input, -1)
	operations := make([]string, 0, len(matches))
	for _, match := range matches {
		operations = append(operations, match[1])
	}
	return operations
}

// codexToolOutput accepts current content-block arrays, plain strings, and the
// JSON-encoded {"output": ...} wrapper used by older shell calls.
func codexToolOutput(raw any) string {
	if blocks, ok := raw.([]any); ok {
		var parts []string
		for _, block := range blocks {
			item, ok := block.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := item["type"].(string)
			switch kind {
			case "text", "input_text", "output_text":
				if value, _ := item["text"].(string); value != "" {
					parts = append(parts, value)
				}
			case "image", "input_image", "output_image":
				parts = append(parts, "[image]")
			}
		}
		return strings.Join(parts, "")
	}
	out, _ := raw.(string)
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		var wrapped map[string]any
		if json.Unmarshal([]byte(out), &wrapped) == nil {
			if inner, ok := wrapped["output"].(string); ok {
				return inner
			}
		}
	}
	return out
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
				toolUses = append(toolUses, codexFunctionCall(fnName, callID, args))
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
