package board

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertMsg(t *testing.T, s *Store, project, sender, content string) int64 {
	t.Helper()
	res, err := s.db.Exec(`INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at, is_system) VALUES (?, 'sess', ?, ?, '2026-10-20T08:00:00Z', 0)`, project, sender, content)
	require.NoError(t, err)
	id, _ := res.LastInsertId()
	return id
}

// #5354: SearchMessages finds the boards that have a matching message, as the full scan did (case-insensitive for ASCII, % and _ as wildcards,
// an empty query matching every board with a message), but stops at the first match of each board.
func TestSearchMessagesFindsTheSameBoardsAsTheOldScan(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	insertMsg(t, s, "alpha", "Dev", "Deploy the Server today")
	insertMsg(t, s, "alpha", "QA", "nothing here")
	insertMsg(t, s, "beta", "Dev", "100% done, dep_oy later")
	insertMsg(t, s, "gamma", "Dev", "café menu")
	insertMsg(t, s, "gamma", "Dev", "CAFÉ MENU upper")
	insertMsg(t, s, "delta-board", "Dev", "second word match")
	for i := 0; i < 50; i++ {
		insertMsg(t, s, "zeta", "QA", fmt.Sprintf("filler %d", i))
	}
	for _, q := range []string{"deploy", "DEPLOY", "server", "100%", "%", "_", "dep_oy", "dep%y", "done", "café", "CAFÉ", "menu", "second", "filler 49", "filler 50", "absent", "", "a", "nothing", "word match", "'", `"`} {
		var want []string
		require.NoError(t, s.db.Select(&want, "SELECT DISTINCT project FROM board_messages WHERE content LIKE ? COLLATE NOCASE", "%"+q+"%"))
		got, err := s.SearchMessages(ctx, q)
		require.NoError(t, err)
		sort.Strings(want)
		sort.Strings(got)
		if len(want) == 0 {
			assert.Empty(t, got, "%q", q)
		} else {
			assert.Equal(t, want, got, "%q", q)
		}
	}
	// No messages at all: no boards, no error.
	empty := testStore(t)
	got, err := empty.SearchMessages(ctx, "x")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// The hits are the same messages, newest first, as the lower() LIKE lower() they replaced.
func TestSearchMessageHitsAreTheOldScansHits(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := 0; i < 300; i++ {
		content := fmt.Sprintf("Update %d: ", i)
		switch {
		case i%7 == 0:
			content += "Deploy to PRODUCTION"
		case i%11 == 0:
			content += "deploy_staging 50% done"
		case i%13 == 0:
			content += "café CAFÉ"
		}
		insertMsg(t, s, []string{"alpha", "beta"}[i%2], "Dev", content)
	}
	for _, q := range []string{"deploy", "PRODUCTION", "production", "deploy_staging", "50%", "%", "_", "dep_oy", "café", "CAFÉ", "update 1", "nothing at all", "'"} {
		for _, limit := range []int{5, 100, 1000} {
			var want []int64
			require.NoError(t, s.db.Select(&want, `SELECT m.id FROM board_messages m WHERE lower(m.content) LIKE lower(?) ORDER BY m.id DESC LIMIT ?`, "%"+q+"%", limit))
			got, err := s.SearchMessageHits(ctx, q, limit)
			require.NoError(t, err)
			ids := make([]int64, len(got))
			for i, m := range got {
				ids[i] = m.ID
			}
			if len(want) == 0 {
				assert.Empty(t, ids, "%q limit %d", q, limit)
			} else {
				assert.Equal(t, want, ids, "%q limit %d", q, limit)
			}
		}
	}
}

// "Has the health monitor posted since I read?" is a seek on the sender index, not a scan of everything unread, and answers as before.
func TestUnreadHealthMessageIsASeekOnTheSenderIndex(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, where := range []string{unreadSenderWhere(false), unreadSenderWhere(true)} {
		rows, err := s.db.Queryx("EXPLAIN QUERY PLAN SELECT 1 FROM board_messages m WHERE "+where+" LIMIT 1", "team", 0, "Orchestrator", "Coral Health Monitor")
		require.NoError(t, err)
		var plan []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &notused, &detail))
			plan = append(plan, detail)
		}
		rows.Close()
		joined := strings.Join(plan, " | ")
		if !strings.Contains(where, "!=?") || strings.Count(where, "!=?") == 1 {
			assert.NotContains(t, joined, "SCAN board_messages", "%s: %s", where, joined)
		}
		if strings.Contains(where, "subscriber_id=?") {
			assert.Contains(t, joined, "idx_board_messages_sender", "the equality on the sender uses its index: %s", joined)
		}
	}

	_, err := s.Subscribe(ctx, "team", "Orchestrator", "Orchestrator", "tmux-orch", nil, "all")
	require.NoError(t, err)
	_, err = s.Subscribe(ctx, "team", "Dev", "Developer", "tmux-dev", nil, "")
	require.NoError(t, err)
	has := func(who string) (bool, bool) {
		h, err := s.HasUnreadHealthMessage(ctx, "team", who)
		require.NoError(t, err)
		n, err := s.HasUnreadNonHealthMessage(ctx, "team", who)
		require.NoError(t, err)
		return h, n
	}
	h, n := has("Orchestrator")
	assert.False(t, h)
	assert.False(t, n)
	first := insertMsg(t, s, "team", "Dev", "ordinary")
	h, n = has("Orchestrator")
	assert.False(t, h)
	assert.True(t, n)
	insertMsg(t, s, "team", "Coral Health Monitor", "health check")
	h, n = has("Orchestrator")
	assert.True(t, h)
	assert.True(t, n)
	// Read up to the health message: nothing unread from either.
	_, err = s.db.Exec(`UPDATE board_subscribers SET last_read_id = (SELECT MAX(id) FROM board_messages) WHERE subscriber_id='Orchestrator'`)
	require.NoError(t, err)
	h, n = has("Orchestrator")
	assert.False(t, h)
	assert.False(t, n)
	// Only the health monitor's unread: health yes, non-health no. The task queue's and the reader's own messages never count.
	_, err = s.db.Exec(`UPDATE board_subscribers SET last_read_id = ? WHERE subscriber_id='Orchestrator'`, first)
	require.NoError(t, err)
	h, n = has("Orchestrator")
	assert.True(t, h)
	assert.False(t, n)
	insertMsg(t, s, "team", "Coral Task Queue", "queue")
	insertMsg(t, s, "team", "Orchestrator", "my own")
	h, n = has("Orchestrator")
	assert.True(t, h)
	assert.False(t, n, "the task queue and the reader's own messages are not unread for the reader")
}

// An existing database gets the index at startup: opening again creates it when it is missing.
func TestTheSenderIndexIsCreatedOnAnExistingDatabase(t *testing.T) {
	s := testStore(t)
	_, err := s.db.Exec("DROP INDEX idx_board_messages_sender")
	require.NoError(t, err)
	require.NoError(t, s.ensureSchema(context.Background()))
	var n int
	require.NoError(t, s.db.Get(&n, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_board_messages_sender'"))
	assert.Equal(t, 1, n)
}
