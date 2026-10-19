package jsonl

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cdknorow/coral/internal/agent"
)

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
			var atts []agent.CodexAttachment
			if imgs, ok := payload["images"].([]any); ok {
				atts = agent.CodexPayloadImages(imgs)
			}
			messages := parseCodexUserEntry(agent.CodexUserMessageText(message, atts), timestamp, toolUseNames)
			setCodexAttachments(messages, atts)
			return messages
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
				_, atts := agent.CodexUserContent(payload["content"])
				setCodexAttachments(messages, atts)
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
	// The event_msg copy holds a data URL (no file name); the response_item copy names the file.
	if previous["attachments"] == nil || !attachmentsHaveFile(previous["attachments"]) && attachmentsHaveFile(current["attachments"]) {
		if current["attachments"] != nil {
			previous["attachments"] = current["attachments"]
		}
	}
	return true
}

// setCodexAttachments puts a user message's pasted images on it as
// attachments: [{name: "[Image #1]", file: "image.png"}].
func setCodexAttachments(messages []map[string]any, atts []agent.CodexAttachment) {
	if len(atts) == 0 || len(messages) != 1 || messages[0]["type"] != "user" {
		return
	}
	list := make([]map[string]any, len(atts))
	for i, a := range atts {
		list[i] = map[string]any{"name": a.Name, "file": a.File}
	}
	messages[0]["attachments"] = list
}

func attachmentsHaveFile(v any) bool {
	list, _ := v.([]map[string]any)
	for _, a := range list {
		if f, _ := a["file"].(string); f != "" {
			return true
		}
	}
	return false
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
		text, _ := agent.CodexUserContent(c)
		var results []map[string]any
		for _, block := range c {
			b, ok := block.(map[string]any)
			if !ok {
				continue
			}
			bt, _ := b["type"].(string)
			if bt == "function_call_output" || bt == "tool_result" {
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
