package jsonl

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexTurnEvent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	complete := `{"timestamp":"2026-09-26T16:29:51.825Z","type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
	cases := []struct{ name, data, want string }{
		{"missing hooks", complete, "stop"},
		{"new turn", complete + `{"timestamp":"2026-09-26T16:30:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n", "prompt_submit"},
		{"active transcript", complete + `{"timestamp":"2026-09-26T16:30:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n" + `{"timestamp":"2026-09-26T16:30:01Z","type":"response_item","payload":{"type":"message"}}` + "\n", "prompt_submit"},
		{"partial new record", complete + `{"timestamp":`, "stop"},
		{"no lifecycle", `{"type":"event_msg","payload":{"type":"item_completed"}}` + "\n", ""},
		{"invalid timestamp", `{"timestamp":"bad","type":"event_msg","payload":{"type":"task_complete"}}` + "\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(p, []byte(c.data), 0600))
			kind, _ := CodexTurnEvent(p)
			require.Equal(t, c.want, kind)
		})
	}
}

func TestCodexTurnEventLongTurnBeyondOriginalTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "long-rollout.jsonl")
	start := `{"timestamp":"2026-09-30T04:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n"
	// More than the former 1 MiB scan window, with committed response records
	// between the lifecycle boundaries. The active turn must remain visible.
	var b strings.Builder
	b.WriteString(start)
	for i := 0; i < 18000; i++ {
		b.WriteString(fmt.Sprintf(`{"timestamp":"2026-09-30T04:00:%02d.%03dZ","type":"response_item","payload":{"type":"message","text":"%s"}}`+"\n", (i/1000)%60, i%1000, strings.Repeat("x", 100)))
	}
	if err := os.WriteFile(p, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	kind, at := CodexTurnEvent(p)
	require.Equal(t, "prompt_submit", kind)
	require.False(t, at.IsZero())

	// A completion inside the bounded window wins, and a subsequent turn
	// correctly takes ownership of the state again.
	completed := b.String() + `{"timestamp":"2026-09-30T04:30:00Z","type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
	completed += `{"timestamp":"2026-09-30T04:31:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n"
	if err := os.WriteFile(p, []byte(completed), 0600); err != nil {
		t.Fatal(err)
	}
	kind, at = CodexTurnEvent(p)
	require.Equal(t, "prompt_submit", kind)
	require.Equal(t, "2026-09-30T04:31:00Z", at.Format(time.RFC3339))
}

func TestReadCodexTurnEventIncrementalLongTurnGrowthAndReplacement(t *testing.T) {
	p := filepath.Join(t.TempDir(), "incremental-rollout.jsonl")
	start := `{"timestamp":"2026-09-30T05:00:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n"
	filler := strings.Repeat(`{"timestamp":"2026-09-30T05:00:01Z","type":"response_item","payload":{"type":"message","text":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}}`+"\n", 70000)
	if err := os.WriteFile(p, []byte(start+filler), 0600); err != nil {
		t.Fatal(err)
	}
	var state codexTurnCache
	kind, at := readCodexTurnEventIncremental(p, &state)
	require.Equal(t, "prompt_submit", kind)
	require.Equal(t, "2026-09-30T05:00:01Z", at.Format(time.RFC3339))
	if err := os.WriteFile(p, []byte(start+filler+`{"timestamp":"2026-09-30T05:01:00Z","type":"event_msg","payload":{"type":"task_complete"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	kind, _ = readCodexTurnEventIncremental(p, &state)
	require.Equal(t, "stop", kind)
	// Replacement with a different header resets the cached lifecycle state.
	if err := os.WriteFile(p, []byte(`{"timestamp":"2026-09-30T06:00:00Z","type":"event_msg","payload":{"type":"task_started"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	kind, at = readCodexTurnEventIncremental(p, &state)
	require.Equal(t, "prompt_submit", kind)
	require.Equal(t, "2026-09-30T06:00:00Z", at.Format(time.RFC3339))
}

func TestAgyTurnEvent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	stopped := `{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-28T03:00:00Z","tool_calls":[]}` + "\n"

	cases := []struct {
		name        string
		data        string
		wantKind    string
		wantSummary string
	}{
		{
			name:     "turn ended with no tools",
			data:     stopped,
			wantKind: "stop",
		},
		{
			name:     "user submitted prompt starts turn",
			data:     stopped + `{"step_index":2,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-28T03:00:05Z"}` + "\n",
			wantKind: "prompt_submit",
		},
		{
			name:     "model executing tool call",
			data:     stopped + `{"step_index":2,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-28T03:00:05Z"}` + "\n" + `{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-28T03:00:08Z","tool_calls":[{"name":"run_command"}]}` + "\n",
			wantKind: "tool_use",
		},
		{
			name:     "generic tool result received",
			data:     stopped + `{"step_index":2,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-28T03:00:05Z"}` + "\n" + `{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-28T03:00:08Z","tool_calls":[{"name":"run_command"}]}` + "\n" + `{"step_index":4,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-09-28T03:00:10Z"}` + "\n",
			wantKind: "tool_use",
		},
		{
			name:        "ask_question tool call needs input",
			data:        stopped + `{"step_index":2,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-09-28T03:00:05Z"}` + "\n" + `{"step_index":3,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-09-28T03:00:08Z","tool_calls":[{"name":"ask_question"}]}` + "\n",
			wantKind:    "notification",
			wantSummary: "Antigravity needs your input",
		},
		{
			name:     "error in planner response stops turn",
			data:     stopped + `{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"ERROR","created_at":"2026-09-28T03:00:05Z"}` + "\n",
			wantKind: "stop",
		},
		{
			name:     "trailing partial line ignored",
			data:     stopped + `{"step_index":2,"source":"USER_EXPLICIT","created_at":`,
			wantKind: "stop",
		},
		{
			name:     "legacy gemini user prompt",
			data:     `[{"role":"model","timestamp":"2026-09-28T03:00:00Z"},{"role":"user","timestamp":"2026-09-28T03:00:05Z"}]`,
			wantKind: "prompt_submit",
		},
		{
			name:     "legacy gemini model response",
			data:     `[{"role":"user","timestamp":"2026-09-28T03:00:00Z"},{"role":"model","timestamp":"2026-09-28T03:00:05Z"}]`,
			wantKind: "stop",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(p, []byte(c.data), 0600))
			kind, at, summary := AgyTurnEvent(p)
			require.Equal(t, c.wantKind, kind)
			if c.wantSummary != "" {
				require.Equal(t, c.wantSummary, summary)
			}
			if c.wantKind != "" {
				require.False(t, at.IsZero())
			}
		})
	}
}
