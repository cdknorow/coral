//go:build dev && !beta && !dropboxers

package config

// Build tier: Dev
// Built with: go build -tags dev
// Skips the EULA. For internal development only.
// Coral has no licence and no usage limits in any tier (#3221): the EULA is the only difference.
var (
	TierSkipEULA         = true
	TierChecksForUpdates = false
	TierName             = "dev"
	// TierDevTools: only a dev build may open the app window's DevTools (script access to the signed-in dashboard), #78 W2.
	TierDevTools = true
	// The Coral Hub this build offers to connect to (#3221); nothing is sent until the user connects.
	TierDefaultCloudURL = ""
)
