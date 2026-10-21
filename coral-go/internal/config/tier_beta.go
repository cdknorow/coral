//go:build beta && !dev && !dropboxers

package config

// Build tier: Beta
// Built with: go build -tags beta
// Same as prod: requires the EULA. Kept so v*-beta tags still build.
// Coral has no licence and no usage limits in any tier (#3221): the EULA is the only difference.
var (
	TierSkipEULA         = false
	TierChecksForUpdates = false
	TierName             = "beta"
	// TierDevTools: only a dev build may open the app window's DevTools (script access to the signed-in dashboard), #78 W2.
	TierDevTools = false
	// The Coral Hub this build offers to connect to (#3221); nothing is sent until the user connects.
	TierDefaultCloudURL = "https://app.coralhub.dev"
)
