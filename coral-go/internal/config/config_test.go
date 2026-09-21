package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// isolateConfigEnv makes Load() hermetic. Coral exports CORAL_DATA_DIR,
// CORAL_DIR, CORAL_HOST and CORAL_PORT into every agent shell, and Load()
// creates its data directory, so an unisolated test both asserts against the
// caller's live settings and touches the real install. It returns the
// temporary data directory.
func isolateConfigEnv(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CORAL_DATA_DIR", dataDir)
	t.Setenv("CORAL_DIR", dataDir)
	t.Setenv("CORAL_HOST", "")
	t.Setenv("CORAL_PORT", "")
	t.Setenv("CORAL_ROOT", "")
	return dataDir
}

func TestLoadDefaults(t *testing.T) {
	dataDir := isolateConfigEnv(t)
	cfg := Load()

	assert.Equal(t, 8420, cfg.Port)
	assert.Equal(t, "0.0.0.0", cfg.Host)
	assert.Equal(t, 5000, cfg.DBBusyTimeoutMS)
	assert.Equal(t, 120, cfg.IndexerIntervalS)
	assert.Equal(t, 120, cfg.GitPollerIntervalS)
	assert.Equal(t, 15, cfg.WebhookDispatcherIntervalS)
	assert.Equal(t, 60, cfg.IdleDetectorIntervalS)
	assert.Equal(t, 30, cfg.BoardNotifierIntervalS)
	assert.Equal(t, 5, cfg.WSPollIntervalS)
	assert.Equal(t, dataDir, cfg.CoralDir())
	assert.Equal(t, filepath.Join(dataDir, "sessions.db"), cfg.DBPath)
}

func TestLoadFromEnv(t *testing.T) {
	isolateConfigEnv(t)
	t.Setenv("CORAL_PORT", "9000")
	t.Setenv("CORAL_HOST", "127.0.0.1")

	cfg := Load()
	assert.Equal(t, 9000, cfg.Port)
	assert.Equal(t, "127.0.0.1", cfg.Host)
}

func TestEnvIntInvalid(t *testing.T) {
	isolateConfigEnv(t)
	t.Setenv("CORAL_PORT", "notanumber")

	cfg := Load()
	assert.Equal(t, 8420, cfg.Port) // fallback to default
}
