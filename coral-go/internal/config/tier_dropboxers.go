//go:build dropboxers && !dev && !beta

package config

// Build tier: Dropboxers
// Built with: go build -tags dropboxers
// Partner build ("Coral For Dropbox"): skips the EULA.
// Coral has no licence and no usage limits in any tier (#3221): the EULA is the only difference.
var (
	TierSkipEULA         = true
	TierChecksForUpdates = false
	TierName             = "dropboxers"
	// TierDevTools: only a dev build may open the app window's DevTools (script access to the signed-in dashboard), #78 W2.
	TierDevTools = false
	// The Coral Hub this build offers to connect to (#3221); nothing is sent until the user connects.
	TierDefaultCloudURL = ""
)
