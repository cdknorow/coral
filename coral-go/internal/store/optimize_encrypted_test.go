//go:build sqlcipher && cgo

package store

import (
	"context"
	"path/filepath"
	"testing"
)

// #4142: an encrypted database's writer gets the WAL limit from the SQLCipher connect hook (its
// DSN takes no _pragma keys) and can be optimized.
func TestEncryptedWriterLimitsTheWAL(t *testing.T) {
	db, err := OpenWithKey(context.Background(), filepath.Join(t.TempDir(), "sessions.db"), "test-only-key-material-1234567890")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var limit int64
	if err := db.Get(&limit, `PRAGMA journal_size_limit`); err != nil {
		t.Fatal(err)
	}
	if limit != JournalSizeLimit {
		t.Fatalf("journal_size_limit = %d, want %d", limit, JournalSizeLimit)
	}
	if err := db.Optimize(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}
