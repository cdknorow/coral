// Package hooks provides shared utilities for Coral CLI hooks.
// Hooks are lightweight binaries that read JSON from stdin (Claude Code hook protocol),
// parse agent events, and forward them to the Coral dashboard API.
package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/cdknorow/coral/internal/tmux"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var tmuxUUIDRe = regexp.MustCompile(`^[a-z]+-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

// CoralBase returns the Coral API base URL from environment.
func CoralBase() string {
	if u := os.Getenv("CORAL_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	port := os.Getenv("CORAL_PORT")
	if port == "" {
		port = "8420"
	}
	return "http://localhost:" + port
}

// ResolveSessionID gets the Coral session ID, in order of preference, from
// the CORAL_SESSION_NAME env var Coral exports into every agent, the tmux
// session name, or finally the hook payload. The payload's session_id is the
// agent's own ID (e.g. a Codex thread ID) which Coral does not know about, so
// events posted under it would never show up in the dashboard.
//
// CORAL_SESSION_NAME comes first (#3635): Coral sets it for every agent on
// both backends, and new launches unset TMUX, so the agent and its tools can't
// reach Coral's tmux server. The tmux lookup stays one release for agents
// launched before that, which still have TMUX and may predate the variable;
// remove it after.
func ResolveSessionID(payloadSessionID string) string {
	if id := sessionIDFromName(strings.TrimSpace(os.Getenv("CORAL_SESSION_NAME"))); id != "" {
		return id
	}
	if os.Getenv("TMUX") != "" {
		out, err := exec.Command(tmux.BinaryFromEnv(), "display-message", "-p", "#{session_name}").Output()
		if err == nil {
			if id := sessionIDFromName(strings.TrimSpace(string(out))); id != "" {
				return id
			}
		}
	}
	return payloadSessionID
}

// sessionIDFromName extracts the lowercase UUID from a "{type}-{uuid}" Coral
// session name, or returns "" if the name is not in that form.
func sessionIDFromName(name string) string {
	if m := tmuxUUIDRe.FindStringSubmatch(strings.ToLower(name)); len(m) == 2 {
		return m[1]
	}
	return ""
}

// ResolveAgentName extracts the agent/worktree name from hook payload cwd.
func ResolveAgentName(hookData map[string]any) string {
	cwd, _ := hookData["cwd"].(string)
	if cwd == "" {
		cwd, _ = hookData["current_working_directory"].(string)
	}
	if cwd == "" {
		cwd, _ = hookData["working_directory"].(string)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if cwd == "" {
		return ""
	}
	name := filepath.Base(strings.TrimRight(cwd, "/\\"))
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return "" // not a name: in a URL path it would climb out of /api/sessions/live/ (#3571)
	}
	return name
}

// LivePath is the API path of one of the agent's live-session routes, with the agent's
// name escaped: the name comes from the hook payload's cwd, so a "?", "#" or "/" in it
// must not change which route is called (#3571).
func LivePath(agentName, route string) string {
	return "/api/sessions/live/" + url.PathEscape(agentName) + route
}

// CoralAPI sends a request to the Coral dashboard API.
func CoralAPI(base, method, path string, data any) (json.RawMessage, error) {
	var body io.Reader
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	result, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(result), nil
}

// Truncate shortens a string, adding "..." if it exceeds maxLen.
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// StrVal extracts a string value from a map by key, returning "" if missing or wrong type.
func StrVal(d map[string]any, key string) string {
	v, _ := d[key].(string)
	return v
}

// GetToolInput extracts tool_input as a map from hook data.
func GetToolInput(hookData map[string]any) map[string]any {
	inp, ok := hookData["tool_input"].(map[string]any)
	if !ok {
		inp, ok = hookData["input"].(map[string]any)
	}
	if !ok {
		if tool, toolOK := hookData["tool"].(map[string]any); toolOK {
			inp, ok = tool["input"].(map[string]any)
		}
	}
	if !ok {
		if args, ok := hookData["arguments"].(map[string]any); ok {
			inp = args
			ok = true
		}
	}
	if !ok {
		return map[string]any{}
	}
	return inp
}

// MakeToolSummary generates a human-readable summary for a tool call.
func MakeToolSummary(tool string, inp map[string]any) string {
	switch tool {
	case "Bash":
		cmd, _ := inp["command"].(string)
		return "Ran: " + Truncate(cmd, 80)
	case "Read":
		fp, _ := inp["file_path"].(string)
		return "Read " + filepath.Base(fp)
	case "Write":
		fp, _ := inp["file_path"].(string)
		return "Created " + filepath.Base(fp)
	case "Edit":
		fp, _ := inp["file_path"].(string)
		return "Edited " + filepath.Base(fp)
	case "Grep":
		pat, _ := inp["pattern"].(string)
		return "Searched: " + Truncate(pat, 60)
	case "Glob":
		pat, _ := inp["pattern"].(string)
		return "Glob: " + Truncate(pat, 60)
	case "Agent":
		prompt, _ := inp["prompt"].(string)
		return "Agent: " + Truncate(prompt, 60)
	case "TaskCreate":
		subj, _ := inp["subject"].(string)
		return "Task: " + Truncate(subj, 60)
	case "TaskUpdate":
		status, _ := inp["status"].(string)
		subj, _ := inp["subject"].(string)
		if subj != "" {
			return fmt.Sprintf("Task %s: %s", status, Truncate(subj, 50))
		}
		return "Task " + status
	case "WebFetch":
		u, _ := inp["url"].(string)
		return "Fetched " + Truncate(u, 60)
	case "WebSearch":
		q, _ := inp["query"].(string)
		return "Searched: " + Truncate(q, 60)
	default:
		return "Used " + tool
	}
}
