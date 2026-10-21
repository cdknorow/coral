package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTierValues verifies the compile-time tier configuration.
// Run with different build tags to test each tier:
//
//	go test ./internal/config/ -run TestTier           → prod tier
//	go test -tags dev ./internal/config/ -run TestTier  → dev tier
//	go test -tags beta ./internal/config/ -run TestTier → beta tier
func TestTierValues(t *testing.T) {
	t.Logf("TierName=%s SkipEULA=%v ChecksForUpdates=%v", TierName, TierSkipEULA, TierChecksForUpdates)

	switch TierName {
	case "prod", "staging":
		assert.False(t, TierSkipEULA, "prod: EULA should be required")
		assert.True(t, TierChecksForUpdates, "prod: checks for a newer release")
	case "beta":
		assert.False(t, TierSkipEULA, "beta: EULA should be required")
		assert.False(t, TierChecksForUpdates, "beta: no update check")
	case "dev", "dropboxers":
		assert.True(t, TierSkipEULA, "dev: EULA should be skipped")
		assert.False(t, TierChecksForUpdates, "dev: no update check")
	default:
		t.Fatalf("unexpected TierName: %s", TierName)
	}
}

// #78 W2: only a dev build may open the app window's DevTools; every release tier (prod, staging, beta, dropboxers) keeps them closed.
func TestOnlyADevBuildHasDevTools(t *testing.T) {
	assert.Equal(t, TierName == "dev", TierDevTools, "tier %s", TierName)
}

func TestEULARequired(t *testing.T) {
	switch TierName {
	case "dev", "dropboxers":
		assert.False(t, EULARequired(), "dev: EULA not required")
	case "beta", "prod", "staging":
		assert.True(t, EULARequired(), "beta/prod: EULA required")
	}
}
