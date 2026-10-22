package startup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/subgentic/coral/internal/logsafe"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/subgentic/coral/internal/agent"
	"github.com/subgentic/coral/internal/board"
	"github.com/subgentic/coral/internal/cmdhandoff"
	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/dbcrypt"
	"github.com/subgentic/coral/internal/store"
)

// prepareDataDirs makes the Coral home (this user only, #3637) and the database's folder (it may be elsewhere).
// A standard (non-sqlcipher) build cannot use database encryption: a requested or existing encrypted setup is
// refused first, before anything is created, saved, locked or opened, so a rejected start leaves the Coral home
// untouched.
func prepareDataDirs(cfg *config.Config, coralDir string) error {
	if err := dbcrypt.RejectUnsupportedEncryption(coralDir, cfg.DBPath, filepath.Join(coralDir, "messageboard.db")); err != nil {
		return err
	}
	if err := config.EnsureDataDir(coralDir); err != nil {
		return fmt.Errorf("failed to create data directory %s: %w", coralDir, err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}
	return nil
}

// resolveDatabaseKey resolves the databases' encryption before either is opened: the mode (security.json, or
// CORAL_DB_ENCRYPTION on a first start; missing security.json is the backwards-compatible plaintext setup), the key
// (a key file, generated for a requested migration; or the password, asked for), and a requested migration of
// plaintext databases. The board database is separate and must use the same mode and key. No listener or service
// has been started yet, so an unlock failure cannot expose partial state.
func resolveDatabaseKey(cfg *config.Config, coralDir, boardPath string, opts Options) (dbcrypt.Bootstrap, string, error) {
	security, err := dbcrypt.Load(coralDir)
	if err != nil {
		return security, "", err
	}
	// Normalize the empty bootstrap value before validating explicit modes.
	if security.DatabaseEncryption == "" {
		security.DatabaseEncryption = "disabled"
	}
	if envMode := strings.TrimSpace(os.Getenv("CORAL_DB_ENCRYPTION")); envMode != "" {
		if _, statErr := os.Stat(filepath.Join(coralDir, dbcrypt.BootstrapName)); os.IsNotExist(statErr) {
			security.DatabaseEncryption = envMode
			if err := dbcrypt.Save(coralDir, security); err != nil {
				return security, "", fmt.Errorf("save database encryption setting: %w", err)
			}
		}
	}
	if security.DatabaseEncryption != "disabled" && security.DatabaseEncryption != "key_file" && security.DatabaseEncryption != "password" {
		return security, "", fmt.Errorf("unsupported database encryption mode %q", security.DatabaseEncryption)
	}
	if err := dbcrypt.CheckMigrationRecovery([]string{cfg.DBPath, boardPath}); err != nil {
		return security, "", err
	}
	migrateRequested := strings.EqualFold(strings.TrimSpace(os.Getenv("CORAL_DB_ENCRYPTION_MIGRATE")), "1") || strings.EqualFold(strings.TrimSpace(os.Getenv("CORAL_DB_ENCRYPTION_MIGRATE")), "true")
	var dbKey string
	if migrateRequested && security.DatabaseEncryption == "key_file" {
		if err := refuseKeyFileReplacement(cfg, coralDir, boardPath, security); err != nil {
			return security, "", err
		}
		dbKey, err = dbcrypt.GenerateKeyFile(coralDir, security)
	} else {
		dbKey, err = dbcrypt.ResolveKey(coralDir, security, cfg.DBPath, boardPath)
	}
	if err != nil && !errors.Is(err, dbcrypt.ErrUnlockRequired) {
		return security, "", err
	}
	if errors.Is(err, dbcrypt.ErrUnlockRequired) {
		if opts.PasswordPrompt == nil {
			return security, "", fmt.Errorf("database encryption requires an interactive password prompt")
		}
		dbKey, err = opts.PasswordPrompt()
		if err != nil {
			return security, "", fmt.Errorf("database unlock cancelled: %w", err)
		}
		if strings.TrimSpace(dbKey) == "" {
			return security, "", fmt.Errorf("database unlock password is empty")
		}
	}
	if dbKey != "" && security.DatabaseEncryption != "disabled" {
		if security.DatabaseEncryption == "password" && security.KeySalt == "" {
			salt := make([]byte, 16)
			if _, saltErr := rand.Read(salt); saltErr != nil {
				return security, "", fmt.Errorf("generate password salt: %w", saltErr)
			}
			security.KeySalt = hex.EncodeToString(salt)
			if saveErr := dbcrypt.Save(coralDir, security); saveErr != nil {
				return security, "", fmt.Errorf("save password salt: %w", saveErr)
			}
		}
		if dbKey, err = dbcrypt.PrepareStorageKey(dbKey, security.DatabaseEncryption, security.KeySalt); err != nil {
			return security, "", err
		}
	}
	if migrateRequested && dbKey != "" {
		if err := dbcrypt.MigratePlaintexts([]string{cfg.DBPath, boardPath}, dbKey); err != nil {
			return security, "", fmt.Errorf("database migration failed: %w", err)
		}
	}
	return security, dbKey, nil
}

// refuseKeyFileReplacement: a migration to a key file whose key file is missing must not generate a new key while
// either database is already encrypted (with the lost key).
func refuseKeyFileReplacement(cfg *config.Config, coralDir, boardPath string, security dbcrypt.Bootstrap) error {
	keyPath := security.KeyFile
	if keyPath == "" {
		keyPath = dbcrypt.KeyName
	}
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(coralDir, keyPath)
	}
	if _, keyErr := os.Stat(keyPath); !os.IsNotExist(keyErr) {
		return nil
	}
	for _, path := range []string{cfg.DBPath, boardPath} {
		encrypted, probeErr := dbcrypt.EncryptedFile(path)
		if probeErr != nil {
			return probeErr
		}
		if encrypted {
			return fmt.Errorf("database key file is missing for encrypted database %s; refusing replacement", filepath.Base(path))
		}
	}
	return nil
}

// openDatabases opens the sessions database and probes the separate board database with the same key before the
// server is built: a wrong key fails closed rather than starting with only one database open.
func openDatabases(ctx context.Context, cfg *config.Config, boardPath, dbKey string, security dbcrypt.Bootstrap) (*store.DB, error) {
	openDone := noticeSlowOpen(cfg.DBPath)
	db, err := store.OpenWithKey(ctx, cfg.DBPath, dbKey)
	openDone()
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	dbcrypt.SetEffectiveMode(security.DatabaseEncryption)
	openDone = noticeSlowOpen(boardPath)
	boardProbe, err := board.NewStoreWithKey(boardPath, dbKey)
	openDone()
	if err != nil {
		db.Close()
		// Keep this fatal (#4924): starting over a damaged board would create a fresh one and lose its
		// history, so refuse and tell the user where the file is and what they can do about it.
		return nil, fmt.Errorf("the board database could not be opened: %w\nCoral will not start over a damaged board. Move %s aside (rename it, e.g. to messageboard.db.bak) to start with a fresh board, or restore it from a backup", err, boardPath)
	}
	boardProbe.Close()
	return db, nil
}

// prepareWithDatabase applies the saved settings that decide how the server binds (privacy, hub mode, Coral Hub
// max mode), then sets up what agents need before anything can launch one: per-session tokens (#3387), the agent
// tools, the launch temp folder and the session-state sweep, and removes stale files (an orphaned proxy CA, launch
// scripts a shell never sourced, older than a day: #4361).
func prepareWithDatabase(ctx context.Context, db *store.DB, cfg *config.Config, opts Options) error {
	applyPrivacySettings(ctx, db, cfg, opts.RemoteAccess, opts.RemoteAccessPrompt)
	applyHubModeSetting(ctx, db, cfg)
	if err := applyCloudMaxMode(ctx, db, cfg); err != nil {
		return err
	}
	setupSessionTokens(db, cfg.CoralDir())
	setupAgentTools(cfg.CoralDir())
	removeOrphanProxyCA(cfg.CoralDir())
	setupLaunchTempDir(db, cfg.CoralDir())
	setupSessionStateSweep(db, cfg.CoralDir())
	if dir := agent.LaunchTempDir(); dir != os.TempDir() {
		if n := cmdhandoff.SweepIn(dir); n > 0 {
			log.Printf("[startup] removed %d launch script(s) older than a day from %s", n, logsafe.Path(dir))
		}
	}
	if n := cmdhandoff.Sweep(); n > 0 {
		log.Printf("[startup] removed %d launch script(s) older than a day from the temp folder", n)
	}
	return nil
}

// prepareLogDir makes the agents' pipe log folder, readable by this user only (#3601, weak site
// W17): an older install made it 0755, so an existing folder is tightened too.
func prepareLogDir(dir string) {
	if dir == "" {
		return
	}
	os.MkdirAll(dir, 0o700)
	os.Chmod(dir, 0o700)
}
