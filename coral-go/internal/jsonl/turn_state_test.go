package jsonl

import (
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexTurnEvent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	complete := `{"timestamp":"2026-09-26T16:29:51.825Z","type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
	cases := []struct{ name, data, want string }{
		{"missing hooks", complete, "stop"},
		{"new turn", complete + `{"timestamp":"2026-09-26T16:30:00Z","type":"event_msg","payload":{"type":"task_started"}}` + "\n", "prompt_submit"},
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
