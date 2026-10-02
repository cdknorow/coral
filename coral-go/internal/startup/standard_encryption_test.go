//go:build !(sqlcipher && cgo)

package startup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/dbcrypt"
)

// A standard build must refuse an encrypted setup before creating, saving,
// locking or opening anything in the Coral home.
func TestStartRejectsEncryptedSetupWithoutTouchingHome(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, home string)
	}{
		{"saved encryption mode", func(t *testing.T, home string) {
			if err := dbcrypt.Save(home, dbcrypt.Bootstrap{DatabaseEncryption: "key_file", KeyFile: ".db_key"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"encrypted sessions database", func(t *testing.T, home string) {
			if err := os.WriteFile(filepath.Join(home, "sessions.db"), []byte("encrypted-looking-bytes-not-sqlite-header"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"environment requests encryption", func(t *testing.T, _ string) { t.Setenv("CORAL_DB_ENCRYPTION", "password") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			tc.setup(t, home)
			before := listDir(t, home)
			cfg := config.Load(home)
			_, err := Start(context.Background(), cfg, Options{BackendType: "pty"})
			if !errors.Is(err, dbcrypt.ErrEncryptionUnavailable) {
				t.Fatalf("want ErrEncryptionUnavailable, got %v", err)
			}
			if after := listDir(t, home); after != before {
				t.Fatalf("rejected startup changed the home:\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

func listDir(t *testing.T, dir string) string {
	t.Helper()
	var out string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(dir, path)
		info, _ := d.Info()
		out += rel + ":" + info.Mode().String() + ":" + itoa(info.Size()) + ";"
		return nil
	})
	return out
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
