package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadDefaults(t *testing.T) {
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
	assert.Contains(t, cfg.DBPath, "sessions.db")
}

func TestLoadFromEnv(t *testing.T) {
	os.Setenv("CORAL_PORT", "9000")
	os.Setenv("CORAL_HOST", "127.0.0.1")
	defer func() {
		os.Unsetenv("CORAL_PORT")
		os.Unsetenv("CORAL_HOST")
	}()

	cfg := Load()
	assert.Equal(t, 9000, cfg.Port)
	assert.Equal(t, "127.0.0.1", cfg.Host)
}

func TestEnvIntInvalid(t *testing.T) {
	os.Setenv("CORAL_PORT", "notanumber")
	defer os.Unsetenv("CORAL_PORT")

	cfg := Load()
	assert.Equal(t, 8420, cfg.Port) // fallback to default
}
