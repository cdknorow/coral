package startup

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerification_ApplyPrivacySettings_RemoteAccess_Disabled(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()
	_, err = db.ExecContext(ctx, "INSERT INTO user_settings (key, value) VALUES ('remote_access_enabled', 'false')")
	require.NoError(t, err)

	cfg := &config.Config{
		Host: "0.0.0.0",
		Port: 8420,
	}

	applyPrivacySettings(ctx, db, cfg)
	assert.Equal(t, "127.0.0.1", cfg.Host, "disabling remote access must force host to 127.0.0.1")
}

func TestVerification_ApplyPrivacySettings_RemoteAccess_EnabledOrUnset(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	defer db.Close()

	ctx := context.Background()

	// 1. Unset (defaults to enabled, preserves existing host)
	cfgUnset := &config.Config{Host: "0.0.0.0", Port: 8420}
	applyPrivacySettings(ctx, db, cfgUnset)
	assert.Equal(t, "0.0.0.0", cfgUnset.Host, "unset remote access must keep original host")

	// 2. Explicitly enabled
	_, err = db.ExecContext(ctx, "INSERT INTO user_settings (key, value) VALUES ('remote_access_enabled', 'true')")
	require.NoError(t, err)

	cfgEnabled := &config.Config{Host: "0.0.0.0", Port: 8420}
	applyPrivacySettings(ctx, db, cfgEnabled)
	assert.Equal(t, "0.0.0.0", cfgEnabled.Host, "enabled remote access must keep original host")
}

func TestVerification_ApplyPrivacySettings_Telemetry_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := store.Open(dbPath)
	require.NoError(t, err)

	// Close DB so GetSettings fails
	db.Close()

	ctx := context.Background()
	cfg := &config.Config{Host: "0.0.0.0"}

	// Must not panic, must fail-closed on DB error
	applyPrivacySettings(ctx, db, cfg)
	assert.Equal(t, "0.0.0.0", cfg.Host, "host should not be modified if DB query fails")
}
