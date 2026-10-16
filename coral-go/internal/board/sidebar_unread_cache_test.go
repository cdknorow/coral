package board

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// uncachedSidebarUnread is getUnreadCounts' rule applied from scratch: every
// active subscriber's eligible messages after its cursor, summed per session.
func uncachedSidebarUnread(t *testing.T, s *Store, sessionNames []string) map[string]int {
	t.Helper()
	var subs []struct {
		Project      string `db:"project"`
		SubscriberID string `db:"subscriber_id"`
		SessionName  string `db:"session_name"`
		JobTitle     string `db:"job_title"`
		LastReadID   int64  `db:"last_read_id"`
		ReceiveMode  string `db:"receive_mode"`
	}
	require.NoError(t, s.db.Select(&subs, "SELECT project, subscriber_id, session_name, job_title, last_read_id, receive_mode FROM board_subscribers WHERE is_active = 1"))
	want := map[string]bool{}
	for _, n := range sessionNames {
		want[n] = true
	}
	var groupRows []struct {
		Project string `db:"project"`
		GroupID string `db:"group_id"`
		Member  string `db:"subscriber_id"`
	}
	require.NoError(t, s.db.Select(&groupRows, "SELECT project, group_id, subscriber_id FROM board_groups"))
	inGroup := map[[3]string]bool{}
	for _, g := range groupRows {
		inGroup[[3]string{g.Project, g.GroupID, g.Member}] = true
	}
	result := map[string]int{}
	for _, sub := range subs {
		if sessionNames != nil && !want[sub.SessionName] {
			continue
		}
		result[sub.SessionName] += 0
		mode := sub.ReceiveMode
		if mode == "none" {
			continue
		}
		if mode == "" {
			mode = "mentions"
		}
		var msgs []struct {
			Sender  string `db:"subscriber_id"`
			Content string `db:"content"`
		}
		require.NoError(t, s.db.Select(&msgs, "SELECT subscriber_id, content FROM board_messages WHERE project = ? AND id > ? AND subscriber_id != 'Coral Task Queue'", sub.Project, sub.LastReadID))
		for _, m := range msgs {
			if m.Sender == sub.SubscriberID {
				continue
			}
			switch mode {
			case "all":
				result[sub.SessionName]++
			case "mentions":
				if containsExplicitTag(m.Content, sub.SubscriberID, sub.JobTitle) {
					result[sub.SessionName]++
				}
			default:
				if inGroup[[3]string{sub.Project, mode, m.Sender}] {
					result[sub.SessionName]++
				}
			}
		}
	}
	return result
}

func TestGetUnreadCounts_IncrementalMatchesFullCount(t *testing.T) {
	ctx := context.Background()
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "board.db"), "")
	require.NoError(t, err)
	defer s.Close()

	boards := []string{"alpha", "beta"}
	subs := []string{"Agent 01", "Agent 02", "Lead", "Reviewer"}
	modes := []string{"mentions", "all", "devs", "none", ""}
	senders := []string{"Agent 01", "Agent 02", "Lead", "Reviewer", "Orchestrator", "Coral Task Queue"}
	mentions := []string{"", "@Agent 01 ", "@all ", "Agent 02: ", "@lead ", "x@agent 01.com ", "@Developer "}
	session := func(board, sub string) string { return "sess-" + board + "-" + sub }
	subscribe := func(board, sub string) {
		_, err := s.Subscribe(ctx, board, sub, "Developer", session(board, sub), nil, modes[0])
		require.NoError(t, err)
	}
	for _, b := range boards {
		for _, sub := range subs {
			subscribe(b, sub)
		}
		// One session subscribed to both boards: its counts are summed.
		_, err := s.Subscribe(ctx, b, "Shared", "Developer", "sess-shared", nil, "all")
		require.NoError(t, err)
	}

	rng := rand.New(rand.NewSource(2913))
	ops := map[string]int{}
	for step := 0; step < equivalenceSteps; step++ {
		board := boards[rng.Intn(len(boards))]
		sub := subs[rng.Intn(len(subs))]
		switch op := rng.Intn(100); {
		case op < 62:
			_, err := s.PostMessage(ctx, board, senders[rng.Intn(len(senders))], fmt.Sprintf("%smsg %d", mentions[rng.Intn(len(mentions))], step), nil)
			require.NoError(t, err)
			ops["post"]++
		case op < 72:
			_, err := s.ReadMessages(ctx, board, sub, 1+rng.Intn(15))
			require.NoError(t, err)
			ops["read"]++
		case op < 79:
			var ids []int64
			require.NoError(t, s.db.Select(&ids, "SELECT id FROM board_messages WHERE project = ?", board))
			if len(ids) > 0 {
				_, err := s.DeleteMessage(ctx, board, ids[rng.Intn(len(ids))])
				require.NoError(t, err)
				ops["delete"]++
			}
		case op < 86:
			_, err := s.db.Exec("UPDATE board_subscribers SET receive_mode = ? WHERE project = ? AND subscriber_id = ?", modes[rng.Intn(len(modes))], board, sub)
			require.NoError(t, err)
			ops["mode"]++
		case op < 93:
			require.NoError(t, s.AddToGroup(ctx, board, "devs", senders[rng.Intn(len(senders))]))
			ops["group+"]++
		case op < 98:
			_, err := s.RemoveFromGroup(ctx, board, "devs", senders[rng.Intn(len(senders))])
			require.NoError(t, err)
			ops["group-"]++
		default:
			_, err := s.db.Exec("UPDATE board_subscribers SET job_title = ? WHERE project = ? AND subscriber_id = ?", []string{"Developer", "Lead", "QA"}[rng.Intn(3)], board, sub)
			require.NoError(t, err)
			ops["title"]++
		}
		all, err := s.GetAllUnreadCounts(ctx)
		require.NoError(t, err)
		require.Equal(t, uncachedSidebarUnread(t, s, nil), all, "step %d: all sessions", step)
		some := []string{session("alpha", "Agent 01"), session("beta", "Lead"), "sess-shared"}
		got, err := s.GetUnreadCountsForSessions(ctx, some)
		require.NoError(t, err)
		require.Equal(t, uncachedSidebarUnread(t, s, some), got, "step %d: some sessions", step)
	}
	for _, op := range []string{"post", "read", "delete", "mode", "group+", "group-", "title"} {
		require.Positive(t, ops[op], op)
	}
}
