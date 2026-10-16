package board

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// uncachedMentionsUnread is CheckUnread's mentions-mode query as it was before
// the count became incremental: a full COUNT from the cursor.
func uncachedMentionsUnread(t *testing.T, s *Store, project, subscriberID string) int {
	t.Helper()
	var sub struct {
		LastReadID  int64  `db:"last_read_id"`
		JobTitle    string `db:"job_title"`
		ReceiveMode string `db:"receive_mode"`
	}
	if err := s.db.Get(&sub, "SELECT last_read_id, job_title, receive_mode FROM board_subscribers WHERE project = ? AND subscriber_id = ?", project, subscriberID); err != nil {
		return 0
	}
	require.Contains(t, []string{"", "mentions"}, sub.ReceiveMode)
	terms := mentionTerms(subscriberID, sub.JobTitle)
	clauses := make([]string, len(terms))
	args := []any{project, sub.LastReadID, subscriberID}
	for i, term := range terms {
		clauses[i] = "content LIKE ? COLLATE NOCASE"
		args = append(args, "%"+term+"%")
	}
	// The SQL finds candidates; each counts only if it really tags the subscriber, the rule
	// ReadMessages uses (word boundaries; "@Backend Dev 2" isn't "@Backend Dev", #3543).
	var contents []string
	require.NoError(t, s.db.Select(&contents, `SELECT content FROM board_messages
	 WHERE project = ? AND id > ? AND subscriber_id != ? AND subscriber_id NOT IN ('Coral Task Queue') AND (`+strings.Join(clauses, " OR ")+`)`, args...))
	count := 0
	numbered := s.numberedNames(context.Background(), s.db, project)
	for _, c := range contents {
		if containsExplicitTag(c, subscriberID, sub.JobTitle, numbered) {
			count++
		}
	}
	return count
}

// Random posts, reads, deletes, title changes and board deletion: after every
// step the incremental count must equal a full recount.
func TestCheckUnread_IncrementalMentionsCountMatchesFullCount(t *testing.T) {
	ctx := context.Background()
	s, err := NewStore(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	defer s.Close()

	boards := []string{"alpha", "beta"}
	subs := []string{"Agent 01", "Agent 02", "Reviewer"}
	senders := []string{"Agent 01", "Agent 02", "Reviewer", "Orchestrator", "Coral Task Queue", "Coral Health Monitor"}
	mentions := []string{"", "@Agent 01 ", "@Agent01 ", "@all ", "Agent 02: ", "@reviewer ", "@QA Engineer ", "user@agent 01.com ", "Agent 01 — "}
	subscribe := func(board string) {
		for _, sub := range subs {
			_, err := s.Subscribe(ctx, board, sub, "Developer", "sess-"+board+"-"+sub, nil, "mentions")
			require.NoError(t, err)
		}
	}
	for _, b := range boards {
		subscribe(b)
	}

	rng := rand.New(rand.NewSource(2905))
	var deletions, reads, titles int
	for step := 0; step < equivalenceSteps; step++ {
		board := boards[rng.Intn(len(boards))]
		switch op := rng.Intn(100); {
		case op < 70:
			content := fmt.Sprintf("%smessage %d", mentions[rng.Intn(len(mentions))], step)
			_, err := s.PostMessage(ctx, board, senders[rng.Intn(len(senders))], content, nil)
			require.NoError(t, err)
		case op < 82:
			_, err := s.ReadMessages(ctx, board, subs[rng.Intn(len(subs))], 1+rng.Intn(20))
			require.NoError(t, err)
			reads++
		case op < 92:
			var ids []int64
			require.NoError(t, s.db.Select(&ids, "SELECT id FROM board_messages WHERE project = ?", board))
			if len(ids) > 0 {
				_, err := s.DeleteMessage(ctx, ids[rng.Intn(len(ids))])
				require.NoError(t, err)
				deletions++
			}
		case op < 98:
			title := []string{"Developer", "QA Engineer", "Reviewer"}[rng.Intn(3)]
			_, err := s.db.Exec("UPDATE board_subscribers SET job_title = ? WHERE project = ? AND subscriber_id = ?", title, board, subs[rng.Intn(len(subs))])
			require.NoError(t, err)
			titles++
		default:
			require.NoError(t, s.DeleteProject(ctx, board))
			subscribe(board)
		}
		for _, b := range boards {
			for _, sub := range subs {
				got, err := s.CheckUnread(ctx, b, sub)
				require.NoError(t, err)
				require.Equal(t, uncachedMentionsUnread(t, s, b, sub), got, "step %d board %s subscriber %s", step, b, sub)
			}
		}
	}
	require.Positive(t, deletions)
	require.Positive(t, reads)
	require.Positive(t, titles)
}

func TestUnreadCache_StrictBoundAndDeleteBrackets(t *testing.T) {
	ctx := context.Background()
	s, err := NewStore(filepath.Join(t.TempDir(), "board.db"))
	require.NoError(t, err)
	defer s.Close()
	_, err = s.Subscribe(ctx, "team", "Dev", "Developer", "sess-dev", nil, "mentions")
	require.NoError(t, err)
	msg, err := s.PostMessage(ctx, "team", "Lead", "@Dev hello", nil)
	require.NoError(t, err)

	n, err := s.CheckUnread(ctx, "team", "Dev")
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, err = s.GetUnreadCountsForSessions(ctx, []string{"sess-dev"})
	require.NoError(t, err)
	cached := func() (int, int) {
		s.unreadCounts.mu.Lock()
		defer s.unreadCounts.mu.Unlock()
		return len(s.unreadCounts.entries), len(s.unreadCounts.sidebar)
	}
	mentions, sidebar := cached()
	require.Equal(t, 1, mentions)
	require.Equal(t, 1, sidebar)

	// Unsubscribing drops the subscriber's entries.
	_, err = s.Unsubscribe(ctx, "team", "Dev")
	require.NoError(t, err)
	mentions, sidebar = cached()
	require.Zero(t, mentions)
	require.Zero(t, sidebar)

	// A subscriber whose row is gone is dropped when it is next asked about.
	_, err = s.Subscribe(ctx, "team", "Gone", "Developer", "sess-gone", nil, "mentions")
	require.NoError(t, err)
	_, err = s.CheckUnread(ctx, "team", "Gone")
	require.NoError(t, err)
	_, err = s.db.Exec("DELETE FROM board_subscribers WHERE subscriber_id = 'Gone'")
	require.NoError(t, err)
	_, err = s.CheckUnread(ctx, "team", "Gone")
	require.NoError(t, err)
	mentions, _ = cached()
	require.Zero(t, mentions)

	// Deletes invalidate both before and after the DELETE.
	before := s.unreadCounts.epoch.Load()
	_, err = s.DeleteMessage(ctx, msg.ID)
	require.NoError(t, err)
	require.Equal(t, before+2, s.unreadCounts.epoch.Load())
	before = s.unreadCounts.epoch.Load()
	require.NoError(t, s.DeleteProject(ctx, "team"))
	require.Equal(t, before+2, s.unreadCounts.epoch.Load())

	// Group changes too (group-mode eligibility depends on membership).
	before = s.unreadCounts.epoch.Load()
	require.NoError(t, s.AddToGroup(ctx, "team", "devs", "Lead"))
	require.Equal(t, before+2, s.unreadCounts.epoch.Load())
	before = s.unreadCounts.epoch.Load()
	_, err = s.RemoveFromGroup(ctx, "team", "devs", "Lead")
	require.NoError(t, err)
	require.Equal(t, before+2, s.unreadCounts.epoch.Load())
}
