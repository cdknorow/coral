//go:build staging && !dev && !beta && !dropboxers

package config

// Build tier: Staging
// Built with: go build -tags staging
// Same as prod: requires the EULA and checks for a newer release.
// Coral has no licence and no usage limits in any tier (#3221): the EULA is the only difference.
var (
	TierSkipEULA         = false
	TierChecksForUpdates = true
	TierName             = "staging"
	// TierDevTools: only a dev build may open the app window's DevTools (script access to the signed-in dashboard), #78 W2.
	TierDevTools = false
	// The Coral Hub this build offers to connect to (#3221); nothing is sent until the user connects.
	TierDefaultCloudURL = "https://app.coralhub.dev"
)
