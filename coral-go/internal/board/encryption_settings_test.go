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
