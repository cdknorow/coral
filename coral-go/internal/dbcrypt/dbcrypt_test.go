package dbcrypt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareStorageKeyUsesPasswordKDF(t *testing.T) {
	salt := "00112233445566778899aabbccddeeff"
	first, err := PrepareStorageKey("correct horse battery staple", "password", salt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareStorageKey("correct horse battery staple", "password", "ffeeddccbbaa99887766554433221100")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first == "rawhex:"+encryptedKeyHex("correct horse battery staple") {
		t.Fatal("password used an unsalted single-hash shortcut")
	}
	if _, err := PrepareStorageKey("password", "password", ""); err == nil {
		t.Fatal("missing salt accepted")
	}
}

func TestResolveKeyGeneratesOnlyForNewDatabases(t *testing.T) {
	dir := t.TempDir()
	b := Bootstrap{DatabaseEncryption: "key_file"}
	key, err := ResolveKey(dir, b, filepath.Join(dir, "sessions.db"), filepath.Join(dir, "messageboard.db"))
	if err != nil || len(key) < 16 {
		t.Fatalf("ResolveKey() = %q, %v", key, err)
	}
	info, err := os.Stat(filepath.Join(dir, KeyName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key mode = %o, want 600", info.Mode().Perm())
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions.db"), []byte("plaintext"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, KeyName)); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveKey(dir, b, filepath.Join(dir, "sessions.db")); err == nil {
		t.Fatal("expected missing key failure for existing database")
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

func TestExistingKeySymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	b := Bootstrap{DatabaseEncryption: "key_file"}
	target := filepath.Join(dir, "outside.key")
	if err := os.WriteFile(target, []byte("safe-key-material-123456"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, KeyName)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ResolveKey(dir, b); err == nil {
		t.Fatal("expected symlink key path to be rejected")
	}
}

func TestSaveAndLoadBootstrap(t *testing.T) {
	dir := t.TempDir()
	want := Bootstrap{DatabaseEncryption: "key_file", KeyFile: ".db_key"}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("bootstrap = %#v, want %#v", got, want)
	}
}

func TestLoadBootstrapDefaultsOnlyWhenMissingOrEmpty(t *testing.T) {
	dir := t.TempDir()
	got, err := Load(dir)
	if err != nil || got.DatabaseEncryption != "" {
		t.Fatalf("missing bootstrap = %#v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, BootstrapName), []byte(`{"database_encryption":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = Load(dir)
	if err != nil || got.DatabaseEncryption != "disabled" {
		t.Fatalf("empty bootstrap = %#v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, BootstrapName), []byte(`{"database_encryption":"unsupported"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("unsupported bootstrap mode must fail closed")
	}
}
