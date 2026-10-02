//go:build !(sqlcipher && cgo)

package board

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/dbcrypt"
)

func TestStandardBoardStoreRejectsKeyAndEncryptedFileWithoutMutation(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "new-dir", "messageboard.db")
	if _, err := NewStoreWithKey(missing, "test-only-key-material-1234567890"); !errors.Is(err, dbcrypt.ErrEncryptionUnavailable) {
		t.Fatalf("keyed open: want ErrEncryptionUnavailable, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Dir(missing)); !os.IsNotExist(statErr) {
		t.Fatalf("rejected keyed open created a directory (%v)", statErr)
	}

	path := filepath.Join(root, "messageboard.db")
	original := []byte("encrypted-looking-bytes-not-sqlite-header")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); !errors.Is(err, dbcrypt.ErrEncryptionUnavailable) {
		t.Fatalf("encrypted file: want ErrEncryptionUnavailable, got %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(original) {
		t.Fatal("encrypted board file was modified")
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("rejected open left extra files: %v", entries)
	}
}

func TestStandardBoardStorePlaintextStillWorks(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "messageboard.db"))
	if err != nil {
		t.Fatalf("standard plaintext board open failed: %v", err)
	}
	defer s.Close()
}
