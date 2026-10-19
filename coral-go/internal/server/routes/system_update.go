// The update check and the latest released version (moved out of system.go, #5003; no behaviour change).

package routes

import (
	"context"
	"net/http"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/updatecheck"
)

// githubReleasesAPI is where the update check asks (tests point it at their own server).
var githubReleasesAPI = updatecheck.ReleasesAPI

const githubReleasesURL = updatecheck.ReleasesURL

// FetchLatestVersion is the latest release's version (without "v"), or "" on any error: a reply
// that isn't a 200, too large, or a tag that isn't a version (updatecheck, #3660).
func FetchLatestVersion() string {
	v, _ := updatecheck.Default.Latest(context.Background(), githubReleasesAPI)
	return v
}

// fetchLatestVersionRefresh is FetchLatestVersion for a person's "Check for Updates": it asks
// GitHub again when the cached answer is over a minute old (updatecheck.RefreshAfter), else it
// returns the cached one, so the button can't be used to hammer GitHub (#4133).
func fetchLatestVersionRefresh() string {
	v, _ := updatecheck.Default.Refresh(context.Background(), githubReleasesAPI)
	return v
}

// UpdateCheck returns update availability info.
// GET /api/system/update-check
func (h *SystemHandler) UpdateCheck(w http.ResponseWriter, r *http.Request) {
	if !config.TierChecksForUpdates {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"current":   config.Version,
		})
		return
	}
	// Answered from a cache (an hour, a failure five minutes) with one request in flight at a time,
	// for every caller, an agent's token included (#4133). ?refresh=1 is the dashboard's manual
	// "Check for Updates": it asks again, at most once a minute.
	var latest string
	if r.URL.Query().Get("refresh") == "1" {
		latest = fetchLatestVersionRefresh()
	} else {
		latest = FetchLatestVersion()
	}
	// Only a strictly newer release: a dev, beta or newer build is never sent to an older one.
	available := updatecheck.Newer(latest, config.Version)
	writeJSON(w, http.StatusOK, map[string]any{
		"available":   available,
		"current":     config.Version,
		"latest":      latest,
		"release_url": githubReleasesURL,
	})
}
