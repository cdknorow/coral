// Package config provides centralized configuration for the Coral server.
// All intervals, timeouts, and paths are collected here.
package config

import (
	"os"
	"path/filepath"
	"strconv"
)

// Config holds all server configuration values.
type Config struct {
	// Database
	DBPath          string
	DBBusyTimeoutMS int

	// Server
	Host string
	Port int

	// Directories
	CoralRoot  string
	LogDir     string
	LogPattern string

	// Background task intervals (seconds)
	IndexerIntervalS         int
	IndexerStartupDelayS     int
	GitPollerIntervalS       int
	WebhookDispatcherIntervalS int
	IdleDetectorIntervalS    int
	BoardNotifierIntervalS   int
	RemotePollerIntervalS    int

	// WebSocket
	WSPollIntervalS int

	// Message board
	BoardPageSize               int
	BoardMaxLimit               int
	BoardPollIntervalS          int
	BoardSubscriberPollMultiplier int

	// Startup
	DeferredStartupDelayS int

	// History paths
	ClaudeProjectsDir string
	GeminiHistoryBase  string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	homeDir, _ := os.UserHomeDir()
	coralDir := filepath.Join(homeDir, ".coral")
	logDir := os.TempDir()

	cfg := &Config{
		DBPath:          filepath.Join(coralDir, "sessions.db"),
		DBBusyTimeoutMS: 5000,

		Host: envOrDefault("CORAL_HOST", "0.0.0.0"),
		Port: envIntOrDefault("CORAL_PORT", 8420),

		CoralRoot:  envOrDefault("CORAL_ROOT", ""),
		LogDir:     logDir,
		LogPattern: filepath.Join(logDir, "*_coral_*.log"),

		IndexerIntervalS:           120,
		IndexerStartupDelayS:       30,
		GitPollerIntervalS:         120,
		WebhookDispatcherIntervalS: 15,
		IdleDetectorIntervalS:      60,
		BoardNotifierIntervalS:     30,
		RemotePollerIntervalS:      30,

		WSPollIntervalS: 5,

		BoardPageSize:                50,
		BoardMaxLimit:                500,
		BoardPollIntervalS:           10,
		BoardSubscriberPollMultiplier: 3,

		DeferredStartupDelayS: 2,

		ClaudeProjectsDir: envOrDefault("CLAUDE_PROJECTS_DIR",
			filepath.Join(homeDir, ".claude", "projects")),
		GeminiHistoryBase: envOrDefault("GEMINI_TMP_DIR",
			filepath.Join(homeDir, ".gemini", "tmp")),
	}

	return cfg
}

// CoralDir returns the ~/.coral directory path.
func (c *Config) CoralDir() string {
	return filepath.Dir(c.DBPath)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOrDefault(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
