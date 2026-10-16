package board

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestBoardPollAndSeedPerf times what perf pass #3833 found slow on a large board (#3845): the
// board chat's poll (the store calls ListAllMessages makes for format=dashboard) and the
// notifier's startup seed (GetAllUnreadStates), on the pass's board: 300,000 messages (80% of
// 80-500 bytes, 15% of 1-2 KB, 5% of 4-9 KB) and 40 subscribers. Only APIs that predate #3845
// are used, so the same file measures the code before and after. Skipped unless
// CORAL_PERF_BOARD is set; CORAL_PERF_BOARD_MESSAGES changes the size, and
// CORAL_PERF_BOARD_KEEP=<dir> also writes the two boards there (realistic.db, zero.db) for a
// server start.
func TestBoardPollAndSeedPerf(t *testing.T) {
	if os.Getenv("CORAL_PERF_BOARD") == "" {
		t.Skip("set CORAL_PERF_BOARD=1 to run")
	}
	n := 300000
	if v := os.Getenv("CORAL_PERF_BOARD_MESSAGES"); v != "" {
		_, err := fmt.Sscan(v, &n)
		require.NoError(t, err)
	}
	dir := t.TempDir()
	if keep := os.Getenv("CORAL_PERF_BOARD_KEEP"); keep != "" {
		dir = keep
	}
	ctx := context.Background()
	timeIt := func(name string, runs int, fn func() error) {
		require.NoError(t, fn()) // warm
		start := time.Now()
		for i := 0; i < runs; i++ {
			require.NoError(t, fn())
		}
		t.Logf("%-52s %s/op", name, (time.Since(start) / time.Duration(runs)).Round(10*time.Microsecond))
	}

	for _, cursors := range []string{"realistic", "zero"} {
		path := filepath.Join(dir, cursors+".db")
		buildPerfBoard(t, path, n, cursors == "zero")

		// The startup seed, as at a start: a fresh store, nothing cached.
		s, err := NewStoreWithKey(path, "")
		require.NoError(t, err)
		seedCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		start := time.Now()
		states, err := s.GetAllUnreadStates(seedCtx)
		cancel()
		if err != nil {
			t.Logf("%-52s did not finish in %s (%v)", "seed, "+cursors+" cursors (GetAllUnreadStates)", time.Since(start).Round(time.Second), err)
		} else {
			t.Logf("%-52s %s (%d sessions)", "seed, "+cursors+" cursors (GetAllUnreadStates)", time.Since(start).Round(time.Millisecond), len(states))
		}

		if cursors == "realistic" {
			latest, err := s.ListMessages(ctx, "big", 200, 0, 0)
			require.NoError(t, err)
			newest := latest[len(latest)-1].ID
			// A dashboard poll for what is new (after_id, nothing new): the list, the total,
			// and the count before the boundary.
			timeIt("poll: after_id, nothing new (list+total+before)", 20, func() error {
				if _, err := s.ListMessagesAfter(ctx, "big", newest, 200); err != nil {
					return err
				}
				if _, err := s.CountMessages(ctx, "big"); err != nil {
					return err
				}
				_, err := s.CountMessagesBefore(ctx, "big", newest+1)
				return err
			})
			timeIt("latest page (list+total+before)", 20, func() error {
				page, err := s.ListMessages(ctx, "big", 200, 0, 0)
				if err != nil {
					return err
				}
				if _, err := s.CountMessages(ctx, "big"); err != nil {
					return err
				}
				_, err = s.CountMessagesBefore(ctx, "big", page[0].ID)
				return err
			})
			timeIt("poll after a new post (post+list+total+before)", 20, func() error {
				m, err := s.PostMessage(ctx, "big", "agent-01", "a new message", nil)
				if err != nil {
					return err
				}
				if _, err := s.ListMessagesAfter(ctx, "big", m.ID-1, 200); err != nil {
					return err
				}
				if _, err := s.CountMessages(ctx, "big"); err != nil {
					return err
				}
				_, err = s.CountMessagesBefore(ctx, "big", m.ID)
				return err
			})
			mid := latest[0].ID - int64(n/2)
			timeIt("page from the middle (before_id: list+total+before)", 5, func() error {
				page, err := s.ListMessagesBefore(ctx, "big", mid, 200)
				if err != nil {
					return err
				}
				if _, err := s.CountMessages(ctx, "big"); err != nil {
					return err
				}
				_, err = s.CountMessagesBefore(ctx, "big", page[0].ID)
				return err
			})
		}
		require.NoError(t, s.Close())
	}
}

// buildPerfBoard writes the board: project "big", 40 subscribers (agent-00, the orchestrator,
// receives everything; the rest are mentions mode), one message in 200 tags a random agent.
// Realistic cursors: 37 subscribers within 300 messages of the newest, 3 with 60,000 unread.
// Zero cursors: none has ever read.
func buildPerfBoard(t *testing.T, path string, n int, zeroCursors bool) {
	t.Helper()
	_ = os.Remove(path)
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		id, title, mode := fmt.Sprintf("agent-%02d", i), fmt.Sprintf("Agent %02d", i), "mentions"
		if i == 0 {
			title, mode = "Orchestrator", "all"
		}
		_, err := s.Subscribe(ctx, "big", id, title, "perf-"+id, nil, mode)
		require.NoError(t, err)
	}
	rng := rand.New(rand.NewSource(3845))
	words := strings.Fields("the build passed and the review is in progress while tests run on the server " +
		"with a fix for the board query plan and the dashboard poll cost under load today")
	text := func(size int) string {
		var b strings.Builder
		for b.Len() < size {
			b.WriteString(words[rng.Intn(len(words))])
			b.WriteByte(' ')
		}
		return b.String()
	}
	tx, err := s.db.Beginx()
	require.NoError(t, err)
	stmt, err := tx.Preparex(`INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at)
		VALUES ('big', ?, ?, ?, '2026-10-16T00:00:00Z')`)
	require.NoError(t, err)
	for i := 0; i < n; i++ {
		var size int
		switch r := rng.Intn(100); {
		case r < 80:
			size = 80 + rng.Intn(420)
		case r < 95:
			size = 1024 + rng.Intn(1024)
		default:
			size = 4096 + rng.Intn(5120)
		}
		content := text(size)
		if rng.Intn(200) == 0 {
			content = fmt.Sprintf("@agent-%02d ", 1+rng.Intn(39)) + content
		}
		sender := fmt.Sprintf("agent-%02d", rng.Intn(40))
		_, err := stmt.Exec(sender, sender, content)
		require.NoError(t, err)
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())
	var head int64
	require.NoError(t, s.db.Get(&head, "SELECT MAX(id) FROM board_messages"))
	for i := 0; i < 40; i++ {
		cursor := int64(0)
		if !zeroCursors {
			cursor = head - int64(rng.Intn(300))
			if i >= 37 {
				cursor = head - 60000
			}
		}
		_, err := s.db.Exec("UPDATE board_subscribers SET last_read_id = ? WHERE subscriber_id = ?", max(cursor, 0), fmt.Sprintf("agent-%02d", i))
		require.NoError(t, err)
	}
}
