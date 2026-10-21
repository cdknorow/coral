package main

// Hook events for raw-input mode: Claude Code runs the hooks in its --settings at session start,
// when a prompt is submitted and when its turn stops, and Coral reads the agent's state from them
// (a person's send is confirmed by its UserPromptSubmit, #4587; the automated-input gate holds
// input while a turn runs). A mock that never ran them looked like an agent stuck starting up, so
// in raw-input mode it runs them as Claude does, unless MOCK_AGENT_HOOKS=0: SessionStart then Stop
// once ready, UserPromptSubmit then Stop for each submission (a turn that ends at once).
//
// A command is split into words (double quotes group, as Coral writes an absolute path with
// spaces) and run directly, not through a shell: this mock tests what reaches the agent's
// terminal, not how a shell parses hook commands (that is each agent CLI's own business).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// hookCommands are the commands settings name for event (every matcher group).
func hookCommands(settingsFile, event string) []string {
	data, err := os.ReadFile(settingsFile)
	if err != nil {
		return nil
	}
	var s struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(data, &s) != nil {
		return nil
	}
	var out []string
	for _, group := range s.Hooks[event] {
		for _, h := range group.Hooks {
			if h.Type == "command" && strings.TrimSpace(h.Command) != "" {
				out = append(out, h.Command)
			}
		}
	}
	return out
}

// splitWords splits a command line into words: white space separates, double quotes group.
func splitWords(s string) []string {
	var words []string
	var cur strings.Builder
	inQuote, have := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote, have = !inQuote, true
		case (r == ' ' || r == '\t') && !inQuote:
			if have {
				words = append(words, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if have {
		words = append(words, cur.String())
	}
	return words
}

// hookRunner fires settings' hooks for one session.
type hookRunner struct {
	settings, sessionID, cwd string
}

func newHookRunner(settingsFile, sessionID string) *hookRunner {
	if os.Getenv("MOCK_AGENT_HOOKS") == "0" || settingsFile == "" {
		return nil
	}
	cwd, _ := os.Getwd()
	return &hookRunner{settings: settingsFile, sessionID: sessionID, cwd: cwd}
}

// fire runs event's hooks with Claude Code's JSON on stdin; failures are reported, never fatal.
func (h *hookRunner) fire(event string, extra map[string]any) {
	if h == nil {
		return
	}
	in := map[string]any{"session_id": h.sessionID, "hook_event_name": event, "cwd": h.cwd, "transcript_path": ""}
	for k, v := range extra {
		in[k] = v
	}
	payload, _ := json.Marshal(in)
	for _, c := range hookCommands(h.settings, event) {
		words := splitWords(c)
		if len(words) == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, words[0], words[1:]...)
		cmd.Stdin = bytes.NewReader(payload)
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "[mock-agent] %s hook failed: %v %s\r\n", event, err, bytes.TrimSpace(out))
		}
		cancel()
	}
}
