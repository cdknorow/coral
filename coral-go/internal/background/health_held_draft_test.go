package background

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/subgentic/coral/internal/board"
)

// #247: messages held behind text in an agent's input box that isn't Coral's are told to the Operator on the board, once per
// held episode, naming the agent and nothing of the screen or the held text; a dialog hold keeps its own line.
func TestMessagesHeldBehindADraftAreTold(t *testing.T) {
	ctx := context.Background()
	bs, err := board.NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer bs.Close()
	now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	subs := []board.Subscriber{
		{SubscriberID: "Codex Tester", SessionName: "codex-1"},
		{SubscriberID: "Lead Developer", SessionName: "claude-2"},
		{SubscriberID: "QA Engineer", SessionName: "claude-3"},
	}
	h := &HealthNudges{
		store:       bs,
		now:         func() time.Time { return now },
		heldDialogs: func() map[string]time.Time { return map[string]time.Time{"claude-3": now.Add(-20 * time.Minute)} },
		heldDrafts: func() map[string]time.Time {
			return map[string]time.Time{"codex-1": now.Add(-15 * time.Minute), "claude-2": now.Add(-2 * time.Minute)}
		},
	}
	hs := board.DefaultHealthSettings()
	take := func(string, string, time.Duration) bool { return true }

	h.heldNotices(ctx, "team", subs, now, hs, take)
	h.heldNotices(ctx, "team", subs, now.Add(5*time.Minute), hs, take) // the same episodes: told once

	msgs, err := bs.ListMessages(ctx, "team", 100, 0, 0)
	require.NoError(t, err)
	var drafts, dialogs []string
	for _, m := range msgs {
		switch {
		case strings.Contains(m.Content, "messages held behind text in the input box"):
			drafts = append(drafts, m.Content)
		case strings.Contains(m.Content, "waiting at a dialog"):
			dialogs = append(dialogs, m.Content)
		}
	}
	require.Len(t, drafts, 1, "once per episode")
	assert.Contains(t, drafts[0], "Codex Tester (15 min)")
	assert.NotContains(t, drafts[0], "Lead Developer", "held for less than the set minutes")
	require.Len(t, dialogs, 1)
	assert.Contains(t, dialogs[0], "QA Engineer")
}
