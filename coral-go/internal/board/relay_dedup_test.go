package board

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #3489: relayed message ids are remembered for de-duplication for 30 days; older ones are
// pruned when a new relayed message is stored.
func TestRelayDedupIdsArePruned(t *testing.T) {
	s, err := NewStoreWithKey(t.TempDir()+"/board.db", "")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	_, err = s.Subscribe(ctx, "beta", "Orchestrator", "Orchestrator", "sess", nil, "mentions")
	require.NoError(t, err)
	old := time.Now().UTC().Add(-relayDedupKeep - time.Hour).Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `INSERT INTO board_relayed_messages (source_server, message_id, target_message_id, created_at) VALUES ('srv', 'old', 1, ?)`, old)
	require.NoError(t, err)
	recent := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	_, err = s.db.ExecContext(ctx, `INSERT INTO board_relayed_messages (source_server, message_id, target_message_id, created_at) VALUES ('srv', 'recent', 2, ?)`, recent)
	require.NoError(t, err)

	_, dup, err := s.PostRelayedCrossTeamMessage(ctx, CrossTeamPost{
		Origin:   MessageOrigin{ServerID: "srv", Board: "a", Sender: "Orchestrator", SenderRole: "Orchestrator", TargetServerID: "local", TargetBoard: "beta", RelayMessageID: "new"},
		Mentions: []string{"Orchestrator"},
		Content:  "hi",
	})
	require.NoError(t, err)
	assert.False(t, dup)
	var ids []string
	require.NoError(t, s.db.SelectContext(ctx, &ids, `SELECT message_id FROM board_relayed_messages ORDER BY message_id`))
	assert.Equal(t, []string{"new", "recent"}, ids)
}
