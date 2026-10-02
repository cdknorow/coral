// Package dbcrypt provides the startup contract for optional SQLCipher
// databases.  It deliberately keeps the password outside of configuration and
// never logs key material.
package dbcrypt

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"

	_ "github.com/0xCarbon/go-sqlite3"
)

func encryptedKeyHex(key string) string {
	if strings.HasPrefix(key, "rawhex:") {
		return strings.TrimPrefix(key, "rawhex:")
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func encryptedDSN(path, key string) string {
	return path + "?_key=" + encryptedKeyHex(key) + "&_pragma_cipher_page_size=4096&_pragma_busy_timeout=30000&_pragma_journal_mode=WAL&_pragma_synchronous=NORMAL&_pragma_temp_store=MEMORY"
}

func encryptedKeyLiteral(key string) string {
	return "x'" + encryptedKeyHex(key) + "'"
}

const (
	BootstrapName      = "security.json"
	KeyName            = ".db_key"
	migrationStateName = ".db-migration-state.json"
)

type migrationState struct {
	Phase string   `json:"phase"`
	Paths []string `json:"paths"`
}

// MigrationRecoveryPath is a non-secret durable marker used to prevent a
// restart from opening a pair whose canonical files may be mid-swap.
func MigrationRecoveryPath(paths []string) string {
	if len(paths) == 0 {
		return migrationStateName
	}
	return filepath.Join(filepath.Dir(paths[0]), migrationStateName)
}

func migrationRecoveryError(path string) error {
	return fmt.Errorf("database migration recovery is required; inspect %s, restore one consistent database pair, remove the marker, then rerun CORAL_DB_ENCRYPTION_MIGRATE=1", path)
}

// CheckMigrationRecovery refuses to open databases after an interrupted swap.
// The marker contains paths and phase only, never keys or passwords.
func CheckMigrationRecovery(paths []string) error {
	path := MigrationRecoveryPath(paths)
	if _, err := os.Stat(path); err == nil {
		return migrationRecoveryError(path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check database migration recovery state: %w", err)
	}
	return nil
}

func writeMigrationState(path, phase string, paths []string) error {
	data, err := json.Marshal(migrationState{Phase: phase, Paths: paths})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".db-migration-state.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// migrationFaultInjection is intentionally environment-gated and exists only
// so an isolated subprocess test can terminate at a swap boundary.
func migrationFaultInjection(phase string) {
	if os.Getenv("CORAL_DBCRYPT_TEST_EXIT_PHASE") == phase {
		os.Exit(91)
	}
}

type Bootstrap struct {
	DatabaseEncryption string `json:"database_encryption,omitempty"`
	KeyFile            string `json:"key_file,omitempty"`
	KeySalt            string `json:"key_salt,omitempty"`
}

func PrepareStorageKey(secret, mode, salt string) (string, error) {
	if mode == "password" {
		if salt == "" {
			return "", fmt.Errorf("password encryption salt is missing")
		}
		saltBytes, err := hex.DecodeString(salt)
		if err != nil || len(saltBytes) < 16 {
			return "", fmt.Errorf("password encryption salt is invalid")
		}
		return "rawhex:" + hex.EncodeToString(pbkdf2SHA256([]byte(secret), saltBytes, 600000, 32)), nil
	}
	if raw, err := base64.RawURLEncoding.DecodeString(secret); err == nil && len(raw) == 32 {
		return "rawhex:" + hex.EncodeToString(raw), nil
	}
	sum := sha256.Sum256([]byte(secret))
	return "rawhex:" + hex.EncodeToString(sum[:]), nil
}

func pbkdf2SHA256(password, salt []byte, iterations, size int) []byte {
	out := make([]byte, 0, size)
	for block := uint32(1); len(out) < size; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u, t := mac.Sum(nil), []byte(nil)
		t = append(t, u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:size]
}

var ErrUnlockRequired = errors.New("encrypted database unlock required")

var effectiveMode atomic.Value
var unlockSurface atomic.Value

func init() {
	effectiveMode.Store("disabled")
	unlockSurface.Store("headless")
}

func SetEffectiveMode(mode string) { effectiveMode.Store(mode) }
func EffectiveMode() string        { return effectiveMode.Load().(string) }
func SetUnlockSurface(surface string) {
	if surface == "native" || surface == "tty" || surface == "headless" {
		unlockSurface.Store(surface)
	}
}
func UnlockSurface() string { return unlockSurface.Load().(string) }

func keyPath(dir string, b Bootstrap) (string, error) {
	name := b.KeyFile
	if name == "" {
		name = KeyName
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(dir, name)
	}
	name = filepath.Clean(name)
	rel, err := filepath.Rel(filepath.Clean(dir), name)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("database key file must remain under the Coral data directory")
	}
	if info, err := os.Lstat(name); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("database key file must not be a symlink")
	}
	return name, nil
}

// Load reads only non-secret bootstrap metadata. Missing metadata means the
// backwards-compatible plaintext mode.
func Load(dir string) (Bootstrap, error) {
	data, err := os.ReadFile(filepath.Join(dir, BootstrapName))
	if os.IsNotExist(err) {
		return Bootstrap{}, nil
	}
	if err != nil {
		return Bootstrap{}, fmt.Errorf("read database security settings: %w", err)
	}
	var b Bootstrap
	if err := json.Unmarshal(data, &b); err != nil {
		return Bootstrap{}, fmt.Errorf("parse database security settings: %w", err)
	}
	if b.DatabaseEncryption == "" {
		b.DatabaseEncryption = "disabled"
	}
	if b.DatabaseEncryption != "disabled" && b.DatabaseEncryption != "key_file" && b.DatabaseEncryption != "password" {
		return Bootstrap{}, fmt.Errorf("unsupported database encryption mode %q", b.DatabaseEncryption)
	}
	return b, nil
}

// ResolveKey loads the explicitly configured key file. It creates a random
// key only for a brand-new data directory; an existing DB with a missing key
// fails closed rather than recreating or opening plaintext data.
func ResolveKey(dir string, b Bootstrap, dbPaths ...string) (string, error) {
	if b.DatabaseEncryption == "disabled" || b.DatabaseEncryption == "" {
		return "", nil
	}
	if b.DatabaseEncryption == "password" {
		return "", ErrUnlockRequired
	}
	name, err := keyPath(dir, b)
	if err != nil {
		return "", err
	}
	key, err := readExistingKey(name)
	if err == nil {
		key = []byte(strings.TrimSpace(string(key)))
		if len(key) < 16 {
			return "", fmt.Errorf("database key file is too short")
		}
		return string(key), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("read database key file: %w", err)
	}
	for _, p := range dbPaths {
		if _, statErr := os.Stat(p); statErr == nil {
			return "", fmt.Errorf("database key file %s is missing; refusing to recreate or open plaintext database", name)
		} else if !os.IsNotExist(statErr) {
			return "", fmt.Errorf("check database %s: %w", p, statErr)
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate database key: %w", err)
	}
	key = []byte(base64.RawURLEncoding.EncodeToString(raw))
	tmp, err := os.CreateTemp(filepath.Dir(name), ".db_key.tmp-*")
	if err != nil {
		return "", fmt.Errorf("create database key file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(key); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Link(tmpName, name); err != nil {
		return "", fmt.Errorf("install database key file: %w", err)
	}
	_ = os.Remove(tmpName)
	_ = syscall.Chmod(name, 0600)
	return string(key), nil
}

// GenerateKeyFile creates the local key file for an explicitly requested
// migration. It is separate from ResolveKey so an existing plaintext DB can
// never trigger an implicit migration.
func GenerateKeyFile(dir string, b Bootstrap) (string, error) {
	name, err := keyPath(dir, b)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(name); err == nil {
		data, err := readExistingKey(name)
		return strings.TrimSpace(string(data)), err
	} else if !os.IsNotExist(err) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	key := base64.RawURLEncoding.EncodeToString(raw)
	tmp, err := os.CreateTemp(filepath.Dir(name), ".db_key.tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.WriteString(key); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Link(tmpName, name); err != nil {
		return "", err
	}
	_ = os.Remove(tmpName)
	return key, nil
}

func EncryptedFile(path string) (bool, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	header := make([]byte, 16)
	n, err := f.Read(header)
	if err != nil && n == 0 {
		return false, err
	}
	return n == 16 && string(header) != "SQLite format 3\x00", nil
}

// MigratePlaintext atomically exports one plaintext SQLite file into an
// encrypted SQLCipher file. The caller must hold the per-home lock and ensure
// no other process has the file open. Existing data is retained as a clearly
// named plaintext backup; failures leave the source untouched.
func MigratePlaintext(path, key string) error {
	return MigratePlaintexts([]string{path}, key)
}

// MigratePlaintexts prepares and verifies every database before changing any
// canonical path. If preparation fails, all sources remain untouched. During
// the short install phase, any completed swaps are rolled back on error.
// Callers must hold the Coral instance lock and ensure no other process has
// the files open.
func MigratePlaintexts(paths []string, key string) error {
	if err := CheckMigrationRecovery(paths); err != nil {
		return err
	}
	type item struct{ path, tmp, backup string }
	items := make([]item, 0, len(paths))
	encryptedCount := 0
	for _, path := range paths {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			continue
		} else if statErr != nil {
			return statErr
		}
		encrypted, err := EncryptedFile(path)
		if err != nil {
			return err
		}
		if encrypted {
			encryptedCount++
			continue
		}
		items = append(items, item{path: path, tmp: path + ".encrypted.tmp"})
	}
	if encryptedCount > 0 && len(items) > 0 {
		return fmt.Errorf("database migration found a mixed encrypted/plaintext pair; recover both database files before retrying")
	}
	if len(items) == 0 {
		return nil
	}
	statePath := MigrationRecoveryPath(paths)
	if err := writeMigrationState(statePath, "prepared", paths); err != nil {
		return fmt.Errorf("record database migration recovery state: %w", err)
	}
	migrationFaultInjection("prepared")
	defer os.Remove(statePath)
	cleanup := func() {
		for _, it := range items {
			_ = os.Remove(it.tmp)
		}
	}
	defer cleanup()
	// Export and verify all files before touching a source or creating a backup.
	for i := range items {
		if err := exportPlaintext(items[i].path, items[i].tmp, key); err != nil {
			return fmt.Errorf("prepare encrypted database %s: %w", filepath.Base(items[i].path), err)
		}
		items[i].backup = items[i].path + ".plaintext-backup"
		if _, err := os.Stat(items[i].backup); err == nil {
			items[i].backup = fmt.Sprintf("%s.%d", items[i].backup, os.Getpid())
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	backedUp := 0
	installed := 0
	rollback := func() {
		for i := installed - 1; i >= 0; i-- {
			_ = os.Remove(items[i].path)
		}
		for i := backedUp - 1; i >= 0; i-- {
			_ = os.Rename(items[i].backup, items[i].path)
		}
	}
	for i := range items {
		if err := os.Rename(items[i].path, items[i].backup); err != nil {
			rollback()
			return fmt.Errorf("retain plaintext backup for %s: %w", filepath.Base(items[i].path), err)
		}
		backedUp++
		if err := writeMigrationState(statePath, "backed_up", paths); err != nil {
			rollback()
			return fmt.Errorf("update database migration recovery state: %w", err)
		}
		migrationFaultInjection("backed_up")
	}
	if err := writeMigrationState(statePath, "installing", paths); err != nil {
		rollback()
		return fmt.Errorf("update database migration recovery state: %w", err)
	}
	for i := range items {
		if err := os.Rename(items[i].tmp, items[i].path); err != nil {
			rollback()
			return fmt.Errorf("install encrypted database %s: %w", filepath.Base(items[i].path), err)
		}
		installed++
		migrationFaultInjection(fmt.Sprintf("installed_%d", i))
	}
	return nil
}

func exportPlaintext(path, tmp, key string) error {
	_ = os.Remove(tmp)
	src, err := sql.Open("sqlite3", path)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := src.Ping(); err != nil {
		return fmt.Errorf("open plaintext database: %w", err)
	}
	var integrity string
	if err := src.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("plaintext integrity check failed: %v", err)
	}
	if _, err := src.Exec("ATTACH DATABASE ? AS encrypted KEY ?;", tmp, encryptedKeyLiteral(key)); err != nil {
		return fmt.Errorf("attach encrypted database: %w", err)
	}
	if _, err := src.Exec("SELECT sqlcipher_export('encrypted'); DETACH DATABASE encrypted;"); err != nil {
		return fmt.Errorf("export encrypted database: %w", err)
	}
	check, err := sql.Open("sqlite3", encryptedDSN(tmp, key))
	if err != nil {
		return err
	}
	defer check.Close()
	if err := check.Ping(); err != nil {
		return fmt.Errorf("verify encrypted database: %w", err)
	}
	if err := check.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("encrypted integrity check failed: %v", err)
	}
	return nil
}

/*
legacy implementation retained in history; pair migration above is the only

	path used by startup.
*/
func migratePlaintextLegacy(path, key string) error {
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return nil
	} else if statErr != nil {
		return statErr
	}
	encrypted, err := EncryptedFile(path)
	if err != nil || encrypted {
		return err
	}
	tmp := path + ".encrypted.tmp"
	backup := path + ".plaintext-backup"
	_ = os.Remove(tmp)
	src, err := sql.Open("sqlite3", path)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := src.Ping(); err != nil {
		return fmt.Errorf("open plaintext database: %w", err)
	}
	if _, err := src.Exec("PRAGMA integrity_check"); err != nil {
		return fmt.Errorf("plaintext integrity check: %w", err)
	}
	if _, err := src.Exec("ATTACH DATABASE ? AS encrypted KEY ?;", tmp, encryptedKeyLiteral(key)); err != nil {
		return fmt.Errorf("attach encrypted database: %w", err)
	}
	_, exportErr := src.Exec("SELECT sqlcipher_export('encrypted'); DETACH DATABASE encrypted;")
	if exportErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("export encrypted database: %w", exportErr)
	}
	check, err := sql.Open("sqlite3", encryptedDSN(tmp, key))
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := check.Ping(); err != nil {
		check.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("verify encrypted database: %w", err)
	}
	var result string
	if err := check.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil || result != "ok" {
		check.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("encrypted integrity check failed: %v", err)
	}
	check.Close()
	if err := os.Rename(path, backup); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("retain plaintext backup: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Rename(backup, path)
		return fmt.Errorf("install encrypted database: %w", err)
	}
	return nil
}

func Save(dir string, b Bootstrap) error {
	if b.DatabaseEncryption == "" {
		b.DatabaseEncryption = "disabled"
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".security.tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, BootstrapName))
}
