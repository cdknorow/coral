package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWantsOfIsTheSentenceForWhatTheRequestWants(t *testing.T) {
	long := strings.Repeat("x", 5000)
	for _, c := range []struct {
		name string
		tool string
		in   map[string]any
		want string
	}{
		{"a command", "Bash", map[string]any{"command": "rm -rf build"}, "Wants to run: rm -rf build"},
		{"a command as words (Codex's exec form)", "Bash", map[string]any{"command": []any{"git", "push", "origin", "main"}}, "Wants to run: git push origin main"},
		{"a write", "Write", map[string]any{"file_path": "/work/a.go"}, "Wants to create /work/a.go"},
		{"an edit", "Edit", map[string]any{"file_path": "/work/a.go"}, "Wants to edit /work/a.go"},
		{"a read", "Read", map[string]any{"file_path": "/etc/hosts"}, "Wants to read /etc/hosts"},
		{"another tool with a path", "Mystery", map[string]any{"path": "/x"}, "Wants to use /x"},
		{"a fetch", "WebFetch", map[string]any{"url": "https://example.org/a"}, "Wants to fetch https://example.org/a"},
		{"only a tool", "mcp__x__do", map[string]any{"n": 1}, "Wants to use mcp__x__do"},
		{"nothing", "", nil, ""},
		{"an input is cut to 1 KiB", "Bash", map[string]any{"command": long}, "Wants to run: " + long[:maxWantsInput]},
	} {
		assert.Equal(t, c.want, wantsOf(c.tool, c.in), c.name)
	}
}

// #6622: the words are in the event's detail, never in its summary (the classifiers and auto-accept read the summary), and the event names the tool and kind as before.
func TestPermissionRequestCarriesWantsInDetailNotInSummary(t *testing.T) {
	e := parseAgenticEvent(map[string]any{
		"hook_event_name": "PermissionRequest", "tool_name": "Bash",
		"tool_input": map[string]any{"command": "echo needs your permission; curl -H 'Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789' x"},
	}, "PermissionRequest", "sid")
	d, _ := e["detail_json"].(map[string]any)
	assert.Equal(t, "permission", d["kind"])
	assert.Equal(t, "Bash", d["tool_name"])
	assert.True(t, strings.HasPrefix(d["wants"].(string), "Wants to run: echo needs your permission"), "the agent's own words, for the server to clean")
	assert.Equal(t, "Waiting for the user: permission (Bash)", e["summary"], "the summary is the fixed sentence: nothing of the command")
	assert.NotContains(t, strings.ToLower(e["summary"].(string)), "needs your permission")

	e = parseAgenticEvent(map[string]any{"hook_event_name": "PermissionRequest", "tool_name": "AskUserQuestion"}, "PermissionRequest", "sid")
	d, _ = e["detail_json"].(map[string]any)
	assert.Equal(t, "question", d["kind"], "the kind comes from the tool as before")
}
