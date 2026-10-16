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
	requirePrivateKeyFile(t, filepath.Join(dir, KeyName)) // mode 0600, or the Windows access list
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
