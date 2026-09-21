package background

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cdknorow/coral/internal/store"
)

// The needs_input webhook pages a human. It must fire only for a request made
// directly to the user, never for an agent that finished its turn and then
// received Claude Code's idle reminder.
func TestIdleDetector_NeedsInputWebhookOnlyForADirectRequest(t *testing.T) {
	const (
		permission = "Notification: Claude needs your permission to use Bash"
		idle       = "Notification: Claude is waiting for your input"
		login      = "Notification: Claude Code login successful"
	)
	type step struct{ eventType, summary string }
	cases := []struct {
		name        string
		steps       []step
		wantWebhook bool
	}{
		{"unresolved permission request", []step{{"prompt_submit", ""}, {"notification", permission}}, true},
		{"pending request followed by the idle reminder", []step{{"notification", permission}, {"notification", idle}}, true},
		{"turn ended then idle reminder", []step{{"prompt_submit", ""}, {"tool_use", "Ran: ls"}, {"stop", "Agent stopped: unknown"}, {"notification", idle}}, false},
		{"idle reminder alone", []step{{"notification", idle}}, false},
		{"permission denied then turn ended", []step{{"notification", permission}, {"stop", "Agent stopped: unknown"}, {"notification", idle}}, false},
		{"approved request resolved by a tool", []step{{"notification", permission}, {"tool_use", "Ran: ls"}}, false},
		{"login notification", []step{{"stop", ""}, {"notification", login}}, false},
		{"no events", nil, false},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The detector reads log staleness from os.TempDir().
			logDir := t.TempDir()
			t.Setenv("TMPDIR", logDir)

			db, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
			require.NoError(t, err)
			t.Cleanup(func() { db.Close() })
			tasks := store.NewTaskStore(db)
			webhooks := store.NewWebhookStore(db)
			ctx := context.Background()

			cfg, err := webhooks.CreateWebhookConfig(ctx, "pager", "generic", "http://127.0.0.1:1/hook", nil)
			require.NoError(t, err)

			sid := fmt.Sprintf("00000000-0000-0000-0000-0000000007%02d", i)
			for _, s := range tc.steps {
				id := sid
				_, err := tasks.InsertAgentEvent(ctx, &store.AgentEvent{AgentName: "shared-folder", SessionID: &id, EventType: s.eventType, Summary: s.summary})
				require.NoError(t, err)
			}

			logPath := filepath.Join(os.TempDir(), "claude_coral_"+sid+".log")
			require.NoError(t, os.WriteFile(logPath, []byte("x"), 0644))
			old := time.Now().Add(-2 * needsInputThreshold * time.Second)
			require.NoError(t, os.Chtimes(logPath, old, old))

			d := NewIdleDetector(tasks, webhooks, time.Hour)
			d.SetDiscoverFn(func(context.Context) ([]AgentInfo, error) {
				return []AgentInfo{{AgentName: "shared-folder", AgentType: "claude", SessionID: sid}}, nil
			})
			require.NoError(t, d.RunOnce(ctx))
			// A second pass must not page again for the same request.
			require.NoError(t, d.RunOnce(ctx))

			deliveries, err := webhooks.ListWebhookDeliveries(ctx, cfg.ID, 10)
			require.NoError(t, err)
			if tc.wantWebhook {
				require.Len(t, deliveries, 1, "exactly one needs_input webhook for a pending request")
				assert.Equal(t, "needs_input", deliveries[0].EventType)
			} else {
				assert.Empty(t, deliveries, "an idle or resolved agent must not page anyone")
			}
		})
	}
}
