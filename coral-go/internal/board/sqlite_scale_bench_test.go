package board

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// #5354: the paths Security's SQLite audit (#5318) left open, timed on a seeded 100,000-message board (about 300 bytes each, two senders, one
// board; never the live database):
//
//	go test ./internal/board -run xxx -bench SQLiteScale -benchtime 30x -benchmem
func seedMessages(b testing.TB, s *Store, n int) {
	b.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	words := strings.Fields("build test review deploy merge branch config server client latency cache index queue worker retry timeout schema migrate rollback release")
	for i := 0; i < n; i++ {
		sender := []string{"Dev", "QA"}[i%2]
		content := fmt.Sprintf("%s %s update %d: %s %s %s %s for %s %s %s", words[i%len(words)], words[(i*7)%len(words)], i, words[(i*3)%len(words)], words[(i*5)%len(words)],
			words[(i*11)%len(words)], words[(i*13)%len(words)], words[(i*17)%len(words)], words[(i*19)%len(words)], strings.Repeat("padding ", 20))
		if i%50000 == 0 {
			content += " needle" + fmt.Sprint(i)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO board_messages (project, session_id, subscriber_id, content, created_at, is_system) VALUES ('team', 'sess', ?, ?, '2026-10-20T08:00:00Z', 0)`, sender, content); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkSQLiteScale(b *testing.B) {
	s, err := NewStoreWithKey(filepath.Join(b.TempDir(), "board_bench.db"), "")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close() })
	ctx := context.Background()
	seedMessages(b, s, 100000)
	if _, err := s.Subscribe(ctx, "team", "Orchestrator", "Orchestrator", "tmux-orch", nil, "all"); err != nil {
		b.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE board_subscribers SET last_read_id = 0 WHERE project='team' AND subscriber_id='Orchestrator'`); err != nil {
		b.Fatal(err)
	}
	b.Run("HasUnreadHealthMessage_100k_unread_none_from_health", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if got, err := s.HasUnreadHealthMessage(ctx, "team", "Orchestrator"); err != nil || got {
				b.Fatal(got, err)
			}
		}
	})
	b.Run("SearchMessages_all_boards_rare_term", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if got, err := s.SearchMessages(ctx, "needle50000"); err != nil || len(got) != 1 {
				b.Fatal(got, err)
			}
		}
	})
	b.Run("SearchMessages_all_boards_common_term", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if got, err := s.SearchMessages(ctx, "deploy"); err != nil || len(got) != 1 {
				b.Fatal(got, err)
			}
		}
	})
	b.Run("SearchMessageHits_limit100_rare_term", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if got, err := s.SearchMessageHits(ctx, "needle50000", 100); err != nil || len(got) != 1 {
				b.Fatal(len(got), err)
			}
		}
	})
	b.Run("SearchMessageHits_limit100_common_term", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if got, err := s.SearchMessageHits(ctx, "deploy", 100); err != nil || len(got) != 100 {
				b.Fatal(len(got), err)
			}
		}
	})
}
