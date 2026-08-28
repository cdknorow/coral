package routes

import (
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"

	"github.com/cdknorow/coral/internal/config"
	"github.com/cdknorow/coral/internal/tracking"
)

// TrackingHandler exposes the small set of funnel events that can only be
// observed in the browser (link clicks). Everything else is emitted server-side.
type TrackingHandler struct{}

func NewTrackingHandler() *TrackingHandler { return &TrackingHandler{} }

// allowedTrackingEvents is a strict allowlist. The endpoint is reachable by
// anything running in the page, so it must never become a general-purpose
// event pipe.
var allowedTrackingEvents = map[string]bool{
	tracking.EventSupporterCheckoutClicked: true,
}

// allowedTrackingProps is the allowlist of property keys the browser may set.
// These carry placement and campaign attribution only — never prompts, code,
// repository names, agent output, personal information, or license keys.
var allowedTrackingProps = map[string]bool{
	"surface":  true, // where in the UI the link was clicked
	"campaign": true, // campaign attribution (utm_campaign)
	"source":   true, // campaign attribution (utm_source)
	"medium":   true, // campaign attribution (utm_medium)
}

// trackingPropValue constrains property values to a short slug so no free-form
// user text can be smuggled into an event.
var trackingPropValue = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// TrackEvent records a browser-observed funnel event.
// POST /api/tracking/event
func (h *TrackingHandler) TrackEvent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Event string            `json:"event"`
		Props map[string]string `json:"props"`
	}
	if err := decodeJSON(r, &body); err != nil {
		errBadRequest(w, "invalid JSON")
		return
	}
	if !allowedTrackingEvents[body.Event] {
		errBadRequest(w, "unknown event")
		return
	}

	props := sanitizeTrackingProps(body.Props)
	tracking.TrackEvent(body.Event, props)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sanitizeTrackingProps drops every key not on the allowlist and every value
// that is not a short slug. It returns nil when nothing survives.
func sanitizeTrackingProps(in map[string]string) map[string]string {
	var out map[string]string
	for k, v := range in {
		if !allowedTrackingProps[k] || !trackingPropValue.MatchString(v) {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// ── Supporter link attribution ────────────────────────────────────────────
//
// Every in-product supporter link carries attribution so we can tell which
// surface produced a supporter. Two parameter families are attached:
//
//   checkout[custom][...]  Lemon Squeezy's documented custom-data passthrough.
//                          LS surfaces these as meta.custom_data on the Order,
//                          Subscription, and License Key webhooks, which is what
//                          closes the loop from click to completed purchase.
//   utm_*                  Standard campaign parameters, so the same convention
//                          works for README and campaign links that do not
//                          terminate at Lemon Squeezy.
//
// Only non-identifying values are attached: a fixed source, the UI surface, the
// campaign, and the app version. Never the install ID or anything user-derived.

// Supporter link surfaces. These are the `surface` values on both the
// attribution parameters and the supporter_checkout_clicked event.
const (
	SurfaceSettingsTierBadge    = "settings_tier_badge"
	SurfaceLicenseSettingsPanel = "license_settings_panel"
	SurfaceActivationNag        = "activation_nag"
)

// supporterSource identifies clicks originating inside the app, as opposed to
// the README, docs site, or a campaign link.
const supporterSource = "coral_app"

// supporterCampaign is the campaign for organic in-product clicks. Paid or
// syndicated links use their own campaign value.
const supporterCampaign = "in_app"

// supporterSurfaces are the surfaces exposed to the frontend via
// GET /api/system/status.
var supporterSurfaces = []string{
	SurfaceSettingsTierBadge,
	SurfaceLicenseSettingsPanel,
	SurfaceActivationNag,
}

// SupporterCheckoutURL returns the store URL with attribution parameters for
// one UI surface. An unparseable base is returned unchanged so a bad config can
// never produce a broken or empty link — the worst case is an untracked click.
func SupporterCheckoutURL(base, surface string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return base
	}
	q := u.Query()
	q.Set("checkout[custom][source]", supporterSource)
	q.Set("checkout[custom][surface]", surface)
	q.Set("checkout[custom][campaign]", supporterCampaign)
	q.Set("utm_source", supporterSource)
	q.Set("utm_medium", "in_app")
	q.Set("utm_campaign", supporterCampaign)
	if config.Version != "" {
		q.Set("checkout[custom][version]", config.Version)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// SupporterCheckoutURLs returns the attributed store URL for every surface,
// keyed by surface name. Built from the same base as store_url so the
// /api/system/status override keeps working.
func SupporterCheckoutURLs(base string) map[string]string {
	out := make(map[string]string, len(supporterSurfaces))
	for _, s := range supporterSurfaces {
		out[s] = SupporterCheckoutURL(base, s)
	}
	return out
}

// ── Telemetry disclosure ─────────────────────────────────────────────────

// TelemetryDisclosure returns everything the first-run disclosure needs to
// render, generated from the tracking package's own event list so the UI can
// never describe a different set of events than the one Coral sends.
// GET /api/system/telemetry
func (h *TrackingHandler) TelemetryDisclosure(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		// enabled is false for builds compiled from source, which carry no
		// analytics key and send nothing. The disclosure is not shown then:
		// warning users about collection that is not happening is misleading.
		"enabled":         tracking.Enabled(),
		"acknowledged":    tracking.DisclosureAcknowledged(),
		"events":          tracking.AllEvents,
		"properties":      tracking.StandardProperties,
		"never_collected": tracking.NeverCollected,
		"install_id_path": filepath.Join(tracking.CoralDir(), ".install_id"),
		"failure_log":     filepath.Join(tracking.CoralDir(), "tracking-failures.log"),
	})
}

// AcknowledgeTelemetryDisclosure records that the user has seen the
// disclosure, so it does not appear again.
// POST /api/system/telemetry/acknowledge
func (h *TrackingHandler) AcknowledgeTelemetryDisclosure(w http.ResponseWriter, r *http.Request) {
	if err := tracking.AcknowledgeDisclosure(); err != nil {
		errInternalServer(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "acknowledged": true})
}
