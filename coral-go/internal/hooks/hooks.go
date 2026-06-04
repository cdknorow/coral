// Package hooks provides shared utilities for Coral CLI hooks.
// Hooks are lightweight binaries that read JSON from stdin (Claude Code hook protocol),
// parse agent events, and forward them to the Coral dashboard API.
package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
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

// ResolveSessionID gets the session ID from tmux session name or payload.
func ResolveSessionID(payloadSessionID string) string {
	if os.Getenv("TMUX") != "" {
		out, err := exec.Command("tmux", "display-message", "-p", "#{session_name}").Output()
		if err == nil {
			name := strings.TrimSpace(string(out))
			if m := tmuxUUIDRe.FindStringSubmatch(name); len(m) == 2 {
				return strings.ToLower(m[1])
			}
		}
	}
	return payloadSessionID
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
	return filepath.Base(strings.TrimRight(cwd, "/\\"))
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

// DebugLog appends a timestamped line to the hook debug log.
func DebugLog(msg string) {
	dir := CacheDir()
	logPath := filepath.Join(dir, "debug.log")

	// Rotate if over 500KB
	if info, err := os.Stat(logPath); err == nil && info.Size() > 512000 {
		os.WriteFile(logPath, []byte("--- log rotated ---\n"), 0644)
	}

	ts := time.Now().UTC().Format("15:04:05.000")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] %s\n", ts, msg)
}

// CacheDir returns the Coral hook cache directory.
func CacheDir() string {
	var base string
	if runtime.GOOS == "windows" {
		base = os.TempDir()
	} else {
		base = os.Getenv("TMPDIR")
		if base == "" {
			base = "/tmp"
		}
	}
	d := filepath.Join(base, "coral_task_cache")
	os.MkdirAll(d, 0755)
	return d
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
