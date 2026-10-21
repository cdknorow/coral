//go:build !dev && !beta && !dropboxers && !staging

package config

// Build tier: Prod (default)
// Built with: go build (no tags)
// Requires the EULA; checks GitHub for a newer release at startup.
// Coral has no licence and no usage limits in any tier (#3221): the EULA is the only difference.
var (
	TierSkipEULA         = false
	TierChecksForUpdates = true
	TierName             = "prod"
	// TierDevTools: only a dev build may open the app window's DevTools (script access to the signed-in dashboard), #78 W2.
	TierDevTools = false
	// The Coral Hub this build offers to connect to (#3221); nothing is sent until the user connects.
	TierDefaultCloudURL = "https://app.coralhub.dev"
)
