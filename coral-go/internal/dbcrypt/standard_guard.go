package dbcrypt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrEncryptionUnavailable is returned when encryption is requested or
// required but this binary was not built with SQLCipher support.
var ErrEncryptionUnavailable = errors.New("database encryption is not available in this Coral build")

// BuildVariant names the database build: "standard" (pure-Go, plaintext only)
// or "sqlcipher" (opt-in encrypted build).
func BuildVariant() string {
	if FeatureAvailable() {
		return "sqlcipher"
	}
	return "standard"
}

func unavailable(detail string) error {
	return fmt.Errorf("%w%s: %s. Install or build the opt-in sqlcipher variant (go build -tags sqlcipher,fts5 with CGO_ENABLED=1); this build will not create, replace, migrate or downgrade any database", ErrEncryptionUnavailable, buildNote, detail)
}

// RequireAvailable is the guard for code that is about to use a key.
func RequireAvailable() error {
	if FeatureAvailable() {
		return nil
	}
	return unavailable("a database key was supplied")
}

func envTruthy(v string) bool {
	v = strings.TrimSpace(v)
	return strings.EqualFold(v, "1") || strings.EqualFold(v, "true")
}

// RejectUnsupportedEncryption refuses to continue in a standard build when
// encryption is requested or already in use. It only reads: it never writes,
// creates, renames or removes anything, so calling it first guarantees a
// rejected startup leaves the Coral home exactly as it found it. It is a no-op
// in sqlcipher builds.
func RejectUnsupportedEncryption(coralDir string, dbPaths ...string) error {
	if FeatureAvailable() {
		return nil
	}
	if mode := strings.TrimSpace(os.Getenv("CORAL_DB_ENCRYPTION")); mode != "" && !strings.EqualFold(mode, "disabled") {
		return unavailable(fmt.Sprintf("CORAL_DB_ENCRYPTION=%q requests encryption", mode))
	}
	if envTruthy(os.Getenv("CORAL_DB_ENCRYPTION_MIGRATE")) {
		return unavailable("CORAL_DB_ENCRYPTION_MIGRATE requests an encryption migration")
	}
	b, err := Load(coralDir)
	if err != nil {
		return err
	}
	if b.DatabaseEncryption != "" && b.DatabaseEncryption != "disabled" {
		return unavailable(fmt.Sprintf("%s records database encryption mode %q", BootstrapName, b.DatabaseEncryption))
	}
	if _, statErr := os.Stat(MigrationRecoveryPath(dbPaths)); statErr == nil {
		return unavailable("an encryption migration recovery marker is present")
	}
	for _, p := range dbPaths {
		encrypted, probeErr := EncryptedFile(p)
		if probeErr != nil {
			return fmt.Errorf("check database %s: %w", filepath.Base(p), probeErr)
		}
		if encrypted {
			return unavailable(fmt.Sprintf("%s is encrypted (or is not a plaintext SQLite database)", filepath.Base(p)))
		}
	}
	return nil
}

// RejectEncryptedFile is the direct-open guard for the plaintext path: in a
// standard build it refuses a database whose header is not plaintext SQLite,
// read-only, before any directory, schema or file is created. It is a no-op
// in sqlcipher builds and for missing or empty files.
func RejectEncryptedFile(path string) error {
	if FeatureAvailable() {
		return nil
	}
	encrypted, err := EncryptedFile(path)
	if err != nil {
		return fmt.Errorf("check database %s: %w", filepath.Base(path), err)
	}
	if encrypted {
		return unavailable(fmt.Sprintf("%s is encrypted (or is not a plaintext SQLite database)", filepath.Base(path)))
	}
	return nil
}
