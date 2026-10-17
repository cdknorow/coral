package board

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestReadMessagesMentionPerf times ReadMessages for a mentions-mode subscriber on perf pass #3833's board
// (buildPerfBoard; 1 message in 200 mentions an agent): the call agents poll with, which builds an OR-of-LIKE
// query for the subscriber's mention terms (#4465). It re-reads without moving the cursor (the call itself
// only moves it past what it returns and the subscriber's own messages). Skipped unless CORAL_PERF_BOARD is
// set; CORAL_PERF_BOARD_MESSAGES changes the size (default 100000).
func TestReadMessagesMentionPerf(t *testing.T) {
	if os.Getenv("CORAL_PERF_BOARD") == "" {
		t.Skip("set CORAL_PERF_BOARD=1 to run")
	}
	n := 100000
	if v := os.Getenv("CORAL_PERF_BOARD_MESSAGES"); v != "" {
		_, err := fmt.Sscan(v, &n)
		require.NoError(t, err)
	}
	path := filepath.Join(t.TempDir(), "perf.db")
	buildPerfBoard(t, path, n, false)
	s, err := NewStoreWithKey(path, "")
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	for _, c := range []struct{ name, sub string }{{"cursor near the head", "agent-05"}, {"cursor 60,000 behind", "agent-38"}} {
		var last int64
		require.NoError(t, s.db.Get(&last, "SELECT last_read_id FROM board_subscribers WHERE subscriber_id = ?", c.sub))
		read := func() error {
			if _, err := s.db.Exec("UPDATE board_subscribers SET last_read_id = ? WHERE subscriber_id = ?", last, c.sub); err != nil {
				return err
			}
			_, err := s.ReadMessages(ctx, "big", c.sub, 50, false)
			return err
		}
		require.NoError(t, read())
		const runs = 20
		start := time.Now()
		for i := 0; i < runs; i++ {
			require.NoError(t, read())
		}
		t.Logf("%-24s ReadMessages %s/op", c.name, (time.Since(start) / runs).Round(10*time.Microsecond))
	}
}
