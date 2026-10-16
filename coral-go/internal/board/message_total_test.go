package board

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The cached totals answer what counting every row answers, after every kind of write: posts,
// rows another writer inserted (cross-team posts insert directly), deletes, and a deleted
// board (#3845).
func TestMessageTotalFollowsWrites(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	check := func(step string) {
		t.Helper()
		for _, project := range []string{"p", "q"} {
			var want int
			require.NoError(t, s.db.Get(&want, "SELECT COUNT(*) FROM board_messages WHERE project = ?", project))
			got, err := s.CountMessages(ctx, project)
			require.NoError(t, err)
			require.Equal(t, want, got, "%s: total of %s", step, project)
			var ids []int64
			require.NoError(t, s.db.Select(&ids, "SELECT id FROM board_messages ORDER BY id"))
			anchors := []int64{0, 1}
			for _, id := range ids {
				anchors = append(anchors, id, id+1)
			}
			if len(ids) > 0 {
				anchors = append(anchors, ids[len(ids)-1]+100)
			}
			for _, a := range anchors {
				var want int
				require.NoError(t, s.db.Get(&want, countMessagesBeforeSQL, project, a))
				got, err := s.CountMessagesBefore(ctx, project, a)
				require.NoError(t, err)
				require.Equal(t, want, got, "%s: %s before %d", step, project, a)
			}
		}
	}
	check("empty")
	var pIDs []int64
	for i := 0; i < 5; i++ {
		m, err := s.PostMessage(ctx, "p", "agent-1", "hello", nil)
		require.NoError(t, err)
		pIDs = append(pIDs, m.ID)
		_, err = s.PostMessage(ctx, "q", "agent-2", "hi", nil)
		require.NoError(t, err)
	}
	check("posts")
	_, err := s.db.Exec(`INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at)
		VALUES ('p', 'remote', 'remote', 'from another writer', '2026-10-16T00:00:00Z')`)
	require.NoError(t, err)
	check("direct insert")
	ok, err := s.DeleteMessage(ctx, "p", pIDs[2])
	require.NoError(t, err)
	require.True(t, ok)
	check("delete one")
	ok, err = s.DeleteMessage(ctx, "p", pIDs[0])
	require.NoError(t, err)
	require.True(t, ok)
	_, err = s.PostMessage(ctx, "p", "agent-1", "after the deletes", nil)
	require.NoError(t, err)
	check("delete and post")
	require.NoError(t, s.DeleteProject(ctx, "q"))
	check("delete board")
	_, err = s.PostMessage(ctx, "q", "agent-2", "a new start", nil)
	require.NoError(t, err)
	check("board again")
}

// Paging far back counts the older messages directly; near the newest it subtracts from the
// total. Both give the exact count.
func TestCountMessagesBeforeDeepHistory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	tx, err := s.db.Beginx()
	require.NoError(t, err)
	for i := 0; i < beforeCountTail+500; i++ {
		_, err := tx.Exec(`INSERT INTO board_messages (project, subscriber_id, session_id, content, created_at)
			VALUES ('big', 'agent-1', 's', 'x', '2026-10-16T00:00:00Z')`)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
	var ids []int64
	require.NoError(t, s.db.Select(&ids, "SELECT id FROM board_messages ORDER BY id"))
	for _, a := range []int64{ids[0], ids[100], ids[499], ids[500], ids[501], ids[len(ids)-50], ids[len(ids)-1]} {
		var want int
		require.NoError(t, s.db.Get(&want, countMessagesBeforeSQL, "big", a))
		got, err := s.CountMessagesBefore(ctx, "big", a)
		require.NoError(t, err)
		require.Equal(t, want, got, "before %d", a)
	}
}

// The total's queries seek the board's (project, id) index; none scans the table.
func TestMessageTotalSQLUsesProjectIndex(t *testing.T) {
	s := testStore(t)
	for _, tc := range []struct {
		sql  string
		args []any
	}{
		{messageTotalHighSQL, []any{0, "p", 0}},
		{messageTotalCountSQL, []any{"p", 0, 10}},
		{countMessagesTailSQL, []any{"p", 5, 10, 100}},
	} {
		var plan []struct {
			ID     int    `db:"id"`
			Parent int    `db:"parent"`
			NotUse int    `db:"notused"`
			Detail string `db:"detail"`
		}
		require.NoError(t, s.db.Select(&plan, "EXPLAIN QUERY PLAN "+tc.sql, tc.args...))
		var details []string
		for _, row := range plan {
			details = append(details, row.Detail)
		}
		joined := strings.Join(details, "; ")
		require.Contains(t, joined, "idx_board_messages_project", tc.sql)
		for _, d := range details {
			if strings.HasPrefix(d, "SCAN board_messages") {
				require.Contains(t, d, "COVERING INDEX idx_board_messages_project", tc.sql)
			}
		}
	}
}

// The startup seed skips the newest-mention search only where it cannot find anything; every
// subscriber's seeded state is what LatestUnreadMessageID and the unread count give.
func TestUnreadStatesSkipOnlyEmptySearches(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	subscribe := func(id, title, mode string, peek bool) {
		_, err := s.Subscribe(ctx, "p", id, title, "tmux-"+id, nil, mode, peek)
		require.NoError(t, err)
	}
	subscribe("w1", "Writer One", "mentions", false)    // mentioned, never read
	subscribe("w2", "Writer Two", "mentions", false)    // never mentioned, never read
	subscribe("w3", "Writer Three", "mentions", false)  // mentioned, read past it
	subscribe("lead", "Orchestrator", "mentions", true) // an orchestrator's latest is anyone's
	subscribe("all", "Reader", "all", false)
	subscribe("grp", "Grouped", "g1", false) // counts by sender, latest by mention
	subscribe("off", "Muted", "none", false)
	require.NoError(t, s.AddToGroup(ctx, "p", "g1", "member")) // never posts: grp's count stays 0

	post := func(from, text string) {
		_, err := s.PostMessage(ctx, "p", from, text, nil)
		require.NoError(t, err)
	}
	post("poster", "general news")
	post("poster", "@w1 please look")
	post("poster", "@w3 have a look")
	_, err := s.ReadMessages(ctx, "p", "w3", 100)
	require.NoError(t, err)
	post("outsider", "@grp a question from outside the group")
	post("outsider", "more general news")

	states, err := s.GetAllUnreadStates(ctx)
	require.NoError(t, err)
	counts, err := s.GetAllUnreadCounts(ctx)
	require.NoError(t, err)
	for _, id := range []string{"w1", "w2", "w3", "lead", "all", "grp", "off"} {
		want, err := s.LatestUnreadMessageID(ctx, "p", id)
		require.NoError(t, err)
		got := states["tmux-"+id]
		require.Equal(t, want, got.LatestID, "%s latest", id)
		require.Equal(t, counts["tmux-"+id], got.Count, "%s count", id)
	}
	// The cases the skip must not touch really differ from an empty state.
	require.NotZero(t, states["tmux-lead"].LatestID)
	require.Zero(t, states["tmux-grp"].Count)
	require.NotZero(t, states["tmux-grp"].LatestID)
	require.NotZero(t, states["tmux-w1"].LatestID)
	require.Zero(t, states["tmux-w2"].LatestID)
}
