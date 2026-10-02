//go:build sqlcipher && cgo

package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedDatabaseRequiresSQLCipherKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	db, err := OpenWithKey(context.Background(), path, "test-only-key-material-1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_settings(key,value) VALUES('encryption_sentinel','secret-row')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw[:min(len(raw), 16)]) == "SQLite format 3\x00" {
		t.Fatal("encrypted database retained plaintext SQLite header")
	}
	plain, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.Ping(); err == nil {
		if _, err := plain.Query(`SELECT value FROM user_settings WHERE key='encryption_sentinel'`); err == nil {
			t.Fatal("stock SQLite unexpectedly read encrypted database")
		}
	}
	plain.Close()
	opened, err := OpenWithKey(context.Background(), path, "test-only-key-material-1234567890")
	if err != nil {
		t.Fatal("correct key failed: ", err)
	}
	var got string
	if err := opened.Get(&got, `SELECT value FROM user_settings WHERE key='encryption_sentinel'`); err != nil {
		t.Fatal(err)
	}
	if got != "secret-row" {
		t.Fatalf("sentinel = %q", got)
	}
	opened.Close()
	if _, err := OpenWithKey(context.Background(), path, "wrong-key-material-1234567890"); err == nil {
		t.Fatal("wrong key unexpectedly opened database")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
