//go:build sqlcipher && cgo

package board

import (
	"path/filepath"
	"testing"
)

// #3454: the encrypted board database runs with the plaintext one's settings (WAL,
// a 30 s busy timeout, NORMAL sync, temp tables in memory, an 8 MB cache).
func TestEncryptedBoardDatabaseSettings(t *testing.T) {
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "messageboard.db"), "board-settings-key-material-1234567890")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for name, want := range map[string]string{
		"journal_mode": "wal",
		"busy_timeout": "30000",
		"synchronous":  "1",
		"temp_store":   "2",
		"cache_size":   "-8000",
	} {
		var got string
		if err := s.db.QueryRow("PRAGMA " + name).Scan(&got); err != nil {
			t.Fatal(name, err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %s, want %s", name, got, want)
		}
	}
}

// #3441: the encrypted board database reader pool runs with query_only(1).
func TestEncryptedBoardDatabaseReaderPoolQueryOnly(t *testing.T) {
	s, err := NewStoreWithKey(filepath.Join(t.TempDir(), "messageboard.db"), "board-settings-key-material-1234567890")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.readDB == s.db {
		t.Fatal("expected separate reader pool for disk-backed encrypted board db")
	}

	for name, want := range map[string]string{
		"journal_mode": "wal",
		"busy_timeout": "30000",
		"synchronous":  "1",
		"temp_store":   "2",
		"cache_size":   "-8000",
		"query_only":   "1",
	} {
		var got string
		if err := s.readDB.QueryRow("PRAGMA " + name).Scan(&got); err != nil {
			t.Fatal(name, err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %s, want %s", name, got, want)
		}
	}

	if _, err := s.readDB.Exec(`INSERT INTO subscribers(project, subscriber_id, session_name) VALUES ('p', 's', 's')`); err == nil {
		t.Fatal("expected write to fail on encrypted board reader pool")
	}
}
