// The update check and the latest released version (moved out of system.go, #5003; no behaviour change).

package routes

import (
	"context"
	"net/http"
	"time"

	"github.com/subgentic/coral/internal/config"
	"github.com/subgentic/coral/internal/settings"
	"github.com/subgentic/coral/internal/tracking"
	"github.com/subgentic/coral/internal/updatecheck"
)

// githubReleasesAPI is where the update check asks (tests point it at their own server).
var githubReleasesAPI = updatecheck.ReleasesAPI

// githubReleasesListAPI is the Agentic channel's feed (tests point it at their own server too).
var githubReleasesListAPI = updatecheck.ReleasesListAPI

const githubReleasesURL = updatecheck.ReleasesURL

// releaseFeeds are the two feed URLs this server asks: the channel only chooses between them (#5557).
func releaseFeeds() updatecheck.Feeds {
	return updatecheck.Feeds{Approved: githubReleasesAPI, Agentic: githubReleasesListAPI}
}

// updateChannel is the channel the settings name: anything but the word agentic is approved.
func updateChannel(m map[string]string) updatecheck.Channel {
	return updatecheck.ParseChannel(settings.Enum(m, settings.UpdateChannel))
}

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

// UpdateCheck returns update availability info for the channel the settings name.
// GET /api/system/update-check
func (h *SystemHandler) UpdateCheck(w http.ResponseWriter, r *http.Request) {
	var m map[string]string
	if h.ss != nil {
		m, _ = h.ss.GetSettings(r.Context())
	}
	ch := updateChannel(m)
	if !config.TierChecksForUpdates {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false,
			"current":   config.Version,
			"channel":   string(ch),
		})
		return
	}
	// Answered from a cache (an hour, a failure five minutes) with one request in flight at a time per feed,
	// for every caller, an agent's token included (#4133). ?refresh=1 is the dashboard's manual
	// "Check for Updates": it asks again, at most once a minute.
	feed, _ := updatecheck.Default.Feed(r.Context(), ch, releaseFeeds(), r.URL.Query().Get("refresh") == "1")
	latest, approved := "", true
	if feed != nil {
		latest, approved = feed.Offer.Version, !feed.Offer.Prerelease
	}
	// Only a strictly newer release: a dev, beta or newer build is never sent to an older one.
	available := updatecheck.Newer(latest, config.Version)
	if available {
		tracking.NoteOfferedRelease(latest) // the dashboard may report update events only for this release (#5952)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available":   available,
		"current":     config.Version,
		"latest":      latest,
		"release_url": updatecheck.ReleasePageURL(ch, latest),
		"channel":     string(ch),
		"approved":    approved,
	})
}

// addChannelFacts adds what the Updates pane needs about the channel to GET /api/system/update (#5557), all additive and all optional: channel
// (always), approved {version, approved_at}, past_approved, and on Agentic waiting [{version, published_at, notes}]. It never waits for GitHub: it
// reads the cached feed and starts a background fetch when there is none, so the next poll has it. The Hub gets no waiting list (that is the
// operator's view of this computer). Whether a release is approved comes from the pre-release flag and releases/latest, never from the notes.
func (h *UpdateRelaunchHandler) addChannelFacts(r *http.Request, out map[string]any) {
	var m map[string]string
	if h.Settings != nil {
		m, _ = h.Settings(r.Context())
	}
	ch := updateChannel(m)
	out["channel"] = string(ch)
	if !config.TierChecksForUpdates || h.Feeds == nil {
		return
	}
	feeds := h.Feeds()
	feed, err, ok := updatecheck.Default.PeekFeed(ch, feeds)
	if !ok {
		updatecheck.Default.WarmFeed(ch, feeds)
		return
	}
	if err != nil || feed == nil {
		return
	}
	if a := feed.Approved; a != nil {
		at := ""
		if !a.ApprovedAt.IsZero() {
			at = a.ApprovedAt.UTC().Format(time.RFC3339)
		}
		out["approved"] = map[string]any{"version": a.Version, "approved_at": at}
		if updatecheck.Newer(config.Version, a.Version) {
			out["past_approved"] = true
		}
	}
	if ch == updatecheck.Agentic && !viaTunnel(r) && len(feed.Waiting) > 0 {
		waiting := make([]map[string]any, 0, len(feed.Waiting))
		for _, w := range feed.Waiting {
			at := ""
			if !w.PublishedAt.IsZero() {
				at = w.PublishedAt.UTC().Format(time.RFC3339)
			}
			waiting = append(waiting, map[string]any{"version": w.Version, "published_at": at, "notes": w.Notes})
		}
		out["waiting"] = waiting
	}
}
