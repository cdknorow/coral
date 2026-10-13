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
	t.Logf("TierName=%s SkipEULA=%v SkipLicense=%v DemoLimits=%v",
		TierName, TierSkipEULA, TierSkipLicense, TierDemoLimits)

	switch TierName {
	case "prod":
		assert.False(t, TierSkipEULA, "prod: EULA should be required")
		assert.False(t, TierSkipLicense, "prod: license should be required")
		assert.False(t, TierDemoLimits, "prod: demo limits controlled by runtime LS plan")
	case "dev":
		assert.True(t, TierSkipEULA, "dev: EULA should be skipped")
		assert.True(t, TierSkipLicense, "dev: license should be skipped")
		assert.False(t, TierDemoLimits, "dev: no demo limits")
	case "beta":
		assert.False(t, TierSkipEULA, "beta: EULA should be required")
		assert.True(t, TierSkipLicense, "beta: license should be skipped")
		assert.True(t, TierDemoLimits, "beta: demo limits enforced")
	default:
		t.Fatalf("unexpected TierName: %s", TierName)
	}
}

func TestLicenseRequired(t *testing.T) {
	cfg := &Config{}
	switch TierName {
	case "prod":
		assert.True(t, cfg.LicenseRequired(), "prod: license required")
	case "dev", "beta":
		assert.False(t, cfg.LicenseRequired(), "dev/beta: license not required")
	}
}

func TestEULARequired(t *testing.T) {
	switch TierName {
	case "dev":
		assert.False(t, EULARequired(), "dev: EULA not required")
	case "beta", "prod":
		assert.True(t, EULARequired(), "beta/prod: EULA required")
	}
}

func TestDemoLimitsEnforced(t *testing.T) {
	switch TierName {
	case "beta":
		assert.True(t, DemoLimitsEnforced(), "beta: demo limits enforced")
	case "dev", "prod":
		assert.False(t, DemoLimitsEnforced(), "dev/prod: demo limits not enforced at tier level")
	}
}
