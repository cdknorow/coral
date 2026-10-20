package board

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func replyStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func join(t *testing.T, s *Store, project string, names ...string) {
	t.Helper()
	for _, n := range names {
		_, err := s.Subscribe(context.Background(), project, n, n, "claude-"+strings.ReplaceAll(n, " ", "-"), nil, "mentions")
		require.NoError(t, err)
	}
}

// #5463: the mention a reply to a task starts with tags exactly the creator, or there is none.
func TestTaskReplyMentionTagsExactlyTheCreatorOrNobody(t *testing.T) {
	ctx := context.Background()

	t.Run("an active creator is mentioned and would be notified", func(t *testing.T) {
		s := replyStore(t)
		join(t, s, "p", "Backend Dev", "Frontend Dev")
		m := s.TaskReplyMention(ctx, "p", "Backend Dev")
		assert.Equal(t, "@Backend Dev: ", m.Prefix)
		assert.Equal(t, []string{"Backend Dev"}, m.Notified)
		msg, err := s.PostMessageForTask(ctx, "p", "Frontend Dev", m.Prefix+"on it", nil, "", int64p(7))
		require.NoError(t, err)
		got, err := s.ReadMessages(ctx, "p", "Backend Dev", 10)
		require.NoError(t, err)
		require.Len(t, got, 1, "the creator's mention-mode read delivers the reply")
		assert.Equal(t, msg.ID, got[0].ID)
		other, _ := s.ReadMessages(ctx, "p", "Frontend Dev", 10)
		assert.Empty(t, other)
	})

	t.Run("a creator who left, while an active member has the shorter name: nobody is mentioned", func(t *testing.T) {
		s := replyStore(t)
		join(t, s, "p", "Backend Dev", "Backend Dev 2")
		_, err := s.Unsubscribe(ctx, "p", "Backend Dev 2")
		require.NoError(t, err)
		m := s.TaskReplyMention(ctx, "p", "Backend Dev 2")
		assert.Empty(t, m.Prefix, "'@Backend Dev 2 ' would tag the active 'Backend Dev'")
		assert.Empty(t, m.Notified)
		// and while the numbered agent is active the numbered creator is tagged alone
		_, err = s.Subscribe(ctx, "p", "Backend Dev 2", "Backend Dev 2", "claude-x", nil, "mentions")
		require.NoError(t, err)
		m = s.TaskReplyMention(ctx, "p", "Backend Dev 2")
		assert.Equal(t, "@Backend Dev 2: ", m.Prefix)
		assert.Equal(t, []string{"Backend Dev 2"}, m.Notified)
	})

	t.Run("a creator who left and nobody else matches: nobody is mentioned", func(t *testing.T) {
		s := replyStore(t)
		join(t, s, "p", "Backend Dev", "Frontend Dev")
		_, err := s.Unsubscribe(ctx, "p", "Backend Dev")
		require.NoError(t, err)
		assert.Empty(t, s.TaskReplyMention(ctx, "p", "Backend Dev").Prefix)
	})

	t.Run("another member whose job title is the creator's name: nobody is mentioned", func(t *testing.T) {
		s := replyStore(t)
		join(t, s, "p", "Backend Dev")
		_, err := s.Subscribe(ctx, "p", "Agent 9", "Backend Dev", "claude-9", nil, "mentions") // same job title
		require.NoError(t, err)
		assert.Empty(t, s.TaskReplyMention(ctx, "p", "Backend Dev").Prefix, "the text tags both")
	})

	t.Run("the spaceless form of another member: nobody is mentioned", func(t *testing.T) {
		s := replyStore(t)
		join(t, s, "p", "BackendDev", "Backend Dev")
		assert.Empty(t, s.TaskReplyMention(ctx, "p", "BackendDev").Prefix, "'@BackendDev ' is also the spaceless form of 'Backend Dev'")
		s2 := replyStore(t)
		join(t, s2, "p", "BackendDev", "Frontend Dev")
		assert.Equal(t, "@BackendDev: ", s2.TaskReplyMention(ctx, "p", "BackendDev").Prefix, "alone it is fine")
	})

	t.Run("a name that is not plain text, or a broadcast name, is never mentioned", func(t *testing.T) {
		s := replyStore(t)
		join(t, s, "p", "Backend Dev", "all")
		for _, creator := range []string{"", " Backend Dev", "Backend Dev ", "Back\nend", "Back\x1b[31mend", "Back\x00end", strings.Repeat("a", 200), "all", "notify-all"} {
			m := s.TaskReplyMention(ctx, "p", creator)
			assert.Empty(t, m.Prefix, "%q", creator)
			assert.Empty(t, m.Notified, "%q", creator)
		}
	})

	t.Run("the Operator, a member or not, is mentioned for the dashboard to notify", func(t *testing.T) {
		s := replyStore(t)
		join(t, s, "p", "Backend Dev")
		m := s.TaskReplyMention(ctx, "p", "Operator")
		assert.Equal(t, "@Operator: ", m.Prefix)
		assert.Equal(t, []string{"Operator"}, m.Notified)
		// but never when the text would also tag a member (an 'Operator' job title on another member)
		_, err := s.Subscribe(ctx, "p", "Agent 3", "Operator", "claude-3", nil, "mentions")
		require.NoError(t, err)
		assert.Empty(t, s.TaskReplyMention(ctx, "p", "Operator").Prefix)
	})

	t.Run("a creator that is not a member (a hub-made task) is not mentioned", func(t *testing.T) {
		s := replyStore(t)
		join(t, s, "p", "Backend Dev")
		assert.Empty(t, s.TaskReplyMention(ctx, "p", "hub-user-1234abcd").Prefix)
	})
}

func int64p(v int64) *int64 { return &v }

func TestListTaskRepliesAreThatTasksRepliesOfThatBoardOldestFirst(t *testing.T) {
	ctx := context.Background()
	s := replyStore(t)
	join(t, s, "p", "A", "B")
	join(t, s, "q", "A")
	var ids []int64
	for i := 0; i < 3; i++ {
		m, err := s.PostMessageForTask(ctx, "p", "A", fmt.Sprintf("reply %d", i), nil, "", int64p(5))
		require.NoError(t, err)
		ids = append(ids, m.ID)
	}
	_, err := s.PostMessageForTask(ctx, "p", "A", "another task", nil, "", int64p(6))
	require.NoError(t, err)
	_, err = s.PostMessage(ctx, "p", "A", "a plain post", nil)
	require.NoError(t, err)
	_, err = s.PostMessageForTask(ctx, "q", "A", "same task id, other board", nil, "", int64p(5))
	require.NoError(t, err)

	got, err := s.ListTaskReplies(ctx, "p", 5, 10)
	require.NoError(t, err)
	require.Len(t, got, 3)
	for i, m := range got {
		assert.Equal(t, ids[i], m.ID, "oldest first")
		require.NotNil(t, m.TaskID)
		assert.EqualValues(t, 5, *m.TaskID)
	}
	limited, _ := s.ListTaskReplies(ctx, "p", 5, 2)
	assert.Len(t, limited, 2)

	// a plain post has no task id, in the list reads too
	all, err := s.ListMessages(ctx, "p", 50, 0, 0)
	require.NoError(t, err)
	withTask := 0
	for _, m := range all {
		if m.TaskID != nil {
			withTask++
		}
	}
	assert.Equal(t, 4, withTask, "3 replies to task 5 and 1 to task 6 carry their task id; the plain post none")
}

func TestTheTaskReplyListUsesThePartialIndex(t *testing.T) {
	s := replyStore(t)
	var plan []struct {
		ID, Parent, Notused int
		Detail              string
	}
	require.NoError(t, s.db.Select(&plan, "EXPLAIN QUERY PLAN "+listMessagesSelect+" WHERE m.project = ? AND m.task_id = ? ORDER BY m.id ASC LIMIT ?", "p", 5, 10))
	found := false
	for _, r := range plan {
		if strings.Contains(r.Detail, "idx_board_messages_task") {
			found = true
		}
	}
	assert.True(t, found, "the query seeks idx_board_messages_task: %+v", plan)
	var ddl string
	require.NoError(t, s.db.Get(&ddl, "SELECT sql FROM sqlite_master WHERE name = 'idx_board_messages_task'"))
	assert.Contains(t, ddl, "WHERE task_id IS NOT NULL", "a partial index: building it touches no old row")
}

// An old database opened by the new code gets the column and the index, every old row keeps a null task id, and nothing but a reply sets one (#5463).
func TestAnOldBoardDatabaseGetsTheTaskColumnAndKeepsItsRows(t *testing.T) {
	for _, tag := range []string{"v1.4.80", "v1.4.110"} {
		t.Run(tag, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "messageboard.db")
			script, err := os.ReadFile(filepath.Join("testdata", "migration", tag, "messageboard.sql"))
			require.NoError(t, err)
			raw, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			_, err = raw.Exec(string(script))
			require.NoError(t, err)
			var before int
			require.NoError(t, raw.QueryRow(`SELECT count(*) FROM board_messages`).Scan(&before))
			require.NoError(t, raw.Close())
			assert.Equal(t, 4, before)

			s, err := NewStoreWithKey(path, "")
			require.NoError(t, err)
			defer s.Close()
			ctx := context.Background()
			var withTask, total int
			require.NoError(t, s.db.Get(&total, `SELECT count(*) FROM board_messages`))
			require.NoError(t, s.db.Get(&withTask, `SELECT count(*) FROM board_messages WHERE task_id IS NOT NULL`))
			assert.Equal(t, before, total, "no row was lost")
			assert.Equal(t, 0, withTask, "every old row has a null task id")
			var idx int
			require.NoError(t, s.db.Get(&idx, `SELECT count(*) FROM sqlite_master WHERE name = 'idx_board_messages_task'`))
			assert.Equal(t, 1, idx)

			// a plain post leaves it null, a reply sets it, and the old rows read back with no task id
			plain, err := s.PostMessage(ctx, "fixture-board", "Fixture Role 1", "plain", nil)
			require.NoError(t, err)
			assert.Nil(t, plain.TaskID)
			reply, err := s.PostMessageForTask(ctx, "fixture-board", "Fixture Role 1", "reply", nil, "", int64p(1))
			require.NoError(t, err)
			require.NotNil(t, reply.TaskID)
			msgs, err := s.ListMessages(ctx, "fixture-board", 50, 0, 0)
			require.NoError(t, err)
			require.Len(t, msgs, before+2)
			for _, m := range msgs {
				if m.ID == plain.ID || m.ID == reply.ID {
					continue
				}
				assert.Nil(t, m.TaskID)
			}
			again, err := s.ListTaskReplies(ctx, "fixture-board", 1, 10)
			require.NoError(t, err)
			require.Len(t, again, 1)
			assert.Equal(t, reply.ID, again[0].ID)
		})
	}
}

// #5466 (Security): the reply's own text must not change who the creator's mention tags. A reply that began with a digit turned "@Backend Dev " into "@Backend Dev 2" and
// notified the numbered agent instead of the creator.
func TestTheRepliesOwnTextCannotRedirectTheCreatorsMention(t *testing.T) {
	ctx := context.Background()
	s := replyStore(t)
	join(t, s, "p", "Backend Dev", "Backend Dev 2", "Frontend Dev")
	m := s.TaskReplyMention(ctx, "p", "Backend Dev")
	require.NotEmpty(t, m.Prefix)
	_, err := s.PostMessageForTask(ctx, "p", "Frontend Dev", m.Prefix+"2 tasks are ready", nil, "", int64p(7))
	require.NoError(t, err)
	creator, err := s.ReadMessages(ctx, "p", "Backend Dev", 10)
	require.NoError(t, err)
	other, err := s.ReadMessages(ctx, "p", "Backend Dev 2", 10)
	require.NoError(t, err)
	assert.Len(t, creator, 1, "the creator is notified")
	assert.Empty(t, other, "the numbered agent is not")
}
