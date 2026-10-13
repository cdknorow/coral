//go:build dropboxers && !dev && !beta

package config

// Build tier: Dropboxers
// Built with: go build -tags dropboxers
// Requires EULA, skips license, enforces demo limits (3 teams / 15 agents).
var (
	TierSkipEULA    = true
	TierSkipLicense = true
	TierDemoLimits  = true
	TierName        = "dropboxers"
	TierMaxTeams    = 3
	TierMaxAgents   = 15

	// Test store (Lemon Squeezy sandbox) — single product with built-in free trial
	StoreURL = "https://store.coralai.ai/checkout/buy/44df39dc-9891-4094-8b77-f73c1d2596ae"
)
