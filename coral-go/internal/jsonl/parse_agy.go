package jsonl

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	agyUserRequestRE = regexp.MustCompile(`(?s)<USER_REQUEST>(.*?)</USER_REQUEST>`)
	agyMetadataRE    = regexp.MustCompile(`(?s)<(?:ADDITIONAL_METADATA|USER_SETTINGS_CHANGE|CONTEXT_SUMMARY|system-reminder|environment_context)>.*?</(?:ADDITIONAL_METADATA|USER_SETTINGS_CHANGE|CONTEXT_SUMMARY|system-reminder|environment_context)>`)
	coralMetaBlockRE = regexp.MustCompile(`(?s)Coral session metadata:.*?This metadata is for Coral bookkeeping only\. Do not mention it to the user\.\s*`)
	coralMarkerRE    = regexp.MustCompile(`\[CORAL_SESSION_ID:[^\]]+\]\s*`)
	agySlashNudgeRE  = regexp.MustCompile(`(?i)^(?:/[a-zA-Z0-9_-]+\s+)+(?:@[^@\n\r]+?\s+)?(You have \d+ unread message|You have tasks available|You have a new task in Coral|\[Task #\d+|\[Wait resolved\]|\[Coral\b)`)
	agySlashCmdRE    = regexp.MustCompile(`^(?:/[a-zA-Z0-9_-]+\s+)+`)
)

func cleanAgyUserInput(content string) string {
	if m := agyUserRequestRE.FindStringSubmatch(content); len(m) > 1 {
		content = m[1]
	} else {
		content = agyMetadataRE.ReplaceAllString(content, "")
	}
	content = coralMetaBlockRE.ReplaceAllString(content, "")
	content = coralMarkerRE.ReplaceAllString(content, "")
	content = strings.TrimSpace(content)
	for agySlashNudgeRE.MatchString(content) {
		content = strings.TrimSpace(agySlashCmdRE.ReplaceAllString(content, ""))
	}
	return content
}

func cleanAgyArgString(raw any) string {
	switch v := raw.(type) {
	case string:
		if strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) && len(v) >= 2 {
			var unquoted string
			if err := json.Unmarshal([]byte(v), &unquoted); err == nil {
				return unquoted
			}
		}
		return v
	case fmt.Stringer:
		return v.String()
	default:
		if raw == nil {
			return ""
		}
		return fmt.Sprintf("%v", raw)
	}
}

// parseAgyEntry handles Antigravity CLI (agy) JSONL transcript format,
// falling back to legacy Gemini JSON array format when needed.
func parseAgyEntry(entry map[string]any, toolUseNames map[string]string) []map[string]any {
	if _, hasType := entry["type"]; !hasType {
		if _, hasParts := entry["parts"]; hasParts {
			return parseGeminiEntry(entry)
		}
	}

	etype, _ := entry["type"].(string)
	timestamp, _ := entry["created_at"].(string)
	if timestamp == "" {
		timestamp, _ = entry["timestamp"].(string)
	}

	switch etype {
	case "USER_INPUT":
		return agyUserInput(entry, timestamp)
	case "PLANNER_RESPONSE":
		return agyPlannerResponse(entry, timestamp, toolUseNames)
	case "GENERIC":
		return agyToolResult(entry, timestamp, toolUseNames)
	}

	if _, ok := entry["role"]; ok {
		return parseGeminiEntry(entry)
	}
	return nil
}

// agyUserInput is a person's message, cleaned of what agy and Coral add to it (nothing for a system-injected one).
func agyUserInput(entry map[string]any, timestamp string) []map[string]any {
	content, _ := entry["content"].(string)
	cleaned := cleanAgyUserInput(content)
	if cleaned == "" || isSystemInjected(cleaned) {
		return nil
	}
	return []map[string]any{{"type": "user", "timestamp": timestamp, "content": cleaned}}
}

// agyPlannerResponse is the agent's reply and its tool calls; each call is noted in toolUseNames for the results
// that follow.
func agyPlannerResponse(entry map[string]any, timestamp string, toolUseNames map[string]string) []map[string]any {
	content, _ := entry["content"].(string)
	var toolUses []map[string]any
	if tcs, ok := entry["tool_calls"].([]any); ok {
		for i, rawTc := range tcs {
			tc, ok := rawTc.(map[string]any)
			if !ok {
				continue
			}
			toolUses = append(toolUses, agyToolUse(entry, i, tc, toolUseNames))
		}
	}
	if content == "" && len(toolUses) == 0 {
		return nil
	}
	return []map[string]any{{
		"type":      "assistant",
		"timestamp": timestamp,
		"content":   content,
		"text":      content,
		"tool_uses": toolUses,
	}}
}

// agyToolUse is the i-th tool call of a reply: its name and id (recorded in toolUseNames, with the last call's), what
// it does, and an input summary.
func agyToolUse(entry map[string]any, i int, tc map[string]any, toolUseNames map[string]string) map[string]any {
	name, _ := tc["name"].(string)
	callID := fmt.Sprintf("step-%v-%d", entry["step_index"], i)
	toolUseNames[callID] = name
	toolUseNames["__last_call_id"] = callID
	toolUseNames["__last_tool_name"] = name

	args, _ := tc["args"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}

	toolEntry := map[string]any{
		"name":        name,
		"tool_use_id": callID,
	}

	desc := cleanAgyArgString(args["toolAction"])
	if desc == "" {
		desc = cleanAgyArgString(args["toolSummary"])
	}
	if desc == "" {
		desc = cleanAgyArgString(args["Description"])
	}
	if desc != "" {
		toolEntry["description"] = desc
	}

	agyToolDetails(name, args, toolEntry)
	if toolEntry["input_summary"] == nil || toolEntry["input_summary"] == "" {
		toolEntry["input_summary"] = agyFallbackSummary(name, args, desc)
	}
	return toolEntry
}

// agyToolDetails adds what each known tool shows: its command, edit, written content, file and lines, URL, query,
// questions or recipient, and an input summary from them.
func agyToolDetails(name string, args, toolEntry map[string]any) {
	switch name {
	case "run_command":
		cmd := cleanAgyArgString(args["CommandLine"])
		if cmd != "" {
			toolEntry["command"] = cmd
			toolEntry["input_summary"] = truncate(cmd, 200)
		}
	case "replace_file_content":
		targetFile := cleanAgyArgString(args["TargetFile"])
		if oldStr := cleanAgyArgString(args["TargetContent"]); oldStr != "" {
			toolEntry["old_string"] = oldStr
		}
		if newStr := cleanAgyArgString(args["ReplacementContent"]); newStr != "" {
			toolEntry["new_string"] = newStr
		}
		if targetFile != "" {
			toolEntry["input_summary"] = targetFile
		}
	case "write_to_file":
		targetFile := cleanAgyArgString(args["TargetFile"])
		if code := cleanAgyArgString(args["CodeContent"]); code != "" {
			toolEntry["write_content"] = truncateContent(code)
		}
		if targetFile != "" {
			toolEntry["input_summary"] = targetFile
		}
	case "view_file":
		if path := cleanAgyArgString(args["AbsolutePath"]); path != "" {
			startLine := cleanAgyArgString(args["StartLine"])
			endLine := cleanAgyArgString(args["EndLine"])
			if startLine != "" && endLine != "" {
				toolEntry["input_summary"] = fmt.Sprintf("%s:%s-%s", path, startLine, endLine)
			} else if startLine != "" {
				toolEntry["input_summary"] = fmt.Sprintf("%s:%s", path, startLine)
			} else {
				toolEntry["input_summary"] = path
			}
		}
	case "read_url_content":
		if u := cleanAgyArgString(args["Url"]); u != "" {
			toolEntry["input_summary"] = u
		}
	case "search_web":
		if q := cleanAgyArgString(args["query"]); q != "" {
			toolEntry["input_summary"] = q
		}
	case "ask_question":
		if rawQ, ok := args["questions"]; ok {
			toolEntry["questions"] = agyQuestions(rawQ)
		}
	case "invoke_subagent", "send_message", "manage_subagents":
		if rec := cleanAgyArgString(args["Recipient"]); rec != "" {
			toolEntry["input_summary"] = rec
		} else if tn := cleanAgyArgString(args["TypeName"]); tn != "" {
			toolEntry["input_summary"] = tn
		} else if role := cleanAgyArgString(args["Role"]); role != "" {
			toolEntry["input_summary"] = role
		}
	}
}

// agyQuestions is ask_question's questions: a JSON array sent as a string is decoded, anything else kept as sent.
func agyQuestions(rawQ any) any {
	if qStr, ok := rawQ.(string); ok && strings.HasPrefix(strings.TrimSpace(qStr), "[") {
		var parsed any
		if err := json.Unmarshal([]byte(qStr), &parsed); err == nil {
			return parsed
		}
		return qStr
	}
	return rawQ
}

// agyFallbackSummary is the input summary of a call its tool's own fields left without one: what it does, else the
// first of the usual path, command, URL or text arguments, else its arguments as JSON, else its name.
func agyFallbackSummary(name string, args map[string]any, desc string) any {
	if desc != "" {
		return desc
	}
	for _, key := range []string{"TargetFile", "AbsolutePath", "CommandLine", "Url", "query", "Prompt", "Message", "path"} {
		if val := cleanAgyArgString(args[key]); val != "" {
			return truncate(val, 200)
		}
	}
	if b, err := json.Marshal(args); err == nil && len(args) > 0 {
		return truncate(name+": "+string(b), 200)
	}
	return name
}

// agyToolResult is a tool's output, matched to its call: the call one step before it, else the last call made.
func agyToolResult(entry map[string]any, timestamp string, toolUseNames map[string]string) []map[string]any {
	content, _ := entry["content"].(string)
	if strings.TrimSpace(content) == "" {
		return nil
	}
	var callID, toolName string
	if stepIdx, ok := entry["step_index"].(float64); ok {
		candidateID := fmt.Sprintf("step-%d-0", int(stepIdx)-1)
		if name, ok := toolUseNames[candidateID]; ok {
			callID = candidateID
			toolName = name
		}
	}
	if callID == "" {
		callID = toolUseNames["__last_call_id"]
		toolName = toolUseNames["__last_tool_name"]
	}
	isError := false
	if status, _ := entry["status"].(string); status == "ERROR" {
		isError = true
	}
	return []map[string]any{{
		"type":        "tool_result",
		"timestamp":   timestamp,
		"content":     truncateContent(content),
		"tool_name":   toolName,
		"tool_use_id": callID,
		"is_error":    isError,
	}}
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
