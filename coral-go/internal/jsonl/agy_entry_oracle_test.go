package jsonl

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseAgyEntry, split into one helper per entry type and the tool call's parts (#4713), reads every entry as
// parseAgyEntryBefore4713 (the function at v1.4.92, below) does: the same messages, and the same call names noted for
// the results that follow. Each seed is one transcript (the names carry from entry to entry). Each kind of entry, tool,
// description source and input-summary fallback has a floor, so none goes unexercised.

var agyOracleTools = []string{"run_command", "replace_file_content", "write_to_file", "view_file", "read_url_content", "search_web",
	"ask_question", "invoke_subagent", "send_message", "manage_subagents", "list_dir", "", "custom_tool"}

var agyOracleArgKeys = []string{"toolAction", "toolSummary", "Description", "CommandLine", "TargetFile", "TargetContent", "ReplacementContent",
	"CodeContent", "AbsolutePath", "StartLine", "EndLine", "Url", "query", "questions", "Recipient", "TypeName", "Role", "Prompt", "Message", "path", "other"}

type agyStringer struct{}

func (agyStringer) String() string { return "stringer value" }

func randomAgyArg(rng *rand.Rand, key string) any {
	if key == "CodeContent" && rng.Intn(4) == 0 {
		return strings.Repeat("y", 10050) // over truncateContent's 10000
	}
	if key == "questions" && rng.Intn(2) == 0 {
		return `[{"q":"which?","options":["a","b"]}]` // a JSON array sent as a string: decoded
	}
	switch rng.Intn(10) {
	case 0:
		return nil
	case 1:
		return ""
	case 2:
		return `"quoted \"value\""` // agy sends some arguments JSON-quoted
	case 3:
		return float64(rng.Intn(400))
	case 4:
		return agyStringer{}
	case 5:
		return strings.Repeat("long ", 60) + key
	case 6:
		if key == "questions" {
			return []string{`[{"q":"which?","options":["a","b"]}]`, `[not json`, ` ["x"]`}[rng.Intn(3)]
		}
		return []any{"a", 1.0}
	case 7:
		return `"unterminated`
	case 8:
		if rng.Intn(2) == 0 {
			return strings.Repeat("y", 10050) // over truncateContent's 10000
		}
	}
	return key + " value"
}

func randomAgyEntry(rng *rand.Rand) map[string]any {
	e := map[string]any{}
	switch rng.Intn(10) {
	case 0, 1:
		e["type"] = "USER_INPUT"
		e["content"] = []string{"please fix the build", "", "  ", "<USER_REQUEST>do it</USER_REQUEST>", "[Coral] system note", "/clear",
			"<task-notification>done</task-notification> hi"}[rng.Intn(7)]
	case 2, 3, 4, 5:
		e["type"] = "PLANNER_RESPONSE"
		if rng.Intn(3) > 0 {
			e["content"] = []string{"working on it", ""}[rng.Intn(2)]
		}
		if rng.Intn(5) > 0 {
			var tcs []any
			for k := rng.Intn(4); k > 0; k-- {
				if rng.Intn(10) == 0 {
					tcs = append(tcs, "not a call")
					continue
				}
				tc := map[string]any{"name": agyOracleTools[rng.Intn(len(agyOracleTools))]}
				if rng.Intn(8) > 0 {
					args := map[string]any{}
					for _, i := range rng.Perm(len(agyOracleArgKeys))[:rng.Intn(6)] {
						args[agyOracleArgKeys[i]] = randomAgyArg(rng, agyOracleArgKeys[i])
					}
					tc["args"] = args
				}
				tcs = append(tcs, tc)
			}
			e["tool_calls"] = tcs
		}
	case 6, 7:
		e["type"] = "GENERIC"
		e["content"] = []string{"output line", "", "   ", strings.Repeat("x", 3000)}[rng.Intn(4)]
		if rng.Intn(4) == 0 {
			e["status"] = "ERROR"
		}
	case 8:
		e["type"] = []string{"UNKNOWN", ""}[rng.Intn(2)]
		if rng.Intn(2) == 0 {
			e["role"] = "model"
			e["parts"] = []any{map[string]any{"text": "legacy reply"}}
		}
	case 9: // legacy Gemini: no type
		e["role"] = []string{"user", "model"}[rng.Intn(2)]
		if rng.Intn(4) > 0 {
			e["parts"] = []any{map[string]any{"text": []string{"hi", ""}[rng.Intn(2)]}}
		}
	}
	if rng.Intn(4) > 0 {
		e["step_index"] = float64(rng.Intn(6))
	} else if rng.Intn(3) == 0 {
		e["step_index"] = "3"
	}
	switch rng.Intn(3) {
	case 0:
		e["created_at"] = "2026-10-18T10:00:00Z"
	case 1:
		e["timestamp"] = "2026-10-18T11:00:00Z"
	}
	return e
}

// agyKinds names what an entry's messages exercised, for the floors.
func agyKinds(entry map[string]any, msgs []map[string]any) []string {
	var kinds []string
	if len(msgs) == 0 {
		if c, _ := entry["content"].(string); entry["type"] == "USER_INPUT" && strings.Contains(c, "<task-notification>") {
			return []string{"no message", "system-injected input dropped"}
		}
		return []string{"no message"}
	}
	for _, m := range msgs {
		kinds = append(kinds, "message "+fmt.Sprint(m["type"]))
		if m["type"] == "tool_result" {
			if m["is_error"] == true {
				kinds = append(kinds, "tool error")
			}
			if id, _ := m["tool_use_id"].(string); id != "" {
				if si, ok := entry["step_index"].(float64); ok && id == fmt.Sprintf("step-%d-0", int(si)-1) {
					kinds = append(kinds, "result matched by step")
				} else {
					kinds = append(kinds, "result matched to the last call")
				}
			}
		}
		uses, _ := m["tool_uses"].([]map[string]any)
		for _, u := range uses {
			name, _ := u["name"].(string)
			for _, f := range []string{"command", "old_string", "new_string", "write_content", "questions", "description"} {
				if _, ok := u[f]; ok {
					kinds = append(kinds, "field "+f)
				}
			}
			if w, _ := u["write_content"].(string); strings.HasSuffix(w, "(truncated)") {
				kinds = append(kinds, "written content truncated")
			}
			if _, ok := u["questions"].(string); ok {
				kinds = append(kinds, "questions as text")
			} else if _, ok := u["questions"].([]any); ok {
				kinds = append(kinds, "questions decoded")
			}
			sum := fmt.Sprint(u["input_summary"])
			switch {
			case sum == name:
				kinds = append(kinds, "summary: the name")
			case strings.HasPrefix(sum, name+": {"):
				kinds = append(kinds, "summary: the arguments")
			case u["description"] != nil && sum == u["description"]:
				kinds = append(kinds, "summary: the description")
			default:
				kinds = append(kinds, "summary from "+agyToolGroup(name))
			}
		}
	}
	return kinds
}

func agyToolGroup(name string) string {
	switch name {
	case "invoke_subagent", "send_message", "manage_subagents":
		return "a subagent tool"
	case "list_dir", "", "custom_tool":
		return "another tool's arguments"
	}
	return name
}

func TestParseAgyEntryAgreesWithBefore4713(t *testing.T) {
	kinds := map[string]int{}
	entries := 0
	for seed := int64(1); seed <= 1500; seed++ {
		rng := rand.New(rand.NewSource(seed))
		oldNames, newNames := map[string]string{}, map[string]string{}
		for i := 0; i < 40; i++ {
			entry := randomAgyEntry(rng)
			old := parseAgyEntryBefore4713(entry, oldNames)
			got := parseAgyEntry(entry, newNames)
			require.True(t, reflect.DeepEqual(old, got), "seed %d entry %d %v:\nold %#v\nnew %#v", seed, i, entry, old, got)
			require.Equal(t, oldNames, newNames, "seed %d entry %d: the call names noted", seed, i)
			entries++
			for _, k := range agyKinds(entry, got) {
				kinds[k]++
			}
		}
	}
	t.Logf("%d entries compared; per kind: %v", entries, kinds)
	for _, k := range []string{"no message", "message user", "message assistant", "message tool_result", "tool error",
		"result matched by step", "result matched to the last call", "field command", "field old_string", "field new_string",
		"field write_content", "field description", "questions as text", "questions decoded", "summary: the name",
		"summary: the arguments", "summary: the description", "summary from run_command", "summary from replace_file_content",
		"summary from write_to_file", "summary from view_file", "summary from read_url_content", "summary from search_web",
		"summary from a subagent tool", "summary from another tool's arguments", "summary from ask_question",
		"system-injected input dropped", "written content truncated"} {
		assert.GreaterOrEqual(t, kinds[k], 30, "%q went (almost) unexercised", k)
	}
}

// parseAgyEntryBefore4713 is parseAgyEntry before #4713 split it (reader.go at v1.4.92), kept to compare with.
func parseAgyEntryBefore4713(entry map[string]any, toolUseNames map[string]string) []map[string]any {
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
		content, _ := entry["content"].(string)
		cleaned := cleanAgyUserInput(content)
		if cleaned == "" || isSystemInjected(cleaned) {
			return nil
		}
		return []map[string]any{{"type": "user", "timestamp": timestamp, "content": cleaned}}

	case "PLANNER_RESPONSE":
		content, _ := entry["content"].(string)
		var toolUses []map[string]any
		if tcs, ok := entry["tool_calls"].([]any); ok {
			for i, rawTc := range tcs {
				tc, ok := rawTc.(map[string]any)
				if !ok {
					continue
				}
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
						if qStr, ok := rawQ.(string); ok && strings.HasPrefix(strings.TrimSpace(qStr), "[") {
							var parsed any
							if err := json.Unmarshal([]byte(qStr), &parsed); err == nil {
								toolEntry["questions"] = parsed
							} else {
								toolEntry["questions"] = qStr
							}
						} else {
							toolEntry["questions"] = rawQ
						}
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

				if toolEntry["input_summary"] == nil || toolEntry["input_summary"] == "" {
					if desc != "" {
						toolEntry["input_summary"] = desc
					} else {
						for _, key := range []string{"TargetFile", "AbsolutePath", "CommandLine", "Url", "query", "Prompt", "Message", "path"} {
							if val := cleanAgyArgString(args[key]); val != "" {
								toolEntry["input_summary"] = truncate(val, 200)
								break
							}
						}
					}
					if toolEntry["input_summary"] == nil || toolEntry["input_summary"] == "" {
						if b, err := json.Marshal(args); err == nil && len(args) > 0 {
							toolEntry["input_summary"] = truncate(name+": "+string(b), 200)
						} else {
							toolEntry["input_summary"] = name
						}
					}
				}

				toolUses = append(toolUses, toolEntry)
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

	case "GENERIC":
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

	if _, ok := entry["role"]; ok {
		return parseGeminiEntry(entry)
	}
	return nil
}
