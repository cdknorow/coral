//go:build sqlcipher && cgo

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cdknorow/coral/internal/dbcrypt"
)

const settingsTestKey = "settings-test-key-material-1234567890"

func pragma(t *testing.T, q interface {
	QueryRow(string, ...any) *sql.Row
}, name string) string {
	t.Helper()
	var v string
	if err := q.QueryRow("PRAGMA " + name).Scan(&v); err != nil {
		t.Fatalf("PRAGMA %s: %v", name, err)
	}
	return v
}

// #3454: an encrypted sessions database runs with the settings the plaintext one has.
// The SQLCipher driver ignored the _pragma_ DSN keys, so it ran in rollback-journal
// mode, without foreign keys, with a 5 s busy timeout.
func TestEncryptedSessionsDatabaseSettings(t *testing.T) {
	db, err := OpenWithKey(context.Background(), filepath.Join(t.TempDir(), "sessions.db"), settingsTestKey)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for name, want := range map[string]string{
		"journal_mode": "wal",
		"foreign_keys": "1",
		"busy_timeout": "30000",
		"synchronous":  "1", // NORMAL
		"temp_store":   "2", // MEMORY
		"cache_size":   "-8000",
		"query_only":   "0",
	} {
		if got := pragma(t, db, name); got != want {
			t.Errorf("PRAGMA %s = %s, want %s", name, got, want)
		}
	}
	// Foreign keys act: deleting a tag removes its session tags (ON DELETE CASCADE).
	if _, err := db.Exec(`INSERT INTO tags(id, name, color) VALUES (7, 't', '#fff'); INSERT INTO session_tags(session_id, tag_id) VALUES ('s', 7)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM tags WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_tags WHERE tag_id = 7`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("session_tags left after the tag was deleted: %d (%v)", n, err)
	}
}

// The read-only form (a read pool, a verification) refuses writes.
func TestEncryptedReadOnlyDSN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := OpenWithKey(context.Background(), path, settingsTestKey)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ro, err := sql.Open(dbcrypt.EncryptedDriver, dbcrypt.EncryptedDSN(path, settingsTestKey, dbcrypt.DSNOptions{ReadOnly: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if got := pragma(t, ro, "query_only"); got != "1" {
		t.Fatalf("query_only = %s", got)
	}
	if got := pragma(t, ro, "journal_mode"); got != "wal" {
		t.Fatalf("journal_mode = %s (the reader sees the writer's WAL)", got)
	}
	if _, err := ro.Exec(`INSERT INTO user_settings(key, value) VALUES ('x', 'y')`); err == nil {
		t.Fatal("a write through the read-only form succeeded")
	}
}

// Rows written while foreign keys were off are reported at startup, never refused.
func TestEncryptedForeignKeyOrphansAreReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := OpenWithKey(context.Background(), path, settingsTestKey)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	// As before #3454: the driver without foreign keys.
	old, err := sql.Open("sqlite3", path+"?_key="+dbcryptKeyHex(settingsTestKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO session_tags(session_id, tag_id) VALUES ('s', 999)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(prev)
	db, err = OpenWithKey(context.Background(), path, settingsTestKey)
	if err != nil {
		t.Fatalf("startup refused over an orphan: %v", err)
	}
	defer db.Close()
	out := logs.String()
	if !strings.Contains(out, "rows that break a foreign key") || !strings.Contains(out, "rows=1") || !strings.Contains(out, "by_table=map[session_tags->tags:1]") {
		t.Fatalf("no orphan report (counts by table only) in the log:\n%s", out)
	}
}

// dbcryptKeyHex is the key as the driver takes it (dbcrypt's encryptedKeyHex).
func dbcryptKeyHex(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// #3441: the encrypted DB reader pool uses query_only(1), sees the writer's WAL,
// and enforces read-only access.
func TestEncryptedDatabaseReaderPoolQueryOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := OpenWithKey(context.Background(), path, settingsTestKey)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	reader := db.Reader()
	if reader == db.DB {
		t.Fatal("expected separate reader pool for disk-backed encrypted db")
	}

	for name, want := range map[string]string{
		"journal_mode": "wal",
		"foreign_keys": "1",
		"busy_timeout": "30000",
		"synchronous":  "1",
		"temp_store":   "2",
		"cache_size":   "-8000",
		"query_only":   "1",
	} {
		if got := pragma(t, reader, name); got != want {
			t.Errorf("reader PRAGMA %s = %s, want %s", name, got, want)
		}
	}

	if _, err := reader.Exec(`INSERT INTO user_settings(key, value) VALUES ('k', 'v')`); err == nil {
		t.Fatal("expected write to fail on encrypted reader pool")
	}
}
