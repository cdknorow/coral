// Package hooks provides shared utilities for Coral CLI hooks.
// Hooks are lightweight binaries that read JSON from stdin (Claude Code hook protocol),
// parse agent events, and forward them to the Coral dashboard API.
package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/executil"
	"github.com/subgentic/coral/internal/tmux"
	"io"
	"net/http"
	"net/url"
	"os"
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
		// Nothing says where Coral is: the address the running server recorded in its lock file, else the platform's own default (8450 on Windows,
		// 8420 elsewhere): 8420 on Windows sent a command typed in a plain terminal to the wrong port, where this platform's Coral never listens (#235).
		return config.LocalServerURL()
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
		out, err := runTmux(tmux.BinaryFromEnv(), "display-message", "-p", "#{session_name}")
		if err == nil {
			if id := sessionIDFromName(strings.TrimSpace(string(out))); id != "" {
				return id
			}
		}
	}
	return payloadSessionID
}

// runTmux runs the tmux binary for the session lookup, bounded (tests replace it: a real process can take longer
// than the bound on a loaded machine).
var runTmux = func(bin string, args ...string) ([]byte, error) {
	return executil.Output(3*time.Second, bin, args...)
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

// MaxHookStdin bounds what a hook reads from the agent CLI: a tool's whole output rides in a PostToolUse payload, and every tool call runs a hook.
const MaxHookStdin = 32 << 20

// ReadStdin reads a hook's stdin up to MaxHookStdin. A longer payload (a tool that printed a huge file) is not held and not parsed: the hook
// records nothing for that call, as for one that is not JSON, and the CLI goes on.
func ReadStdin(r io.Reader) ([]byte, bool, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxHookStdin+1))
	if err != nil {
		return nil, false, err
	}
	if len(raw) > MaxHookStdin {
		// The rest is read and thrown away, never held: the CLI writing the payload must not get EPIPE (a closed pipe) for a hook that stopped reading.
		_, _ = io.Copy(io.Discard, r)
		return nil, false, nil
	}
	return raw, true, nil
}

// maxCoralReply bounds a reply a hook reads from Coral.
const maxCoralReply = 8 << 20

// RedactedURL is raw with any userinfo (user:password@) hidden, for a message that names the server: a credential embedded in CORAL_URL never reaches a
// log or the agent's screen.
func RedactedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("redacted")
	return u.String()
}

// BoardStateFileName is the name of an agent session's board state file in the data folder (coral-board writes it, the message-check hook reads it): the
// session name with any path separator made "_", so no name, whatever the environment says, leaves the data folder.
func BoardStateFileName(sessionName string) string {
	return "board_state_" + strings.NewReplacer("/", "_", "\\", "_").Replace(sessionName) + ".json"
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

	result, err := io.ReadAll(io.LimitReader(resp.Body, maxCoralReply))
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
	// Antigravity's tools (#4577; their argument names as agy 1.4.2 sends them)
	case "run_command":
		return "Ran: " + Truncate(StrVal(inp, "CommandLine"), 80)
	case "view_file":
		return "Read " + filepath.Base(StrVal(inp, "AbsolutePath"))
	case "write_to_file":
		return "Wrote " + filepath.Base(StrVal(inp, "TargetFile"))
	case "replace_file_content", "multi_replace_file_content":
		return "Edited " + filepath.Base(StrVal(inp, "TargetFile"))
	case "search_web":
		return "Searched: " + Truncate(agyQuery(inp), 60)
	case "read_url_content":
		return "Fetched " + Truncate(StrVal(inp, "Url"), 60)
	default:
		return "Used " + tool
	}
}

// agyQuery is search_web's query: agy 1.4.2 sends "query" (lowercase, unlike its other arguments).
func agyQuery(inp map[string]any) string {
	if q := StrVal(inp, "query"); q != "" {
		return q
	}
	return StrVal(inp, "Query")
}

// AgyToolDetail is the detail an activity row shows for an Antigravity tool call, under the keys the other
// CLIs' calls use (command, file_path, query, url), or nil for another tool.
func AgyToolDetail(tool string, inp map[string]any) map[string]any {
	switch tool {
	case "run_command":
		return map[string]any{"command": StrVal(inp, "CommandLine")}
	case "view_file":
		return map[string]any{"file_path": StrVal(inp, "AbsolutePath")}
	case "write_to_file", "replace_file_content", "multi_replace_file_content":
		return map[string]any{"file_path": StrVal(inp, "TargetFile")}
	case "search_web":
		return map[string]any{"query": agyQuery(inp)}
	case "read_url_content":
		return map[string]any{"url": StrVal(inp, "Url")}
	}
	return nil
}
