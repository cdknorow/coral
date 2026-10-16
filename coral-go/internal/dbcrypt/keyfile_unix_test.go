//go:build !windows

package dbcrypt

import (
	"os"
	"path/filepath"
	"testing"
)

// requirePrivateKeyFile: on Unix a key file is private by its mode.
func requirePrivateKeyFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key mode = %o, want 600", info.Mode().Perm())
	}
}

func TestExistingKeyRequiresSafeRegularFile(t *testing.T) {
	dir := t.TempDir()
	b := Bootstrap{DatabaseEncryption: "key_file", KeyFile: "custom.key"}
	path := filepath.Join(dir, b.KeyFile)
	if err := os.WriteFile(path, []byte("safe-key-material-123456"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveKey(dir, b); err == nil {
		t.Fatal("expected permissive key permissions to be rejected")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	key, err := ResolveKey(dir, b)
	if err != nil || key != "safe-key-material-123456" {
		t.Fatalf("valid key = %q, %v", key, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveKey(dir, b); err == nil {
		t.Fatal("expected directory key path to be rejected")
	}
}
