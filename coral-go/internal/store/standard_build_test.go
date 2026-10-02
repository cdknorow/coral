//go:build !(sqlcipher && cgo)

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/dbcrypt"
)

func TestStandardBuildOpensPlaintextWithPureGoDriver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := OpenWithContext(context.Background(), path)
	if err != nil {
		t.Fatalf("standard plaintext open failed: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO user_settings(key,value) VALUES('standard','ok')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 16)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Read(header); err != nil || string(header) != "SQLite format 3\x00" {
		t.Fatalf("database is not plaintext SQLite: %q %v", header, err)
	}
}

func TestStandardBuildRejectsKeyWithoutCreatingAnything(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created-yet")
	path := filepath.Join(dir, "sessions.db")
	_, err := OpenWithKey(context.Background(), path, "test-only-key-material-1234567890")
	if !errors.Is(err, dbcrypt.ErrEncryptionUnavailable) {
		t.Fatalf("want ErrEncryptionUnavailable, got %v", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("rejected keyed open created %s (%v)", dir, statErr)
	}
}

func TestStandardBuildRejectsEncryptedFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	original := []byte("encrypted-looking-bytes-not-sqlite-header")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	_, err := OpenWithContext(context.Background(), path)
	if !errors.Is(err, dbcrypt.ErrEncryptionUnavailable) {
		t.Fatalf("want ErrEncryptionUnavailable, got %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != string(original) {
		t.Fatalf("encrypted file was modified: %q %v", got, readErr)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("rejected open left extra files (wal/shm/backup): %v", entries)
	}
}
