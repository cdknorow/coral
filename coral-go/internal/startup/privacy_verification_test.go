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

func TestVerification_ApplyPrivacySettings_RemoteAccess_OptIn(t *testing.T) {
	ctx := context.Background()
	open := func(t *testing.T) *store.DB {
		t.Helper()
		db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
		require.NoError(t, err)
		t.Cleanup(func() { db.Close() })
		return db
	}
	hostFor := func(t *testing.T, db *store.DB, host string) string {
		t.Helper()
		cfg := &config.Config{Host: host, Port: 8420}
		applyPrivacySettings(ctx, db, cfg)
		return cfg.Host
	}

	// New install / no saved setting: local-only, whatever host was requested.
	db := open(t)
	for _, requested := range []string{"0.0.0.0", "::", "", "192.168.1.20"} {
		assert.Equal(t, "127.0.0.1", hostFor(t, db, requested), "unset remote access must bind local-only (requested %q)", requested)
	}

	// Explicit opt-in is preserved exactly as configured.
	_, err := db.ExecContext(ctx, "INSERT INTO user_settings (key, value) VALUES ('remote_access_enabled', 'true')")
	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0", hostFor(t, db, "0.0.0.0"), "explicit opt-in keeps the configured bind")
	assert.Equal(t, "127.0.0.1", hostFor(t, db, "127.0.0.1"), "an explicit loopback host stays loopback")

	// Only the literal "true" opts in: other values and explicit false stay local.
	for _, value := range []string{"false", "False", "1", "yes", "enabled", ""} {
		db := open(t)
		_, err := db.ExecContext(ctx, "INSERT INTO user_settings (key, value) VALUES ('remote_access_enabled', ?)", value)
		require.NoError(t, err)
		assert.Equal(t, "127.0.0.1", hostFor(t, db, "0.0.0.0"), "saved value %q must not enable remote access", value)
	}
}

// If the opt-in cannot be read, the bind must fail closed rather than widen.
func TestVerification_ApplyPrivacySettings_RemoteAccess_UnreadableSettingsFailClosed(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	cfg := &config.Config{Host: "0.0.0.0", Port: 8420}
	applyPrivacySettings(context.Background(), db, cfg)
	assert.Equal(t, "127.0.0.1", cfg.Host)
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
	assert.Equal(t, "127.0.0.1", cfg.Host, "an unreadable opt-in must fail closed to a local-only bind")
}
