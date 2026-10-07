// Package config provides centralized configuration for the Coral server.
// All intervals, timeouts, and paths are collected here.
package config

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PostHogKey is set at build time via ldflags for install tracking.
var PostHogKey string

// Version is set at build time via ldflags.
var Version string

// Build tier variables are set in tier_dev.go, tier_beta.go, or tier_prod.go
// based on compile-time build tags (-tags dev, -tags beta, or default).
// See those files for the values per tier.

// Config holds all server configuration values.
type Config struct {
	// coralDir is the root data directory for all Coral state.
	// Set by Load() from --home flag, CORAL_DATA_DIR, or ~/.coral.
	coralDir string

	// Database
	DBPath          string
	DBBusyTimeoutMS int

	// Server
	Host string
	Port int

	// Directories
	CoralRoot string
	LogDir    string

	// Background task intervals (seconds)
	IndexerIntervalS           int
	IndexerStartupDelayS       int
	GitPollerIntervalS         int
	WebhookDispatcherIntervalS int
	IdleDetectorIntervalS      int
	BoardNotifierIntervalS     int
	TaskIdleReminderS          int
	TaskIdleEscalationS        int
	RemotePollerIntervalS      int

	// WebSocket
	WSPollIntervalS int

	// Edition limits (0 = unlimited)
	MaxLiveTeams  int
	MaxLiveAgents int

	// Task dependencies
	MaxTaskDependencyDepth int
}

// Load reads configuration from environment variables with sensible defaults.
// If dataDir is non-empty, it overrides all other data directory settings
// (CORAL_DATA_DIR, CORAL_DIR, ~/.coral). Pass "" to use the default.
func Load(dataDir ...string) *Config {
	homeDir, _ := os.UserHomeDir()
	coralDir := filepath.Join(homeDir, ".coral")
	logDir := os.TempDir()

	// CLI --home flag takes highest precedence
	if len(dataDir) > 0 && dataDir[0] != "" {
		coralDir = dataDir[0]
	} else if envDir := os.Getenv("CORAL_DATA_DIR"); envDir != "" {
		// CORAL_DATA_DIR takes precedence (matches Python), CORAL_DIR as fallback alias.
		coralDir = envDir
	} else if envDir := os.Getenv("CORAL_DIR"); envDir != "" {
		coralDir = envDir
	}

	// Ensure data directory exists
	os.MkdirAll(coralDir, 0755)

	cfg := &Config{
		coralDir:        coralDir,
		DBPath:          filepath.Join(coralDir, "sessions.db"),
		DBBusyTimeoutMS: 5000,

		Host: envOrDefault("CORAL_HOST", "0.0.0.0"),
		Port: envIntOrDefault("CORAL_PORT", DefaultPort),

		CoralRoot: envOrDefault("CORAL_ROOT", homeDir),
		LogDir:    logDir,

		IndexerIntervalS:           120,
		IndexerStartupDelayS:       30,
		GitPollerIntervalS:         120,
		WebhookDispatcherIntervalS: 15,
		IdleDetectorIntervalS:      60,
		BoardNotifierIntervalS:     30,
		TaskIdleReminderS:          envIntOrDefault("CORAL_TASK_IDLE_REMINDER_SECONDS", 1800),
		TaskIdleEscalationS:        envIntOrDefault("CORAL_TASK_IDLE_ESCALATION_SECONDS", 3600),
		RemotePollerIntervalS:      30,

		WSPollIntervalS: 5,

		MaxTaskDependencyDepth: 3,
	}

	// Demo limits from build tier (beta) or runtime LS plan (prod)
	if TierDemoLimits {
		cfg.MaxLiveTeams = TierMaxTeams
		cfg.MaxLiveAgents = TierMaxAgents
	}

	return cfg
}

// LicenseRequired returns true if license validation should be enforced.
// False for dev and beta tiers (set at compile time via build tags).
func (c *Config) LicenseRequired() bool {
	return !TierSkipLicense
}

// EULARequired returns true if the EULA acceptance dialog should be shown.
// False for dev tier (set at compile time via build tags).
func EULARequired() bool {
	return !TierSkipEULA
}

// DemoLimitsEnforced returns true if demo edition limits (2 teams / 8 agents)
// should be enforced. True for beta tier.
func DemoLimitsEnforced() bool {
	return TierDemoLimits
}

// CoralDir returns the root data directory for all Coral state.
// Controlled by --home flag, CORAL_DATA_DIR env var, or defaults to ~/.coral.
func (c *Config) CoralDir() string {
	return c.coralDir
}

// IsLoopbackHost reports whether a bind host only accepts local connections.
// Wildcard and empty hosts are not loopback: they listen on every interface.
func IsLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
