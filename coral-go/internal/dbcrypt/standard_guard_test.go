//go:build !(sqlcipher && cgo)

package dbcrypt

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// snapshot records every path under dir with a content hash so tests can prove
// a rejected operation changed nothing.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			lines = append(lines, "d "+rel)
			return nil
		}
		data, _ := os.ReadFile(path)
		sum := sha256.Sum256(data)
		lines = append(lines, fmt.Sprintf("f %s %x", rel, sum))
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func writeEncryptedLooking(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("not-a-plaintext-header-0123456789abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStandardBuildReportsEncryptionUnavailable(t *testing.T) {
	if FeatureAvailable() {
		t.Fatal("standard build must not report encryption as available")
	}
	if BuildVariant() != "standard" {
		t.Fatalf("variant = %q", BuildVariant())
	}
	if err := RequireAvailable(); !errors.Is(err, ErrEncryptionUnavailable) {
		t.Fatalf("RequireAvailable = %v", err)
	}
	if err := MigratePlaintexts([]string{filepath.Join(t.TempDir(), "x.db")}, "some-key-material-123456"); !errors.Is(err, ErrEncryptionUnavailable) {
		t.Fatalf("MigratePlaintexts = %v", err)
	}
}

func TestRejectUnsupportedEncryptionAllowsPlainHome(t *testing.T) {
	dir := t.TempDir()
	sessions, board := filepath.Join(dir, "sessions.db"), filepath.Join(dir, "messageboard.db")
	if err := RejectUnsupportedEncryption(dir, sessions, board); err != nil {
		t.Fatalf("empty home rejected: %v", err)
	}
	if err := Save(dir, Bootstrap{DatabaseEncryption: "disabled"}); err != nil {
		t.Fatal(err)
	}
	// A real plaintext SQLite header is accepted.
	if err := os.WriteFile(sessions, append([]byte("SQLite format 3\x00"), make([]byte, 64)...), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORAL_DB_ENCRYPTION", "disabled")
	if err := RejectUnsupportedEncryption(dir, sessions, board); err != nil {
		t.Fatalf("plaintext home rejected: %v", err)
	}
}

func TestRejectUnsupportedEncryptionRefusesWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir, sessions, board string)
	}{
		{"saved key_file mode", func(t *testing.T, dir, _, _ string) {
			if err := Save(dir, Bootstrap{DatabaseEncryption: "key_file", KeyFile: ".db_key"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"saved password mode", func(t *testing.T, dir, _, _ string) {
			if err := Save(dir, Bootstrap{DatabaseEncryption: "password", KeySalt: "00112233445566778899aabbccddeeff"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"env requests encryption", func(t *testing.T, _, _, _ string) { t.Setenv("CORAL_DB_ENCRYPTION", "key_file") }},
		{"env requests migration", func(t *testing.T, _, _, _ string) { t.Setenv("CORAL_DB_ENCRYPTION_MIGRATE", "1") }},
		{"encrypted sessions db", func(t *testing.T, _, s, _ string) { writeEncryptedLooking(t, s) }},
		{"encrypted board db", func(t *testing.T, _, _, b string) { writeEncryptedLooking(t, b) }},
		{"migration recovery marker", func(t *testing.T, dir, _, _ string) {
			if err := os.WriteFile(filepath.Join(dir, migrationStateName), []byte(`{"phase":"install"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sessions, board := filepath.Join(dir, "sessions.db"), filepath.Join(dir, "messageboard.db")
			tc.setup(t, dir, sessions, board)
			before := snapshot(t, dir)
			err := RejectUnsupportedEncryption(dir, sessions, board)
			if !errors.Is(err, ErrEncryptionUnavailable) {
				t.Fatalf("want ErrEncryptionUnavailable, got %v", err)
			}
			if !strings.Contains(err.Error(), "will not create, replace, migrate or downgrade") {
				t.Fatalf("error does not state the no-mutation guarantee: %v", err)
			}
			if after := snapshot(t, dir); after != before {
				t.Fatalf("rejected check mutated the home:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestRejectEncryptedFileIsReadOnlyAndExact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.db")
	if err := RejectEncryptedFile(path); err != nil {
		t.Fatalf("missing file must be allowed (fresh plaintext install): %v", err)
	}
	writeEncryptedLooking(t, path)
	before := snapshot(t, dir)
	if err := RejectEncryptedFile(path); !errors.Is(err, ErrEncryptionUnavailable) {
		t.Fatalf("encrypted header accepted: %v", err)
	}
	if snapshot(t, dir) != before {
		t.Fatal("guard modified the database file")
	}
}
