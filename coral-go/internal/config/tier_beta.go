//go:build beta && !dev && !dropboxers

package config

// Build tier: Beta
// Built with: go build -tags beta
// Requires EULA, skips license, enforces demo limits (3 teams / 12 agents).
var (
	TierSkipEULA    = false
	TierSkipLicense = true
	TierDemoLimits  = true
	TierName        = "beta"
	TierMaxTeams    = 3
	TierMaxAgents   = 12

	// Test store (Lemon Squeezy sandbox) — single product with built-in free trial
	StoreURL = "https://store.coralai.ai/checkout/buy/44df39dc-9891-4094-8b77-f73c1d2596ae"
)
