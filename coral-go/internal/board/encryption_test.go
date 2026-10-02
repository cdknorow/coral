//go:build fts5

package board

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedBoardStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messageboard.db")
	s, err := NewStoreWithKey(path, "board-test-key-material-1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 16 && string(raw[:16]) == "SQLite format 3\x00" {
		t.Fatal("encrypted board database retained plaintext SQLite header")
	}
	if _, err := NewStoreWithKey(path, "wrong-board-key-material-1234567890"); err == nil {
		t.Fatal("wrong board key unexpectedly opened database")
	}
}
