//go:build sqlcipher && cgo

package dbcrypt

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigratePlaintextRetainsBackupAndData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	src, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Exec("CREATE TABLE sentinel(value TEXT); INSERT INTO sentinel VALUES ('private-row')"); err != nil {
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MigratePlaintext(path, "migration-key-material-1234567890"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".plaintext-backup"); err != nil {
		t.Fatal(err)
	}
	opened, err := sql.Open("sqlite3", encryptedDSN(path, "migration-key-material-1234567890"))
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := opened.QueryRow("SELECT value FROM sentinel").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "private-row" {
		t.Fatalf("value = %q", value)
	}
	opened.Close()
}

func TestMigratePlaintextsPreparesBothBeforeSwap(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "sessions.db")
	second := filepath.Join(dir, "messageboard.db")
	for _, path := range []string{first, second} {
		src, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := src.Exec("CREATE TABLE sentinel(value TEXT); INSERT INTO sentinel VALUES ('keep')"); err != nil {
			t.Fatal(err)
		}
		if err := src.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// An invalid second source must fail during preparation, before the first
	// canonical file is renamed or backed up.
	bad := filepath.Join(dir, "bad.db")
	if err := os.WriteFile(bad, []byte("SQLite format 3\x00not a valid database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := MigratePlaintexts([]string{first, bad}, "pair-migration-key-123456789"); err == nil {
		t.Fatal("expected pair migration failure")
	}
	if _, err := os.Stat(first + ".plaintext-backup"); !os.IsNotExist(err) {
		t.Fatalf("unexpected first backup: %v", err)
	}
	if encrypted, err := EncryptedFile(first); err != nil || encrypted {
		t.Fatalf("first source changed: encrypted=%v err=%v", encrypted, err)
	}
	if err := MigratePlaintexts([]string{first, second}, "pair-migration-key-123456789"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{first, second} {
		if encrypted, err := EncryptedFile(path); err != nil || !encrypted {
			t.Fatalf("%s not encrypted: %v %v", path, encrypted, err)
		}
	}
}

func TestMigrationRecoveryMarkerBlocksRetry(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "sessions.db"), filepath.Join(dir, "messageboard.db")}
	marker := MigrationRecoveryPath(paths)
	if err := os.WriteFile(marker, []byte(`{"phase":"installing","paths":["sessions.db","messageboard.db"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckMigrationRecovery(paths); err == nil {
		t.Fatal("expected interrupted migration to require recovery")
	}
	if err := MigratePlaintexts(paths, "recovery-test-key-123456789"); err == nil {
		t.Fatal("expected migration retry to remain blocked")
	}
}

func TestMigrationProcessTerminationRecovery(t *testing.T) {
	if os.Getenv("CORAL_DBCRYPT_MIGRATION_CHILD") == "1" {
		paths := []string{os.Getenv("CORAL_DBCRYPT_FIRST"), os.Getenv("CORAL_DBCRYPT_SECOND")}
		_ = MigratePlaintexts(paths, "subprocess-recovery-key-123456789")
		return
	}
	dir := t.TempDir()
	first, second := filepath.Join(dir, "sessions.db"), filepath.Join(dir, "messageboard.db")
	for _, path := range []string{first, second} {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("CREATE TABLE sentinel(value TEXT); INSERT INTO sentinel VALUES ('preserve-me')"); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMigrationProcessTerminationRecovery$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		"CORAL_DBCRYPT_MIGRATION_CHILD=1",
		"CORAL_DBCRYPT_TEST_EXIT_PHASE=installed_0",
		"CORAL_DBCRYPT_FIRST="+first,
		"CORAL_DBCRYPT_SECOND="+second,
	)
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 91 {
		t.Fatalf("child exit = %v, want status 91", err)
	}
	paths := []string{first, second}
	if err := CheckMigrationRecovery(paths); err == nil {
		t.Fatal("expected restart refusal after process termination")
	}
	if _, err := os.Stat(first + ".plaintext-backup"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(second + ".plaintext-backup"); err != nil {
		t.Fatal(err)
	}
	// Documented recovery: restore both canonical plaintext files from their
	// backups, discard the partial encrypted output and marker, then retry.
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(first+".plaintext-backup", first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(second); err == nil {
		if err := os.Remove(second); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(second+".plaintext-backup", second); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(first + ".encrypted.tmp")
	_ = os.Remove(second + ".encrypted.tmp")
	_ = os.Remove(MigrationRecoveryPath(paths))
	if err := MigratePlaintexts(paths, "subprocess-recovery-key-123456789"); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if encrypted, err := EncryptedFile(path); err != nil || !encrypted {
			t.Fatalf("%s recovery encrypted=%v err=%v", path, encrypted, err)
		}
		db, err := sql.Open("sqlite3", encryptedDSN(path, "subprocess-recovery-key-123456789"))
		if err != nil {
			t.Fatal(err)
		}
		var value string
		if err := db.QueryRow("SELECT value FROM sentinel").Scan(&value); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
		if value != "preserve-me" {
			t.Fatalf("%s sentinel=%q", path, value)
		}
	}
}
