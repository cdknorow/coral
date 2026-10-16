package board

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func messageIDs(msgs []Message) []int64 {
	ids := make([]int64, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	return ids
}

func TestListMessagesKeyset(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sub(s, ctx, "proj", "agent-1", "Dev")
	sub(s, ctx, "other", "agent-2", "QA")
	var ids []int64
	for i := 0; i < 7; i++ {
		m, err := s.PostMessage(ctx, "proj", "agent-1", fmt.Sprintf("msg %d", i), nil)
		require.NoError(t, err)
		ids = append(ids, m.ID)
		// Interleave another board so ids are not contiguous per project.
		_, err = s.PostMessage(ctx, "other", "agent-2", "noise", nil)
		require.NoError(t, err)
	}

	// before_id: the newest page below the id, oldest first.
	got, err := s.ListMessagesBefore(ctx, "proj", ids[5], 3)
	require.NoError(t, err)
	require.Equal(t, ids[2:5], messageIDs(got))
	require.Equal(t, "Dev", got[0].JobTitle)
	got, err = s.ListMessagesBefore(ctx, "proj", ids[2], 3) // short last page
	require.NoError(t, err)
	require.Equal(t, ids[0:2], messageIDs(got))
	got, err = s.ListMessagesBefore(ctx, "proj", ids[0], 3) // nothing older
	require.NoError(t, err)
	require.Empty(t, got)
	got, err = s.ListMessagesBefore(ctx, "proj", ids[6]+1000, 2) // past the end
	require.NoError(t, err)
	require.Equal(t, ids[5:7], messageIDs(got))

	// after_id: the oldest page above the id.
	got, err = s.ListMessagesAfter(ctx, "proj", ids[1], 3)
	require.NoError(t, err)
	require.Equal(t, ids[2:5], messageIDs(got))
	got, err = s.ListMessagesAfter(ctx, "proj", 0, 2) // from the start
	require.NoError(t, err)
	require.Equal(t, ids[0:2], messageIDs(got))
	got, err = s.ListMessagesAfter(ctx, "proj", ids[6], 3) // nothing newer
	require.NoError(t, err)
	require.Empty(t, got)

	n, err := s.CountMessagesBefore(ctx, "proj", ids[3])
	require.NoError(t, err)
	require.Equal(t, 3, n)

	// A deleted message, even the boundary itself, leaves no gap or repeat.
	ok, err := s.DeleteMessage(ctx, "proj", ids[4])
	require.NoError(t, err)
	require.True(t, ok)
	got, err = s.ListMessagesBefore(ctx, "proj", ids[6], 3)
	require.NoError(t, err)
	require.Equal(t, []int64{ids[2], ids[3], ids[5]}, messageIDs(got))
	got, err = s.ListMessagesBefore(ctx, "proj", ids[4], 2)
	require.NoError(t, err)
	require.Equal(t, ids[2:4], messageIDs(got))
	got, err = s.ListMessagesAfter(ctx, "proj", ids[4], 5)
	require.NoError(t, err)
	require.Equal(t, ids[5:7], messageIDs(got))
	n, err = s.CountMessagesBefore(ctx, "proj", ids[6])
	require.NoError(t, err)
	require.Equal(t, 5, n)

	// Walking back page by page visits every message exactly once.
	var walked []int64
	before := ids[6] + 1
	for {
		page, err := s.ListMessagesBefore(ctx, "proj", before, 2)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		walked = append(messageIDs(page), walked...)
		before = page[0].ID
	}
	require.Equal(t, []int64{ids[0], ids[1], ids[2], ids[3], ids[5], ids[6]}, walked)
}

func TestListMessagesKeysetUsesProjectIndex(t *testing.T) {
	s := testStore(t)
	sub(s, context.Background(), "p", "agent-1", "Dev")
	// The production SQL (with the subscriber join and the re-sort wrapper).
	for _, tc := range []struct{ sql, want string }{
		{listMessagesBeforeSQL, "idx_board_messages_project (project=? AND id<?)"},
		{listMessagesAfterSQL, "idx_board_messages_project (project=? AND id>?)"},
		{countMessagesBeforeSQL, "idx_board_messages_project (project=? AND id<?)"},
	} {
		var plan []struct {
			ID     int    `db:"id"`
			Parent int    `db:"parent"`
			NotUse int    `db:"notused"`
			Detail string `db:"detail"`
		}
		args := []any{"p", 10, 5}
		if tc.sql == countMessagesBeforeSQL {
			args = args[:2]
		}
		require.NoError(t, s.db.Select(&plan, "EXPLAIN QUERY PLAN "+tc.sql, args...))
		var details []string
		found := false
		for _, row := range plan {
			details = append(details, row.Detail)
			found = found || strings.Contains(row.Detail, tc.want)
			require.NotContains(t, row.Detail, "SCAN m", tc.sql)
		}
		require.True(t, found, "plan %q does not use %s", details, tc.want)
	}
}

// TestListMessagesKeysetPerf compares OFFSET and keyset paging for the last
// pages of a large board. Skipped unless CORAL_PERF_BOARD is set.
func TestListMessagesKeysetPerf(t *testing.T) {
	if os.Getenv("CORAL_PERF_BOARD") == "" {
		t.Skip("set CORAL_PERF_BOARD=1 to run")
	}
	s := testStore(t)
	ctx := context.Background()
	sub(s, ctx, "big", "agent-1", "Dev")
	tx, err := s.db.Beginx()
	require.NoError(t, err)
	body := fmt.Sprintf("%0400d", 0)
	for i := 0; i < 25000; i++ {
		_, err := tx.Exec(`INSERT INTO board_messages (project, subscriber_id, session_id, content, created_at) VALUES ('big', 'agent-1', 's', ?, '2026-10-01T00:00:00Z')`, body)
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
	total, err := s.CountMessages(ctx, "big")
	require.NoError(t, err)
	latest, err := s.ListMessages(ctx, "big", 50, 0, 0)
	require.NoError(t, err)
	oldestLoaded := latest[0].ID
	timeIt := func(name string, fn func()) {
		fn()
		start := time.Now()
		for i := 0; i < 20; i++ {
			fn()
		}
		t.Logf("%-44s %s/op", name, (time.Since(start) / 20).Round(10*time.Microsecond))
	}
	timeIt("offset: newest page (offset=total-50)", func() { _, _ = s.ListMessages(ctx, "big", 50, total-50, 0) })
	timeIt("offset: page before it (offset=total-100)", func() { _, _ = s.ListMessages(ctx, "big", 50, total-100, 0) })
	timeIt("offset: poll for new (offset=total)", func() { _, _ = s.ListMessages(ctx, "big", 50, total, 0) })
	timeIt("latest page (offset=0)", func() { _, _ = s.ListMessages(ctx, "big", 50, 0, 0) })
	timeIt("keyset: page before it (before_id)", func() { _, _ = s.ListMessagesBefore(ctx, "big", oldestLoaded, 50) })
	timeIt("keyset: poll for new (after_id)", func() { _, _ = s.ListMessagesAfter(ctx, "big", latest[len(latest)-1].ID, 50) })
	timeIt("keyset: before_count", func() { _, _ = s.CountMessagesBefore(ctx, "big", oldestLoaded) })
	timeIt("total (CountMessages)", func() { _, _ = s.CountMessages(ctx, "big") })
}
