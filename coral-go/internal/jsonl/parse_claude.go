package jsonl

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/subgentic/coral/internal/agent"
	at "github.com/subgentic/coral/internal/agenttypes"
	"github.com/subgentic/coral/pkg/redact"
)

// parseTranscriptEntry converts a raw JSONL entry into normalized frontend messages.
func parseTranscriptEntry(entry map[string]any, toolUseNames map[string]string, agentType string) []map[string]any {
	switch agentType {
	case at.Agy, at.Antigravity, at.Gemini:
		return parseAgyEntry(entry, toolUseNames)
	case at.Codex:
		return parseCodexEntry(entry, toolUseNames)
	default:
		return parseClaudeEntry(entry, toolUseNames)
	}
}

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
	case "system":
		return parseClaudeLocalCommandEntry(entry, timestamp)
	}
	return nil
}

// parseClaudeLocalCommandEntry reads a slash command Claude Code ran by itself ("/model sonnet", "/effort high"): the transcript records it as a
// system entry (subtype local_command) with <command-name> and <command-args>, never as a user turn, so what the person typed did not appear in the
// chat and its pending bubble waited for ever (#5488). It is a user turn flagged local_command, with the text as it was typed ("/model sonnet"),
// which the page shows as a small note and which settles the pending message. The output entry (<local-command-stdout>) is not a turn.
func parseClaudeLocalCommandEntry(entry map[string]any, timestamp string) []map[string]any {
	if sub, _ := entry["subtype"].(string); sub != "local_command" {
		return nil
	}
	content, _ := entry["content"].(string)
	turns, _ := localCommandTurn(content, timestamp)
	return turns
}

// localCommandTurn is the chat turn for an entry's text when that text is a slash command's own entry: the command as it was typed ("/effort low") as a note-flagged user turn, and nothing for its
// output entry. The second result says the text was one of the two (so the caller does not show it as a message). Claude Code 2.1.309 writes both as type "user" entries, where they showed as
// a bubble "/effort effort low" and a bubble of the output (#6219, finding 4).
func localCommandTurn(content, timestamp string) ([]map[string]any, bool) {
	if !localCommandEntryR.MatchString(content) {
		return nil, false
	}
	m := localCommandNameR.FindStringSubmatch(content)
	if m == nil {
		return nil, true // the command's output entry: not a turn
	}
	text := m[1]
	if a := localCommandArgsR.FindStringSubmatch(content); a != nil {
		if args := cleanCommandText(a[1], 120); args != "" {
			text += " " + args
		}
	}
	return []map[string]any{{"type": "user", "timestamp": timestamp, "content": text, "local_command": true}}, true
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
		if turns, ok := localCommandTurn(c, timestamp); ok { // 2.1.309: a slash command and its output are user entries (#6219)
			return turns
		}
		if isMeta || strings.TrimSpace(c) == "" || isSystemInjected(c) {
			return nil
		}
		return []map[string]any{{"type": "user", "timestamp": timestamp, "content": c}}

	case []any:
		var results []map[string]any
		var textParts []string
		var images []map[string]any // a pasted image: its chip (#5032)

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
			} else if bt == "image" {
				if src, _ := b["source"].(map[string]any); src != nil {
					if data, _ := src["data"].(string); data != "" && src["type"] == "base64" {
						images = append(images, map[string]any{"name": fmt.Sprintf("[Image #%d]", len(images)+1), "file": "", "ref": agent.ImageRef(data)})
					}
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
		if isMeta || len(textParts) == 0 && len(images) == 0 {
			return nil
		}
		text := strings.Join(textParts, "\n")
		if strings.TrimSpace(text) == "" {
			if len(images) == 0 {
				return nil
			}
			labels := make([]string, len(images)) // an image-only message reads as its labels, as Codex's does
			for i, a := range images {
				labels[i], _ = a["name"].(string)
			}
			text = strings.Join(labels, " ")
		}
		if isSystemInjected(text) {
			return nil
		}
		msg := map[string]any{"type": "user", "timestamp": timestamp, "content": text}
		if len(images) > 0 {
			msg["attachments"] = images
		}
		return []map[string]any{msg}
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

// A field cut short never ends in the start of a token (redact.Cut): the Coral Hub
// redactor knows whole tokens only, and would pass a token's first characters (#3723).
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:redact.Cut(s, n)] + "..."
	}
	return s
}

// truncateContent truncates large content blocks (tool results, file writes) to 10KB.
func truncateContent(s string) string {
	if len(s) > 10000 {
		return s[:redact.Cut(s, 10000)] + "\n... (truncated)"
	}
	return s
}

// runeCut is the largest index <= n where s can be cut without splitting a UTF-8 sequence
// (#3694): a byte cut inside one left bytes the JSON encoder could only show as U+FFFD. The text
// is cut decoded, before it is encoded, so a cut never falls inside a JSON escape.
func runeCut(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for k := n; k > 0 && n-k < utf8.UTFMax; k-- {
		if utf8.RuneStart(s[k]) {
			return k
		}
	}
	return n
}
