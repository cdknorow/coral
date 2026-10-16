//go:build windows

package dbcrypt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/secfile"
	"github.com/cdknorow/coral/internal/secfile/secfiletest"
)

// requirePrivateKeyFile: on Windows the mode says nothing (a writable file is 0666); the key
// file must have its own access list, only this user and SYSTEM (#3693, #3914).
func requirePrivateKeyFile(t *testing.T, path string) {
	t.Helper()
	secfiletest.RequirePrivate(t, path)
}

// The Windows twin of the Unix mode test (#3914): an existing key file another account can read
// is refused, a private one is used, and a folder at the key's name is refused.
func TestExistingKeyRequiresSafeRegularFile(t *testing.T) {
	dir := t.TempDir()
	b := Bootstrap{DatabaseEncryption: "key_file", KeyFile: "custom.key"}
	path := filepath.Join(dir, b.KeyFile)
	if err := os.WriteFile(path, []byte("safe-key-material-123456"), 0o600); err != nil {
		t.Fatal(err)
	}
	secfiletest.GrantUsersRead(t, path)
	if _, err := ResolveKey(dir, b); err == nil {
		t.Fatal("expected a key file other accounts can read to be rejected")
	}
	if err := secfile.Protect(path); err != nil {
		t.Fatal(err)
	}
	key, err := ResolveKey(dir, b)
	if err != nil || key != "safe-key-material-123456" {
		t.Fatalf("valid key = %q, %v", key, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveKey(dir, b); err == nil {
		t.Fatal("expected directory key path to be rejected")
	}
}
